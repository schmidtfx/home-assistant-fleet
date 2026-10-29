// Package store persists users, sessions, HA instances and the audit log in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

var migrations = []string{
	`CREATE TABLE users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		username      TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		created_at    INTEGER NOT NULL
	);
	CREATE TABLE sessions (
		id_hash    TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		csrf_token TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE instances (
		id                  TEXT PRIMARY KEY,
		name                TEXT NOT NULL,
		url                 TEXT NOT NULL,
		token_enc           BLOB NOT NULL,
		ca_pem              TEXT NOT NULL DEFAULT '',
		cert_fingerprint    TEXT NOT NULL DEFAULT '',
		allow_insecure_http INTEGER NOT NULL DEFAULT 0,
		enabled             INTEGER NOT NULL DEFAULT 1,
		created_at          INTEGER NOT NULL,
		updated_at          INTEGER NOT NULL
	);
	CREATE TABLE audit (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		ts            INTEGER NOT NULL,
		username      TEXT NOT NULL,
		instance_id   TEXT NOT NULL,
		instance_name TEXT NOT NULL,
		action        TEXT NOT NULL,
		target        TEXT NOT NULL DEFAULT '',
		result        TEXT NOT NULL,
		message       TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX audit_ts ON audit(ts DESC);`,
	`ALTER TABLE instances ADD COLUMN ai_enabled INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE ai_cache (
		key        TEXT PRIMARY KEY,
		kind       TEXT NOT NULL,
		result     TEXT NOT NULL,
		provider   TEXT NOT NULL,
		model      TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);`,
	`CREATE TABLE acks (
		instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
		task_id     TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		username    TEXT NOT NULL,
		note        TEXT NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL,
		expires_at  INTEGER,
		PRIMARY KEY (instance_id, task_id)
	);`,
	`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'admin';`,
	`CREATE TABLE task_history (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
		task_id     TEXT NOT NULL,
		kind        TEXT NOT NULL,
		severity    TEXT NOT NULL,
		title       TEXT NOT NULL,
		opened_at   INTEGER NOT NULL,
		closed_at   INTEGER
	);
	CREATE INDEX task_history_open ON task_history(instance_id, task_id) WHERE closed_at IS NULL;
	CREATE INDEX task_history_closed ON task_history(closed_at);`,
	`ALTER TABLE instances ADD COLUMN maint_window TEXT NOT NULL DEFAULT '';
	CREATE TABLE rollouts (
		id             TEXT PRIMARY KEY,
		created_by     TEXT NOT NULL,
		entity_id      TEXT NOT NULL,
		title          TEXT NOT NULL,
		target_version TEXT NOT NULL,
		backup         INTEGER NOT NULL,
		canary_count   INTEGER NOT NULL,
		gate_minutes   INTEGER NOT NULL,
		start_at       INTEGER,
		state          TEXT NOT NULL,
		message        TEXT NOT NULL DEFAULT '',
		created_at     INTEGER NOT NULL,
		updated_at     INTEGER NOT NULL
	);
	CREATE TABLE rollout_steps (
		rollout_id  TEXT NOT NULL REFERENCES rollouts(id) ON DELETE CASCADE,
		ord         INTEGER NOT NULL,
		instance_id TEXT NOT NULL,
		canary      INTEGER NOT NULL,
		state       TEXT NOT NULL,
		action_id   TEXT NOT NULL DEFAULT '',
		message     TEXT NOT NULL DEFAULT '',
		started_at  INTEGER,
		finished_at INTEGER,
		PRIMARY KEY (rollout_id, ord)
	);`,
	`ALTER TABLE instances ADD COLUMN publish_status INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE rollouts ADD COLUMN ai_gate INTEGER NOT NULL DEFAULT 0;`,
	`CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	ALTER TABLE instances ADD COLUMN settings TEXT NOT NULL DEFAULT '';`,
}

// Open opens (and creates if needed) the database in dataDir and applies migrations.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dataDir, "hafleet.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	return open(dsn)
}

// OpenMemory opens an in-memory database (for tests).
func OpenMemory() (*Store, error) {
	return open("file::memory:?_pragma=foreign_keys(1)")
}

func open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: serialize writers, also keeps :memory: on one connection
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var v int
	err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.Exec(`INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`UPDATE schema_version SET version = ?`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---- users ----

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
}

func (s *Store) CreateUser(ctx context.Context, username, passwordHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		username, passwordHash, time.Now().Unix())
	return err
}

func (s *Store) SetUserPassword(ctx context.Context, username, passwordHash string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE username = ?`, passwordHash, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserRole changes a user's role. Callers validate the role name.
func (s *Store) SetUserRole(ctx context.Context, username, role string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET role = ? WHERE username = ?`, role, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UserByName(ctx context.Context, username string) (*User, error) {
	u := &User{}
	err := s.db.QueryRowContext(ctx, `SELECT id, username, password_hash, role FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ---- sessions ----

type Session struct {
	UserID    int64
	Username  string
	Role      string
	CSRFToken string
	ExpiresAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, idHash string, userID int64, csrf string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id_hash, user_id, csrf_token, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		idHash, userID, csrf, time.Now().Unix(), expires.Unix())
	return err
}

func (s *Store) SessionByHash(ctx context.Context, idHash string) (*Session, error) {
	var sess Session
	var exp int64
	err := s.db.QueryRowContext(ctx,
		`SELECT s.user_id, u.username, u.role, s.csrf_token, s.expires_at
		 FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, idHash).
		Scan(&sess.UserID, &sess.Username, &sess.Role, &sess.CSRFToken, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.ExpiresAt = time.Unix(exp, 0)
	return &sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, username string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}

// ---- instances ----

type Instance struct {
	ID                string
	Name              string
	URL               string
	TokenEnc          []byte
	CAPEM             string
	CertFingerprint   string
	AllowInsecureHTTP bool
	Enabled           bool
	AIEnabled         bool
	MaintWindow       string // "HH:MM-HH:MM" in server local time, "" = any time
	// PublishStatus sends hafleet's view of the instance to it as a hafleet_status event.
	PublishStatus bool
	// Settings is JSON (tasks.Settings) overriding the fleet-wide check settings; "" inherits everything.
	Settings  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

const instanceCols = `id, name, url, token_enc, ca_pem, cert_fingerprint, allow_insecure_http, enabled, ai_enabled, maint_window, publish_status, settings, created_at, updated_at`

func scanInstance(row interface{ Scan(...any) error }) (*Instance, error) {
	var in Instance
	var created, updated int64
	err := row.Scan(&in.ID, &in.Name, &in.URL, &in.TokenEnc, &in.CAPEM, &in.CertFingerprint,
		&in.AllowInsecureHTTP, &in.Enabled, &in.AIEnabled, &in.MaintWindow, &in.PublishStatus, &in.Settings, &created, &updated)
	if err != nil {
		return nil, err
	}
	in.CreatedAt = time.Unix(created, 0)
	in.UpdatedAt = time.Unix(updated, 0)
	return &in, nil
}

func (s *Store) ListInstances(ctx context.Context) ([]*Instance, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+instanceCols+` FROM instances ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Instance
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) GetInstance(ctx context.Context, id string) (*Instance, error) {
	in, err := scanInstance(s.db.QueryRowContext(ctx, `SELECT `+instanceCols+` FROM instances WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return in, err
}

func (s *Store) CreateInstance(ctx context.Context, in *Instance) error {
	now := time.Now()
	in.CreatedAt, in.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO instances (`+instanceCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, in.URL, in.TokenEnc, in.CAPEM, in.CertFingerprint, in.AllowInsecureHTTP, in.Enabled,
		in.AIEnabled, in.MaintWindow, in.PublishStatus, in.Settings, now.Unix(), now.Unix())
	return err
}

func (s *Store) UpdateInstance(ctx context.Context, in *Instance) error {
	in.UpdatedAt = time.Now()
	res, err := s.db.ExecContext(ctx, `UPDATE instances SET name = ?, url = ?, token_enc = ?, ca_pem = ?,
		cert_fingerprint = ?, allow_insecure_http = ?, enabled = ?, ai_enabled = ?, maint_window = ?, publish_status = ?, settings = ?, updated_at = ? WHERE id = ?`,
		in.Name, in.URL, in.TokenEnc, in.CAPEM, in.CertFingerprint, in.AllowInsecureHTTP, in.Enabled,
		in.AIEnabled, in.MaintWindow, in.PublishStatus, in.Settings, in.UpdatedAt.Unix(), in.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteInstance(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- settings ----

// GetSetting returns the value stored under key, or "" if there is none.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) PutSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---- audit ----

type AuditEntry struct {
	ID           int64     `json:"id"`
	Time         time.Time `json:"time"`
	Username     string    `json:"username"`
	InstanceID   string    `json:"instance_id"`
	InstanceName string    `json:"instance_name"`
	Action       string    `json:"action"`
	Target       string    `json:"target"`
	Result       string    `json:"result"`
	Message      string    `json:"message,omitempty"`
}

func (s *Store) AddAudit(ctx context.Context, e *AuditEntry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO audit (ts, username, instance_id, instance_name, action, target, result, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.UnixMilli(), e.Username, e.InstanceID, e.InstanceName, e.Action, e.Target, e.Result, e.Message)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]*AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, username, instance_id, instance_name, action, target, result, message
		FROM audit ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Username, &e.InstanceID, &e.InstanceName, &e.Action, &e.Target, &e.Result, &e.Message); err != nil {
			return nil, err
		}
		e.Time = time.UnixMilli(ts)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ---- AI result cache ----

type AICacheEntry struct {
	Kind      string
	Result    string // JSON
	Provider  string
	Model     string
	CreatedAt time.Time
}

func (s *Store) GetAICache(ctx context.Context, key string) (*AICacheEntry, error) {
	var e AICacheEntry
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT kind, result, provider, model, created_at FROM ai_cache WHERE key = ?`, key).
		Scan(&e.Kind, &e.Result, &e.Provider, &e.Model, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.CreatedAt = time.Unix(created, 0)
	return &e, nil
}

func (s *Store) PutAICache(ctx context.Context, key string, e *AICacheEntry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_cache (key, kind, result, provider, model, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET kind = excluded.kind, result = excluded.result,
			provider = excluded.provider, model = excluded.model, created_at = excluded.created_at`,
		key, e.Kind, e.Result, e.Provider, e.Model, time.Now().Unix())
	return err
}

// PruneAICache deletes cache entries older than maxAge.
func (s *Store) PruneAICache(ctx context.Context, maxAge time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ai_cache WHERE created_at < ?`, time.Now().Add(-maxAge).Unix())
	return err
}

// ---- acknowledgements ----

// Ack hides a task until it changes (fingerprint), resolves, or expires.
type Ack struct {
	InstanceID  string
	TaskID      string
	Fingerprint string
	Username    string
	Note        string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
}

func (s *Store) ListAcks(ctx context.Context) ([]*Ack, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id, task_id, fingerprint, username, note, created_at, expires_at FROM acks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Ack
	for rows.Next() {
		var a Ack
		var created int64
		var expires sql.NullInt64
		if err := rows.Scan(&a.InstanceID, &a.TaskID, &a.Fingerprint, &a.Username, &a.Note, &created, &expires); err != nil {
			return nil, err
		}
		a.CreatedAt = time.Unix(created, 0)
		if expires.Valid {
			t := time.Unix(expires.Int64, 0)
			a.ExpiresAt = &t
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func (s *Store) PutAck(ctx context.Context, a *Ack) error {
	var expires any
	if a.ExpiresAt != nil {
		expires = a.ExpiresAt.Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO acks (instance_id, task_id, fingerprint, username, note, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id, task_id) DO UPDATE SET fingerprint = excluded.fingerprint, username = excluded.username,
			note = excluded.note, created_at = excluded.created_at, expires_at = excluded.expires_at`,
		a.InstanceID, a.TaskID, a.Fingerprint, a.Username, a.Note, a.CreatedAt.Unix(), expires)
	return err
}

func (s *Store) DeleteAck(ctx context.Context, instanceID, taskID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM acks WHERE instance_id = ? AND task_id = ?`, instanceID, taskID)
	return err
}
