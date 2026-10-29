// Package hass is a minimal Home Assistant WebSocket API client.
//
// Protocol reference: https://developers.home-assistant.io/docs/api/websocket
package hass

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Options describes how to reach and authenticate with an HA instance.
type Options struct {
	URL               string // base URL, e.g. https://ha.example.com:8123
	Token             string // long-lived access token
	CAPEM             string // optional PEM bundle used instead of system roots
	CertFingerprint   string // optional SHA-256 fingerprint (hex, colons allowed) of the leaf cert
	AllowInsecureHTTP bool   // permit http:// / ws:// URLs
}

// ErrAuth is returned when HA rejects the access token.
var ErrAuth = errors.New("authentication failed")

// Error is an error returned by an HA command.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("home assistant: %s: %s", e.Code, e.Message) }

type message struct {
	ID      int64           `json:"id"`
	Type    string          `json:"type"`
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   *Error          `json:"error"`
	Event   json.RawMessage `json:"event"`
	// auth phase
	HAVersion string `json:"ha_version"`
	Message   string `json:"message"`
}

type Client struct {
	conn      *websocket.Conn
	HAVersion string
	// CertNotAfter is when the server's leaf certificate expires (zero over plain HTTP).
	CertNotAfter time.Time

	nextID  atomic.Int64
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[int64]chan *message
	subs    map[int64]func(json.RawMessage)

	done   chan struct{}
	errMu  sync.Mutex
	err    error
	cancel context.CancelFunc
}

// WebSocketURL converts an HA base URL into its websocket endpoint.
func WebSocketURL(base string, allowInsecure bool) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		if !allowInsecure {
			return "", errors.New("plain http is not allowed for this instance (enable 'allow insecure http' to override)")
		}
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported url scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("url has no host")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/websocket"
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// TLSConfig builds the TLS configuration for an instance. If a fingerprint is
// set it is authoritative (chain verification is replaced by pinning, which
// supports self-signed certs); otherwise a custom CA bundle replaces system roots.
func TLSConfig(o Options) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if fp := normalizeFingerprint(o.CertFingerprint); fp != "" {
		want, err := hex.DecodeString(fp)
		if err != nil || len(want) != sha256.Size {
			return nil, errors.New("certificate fingerprint must be a SHA-256 hex digest")
		}
		cfg.InsecureSkipVerify = true // replaced by the pin check below
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no peer certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if hex.EncodeToString(got[:]) != fp {
				return fmt.Errorf("certificate fingerprint mismatch: got %s", hex.EncodeToString(got[:]))
			}
			return nil
		}
		return cfg, nil
	}
	if strings.TrimSpace(o.CAPEM) != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(o.CAPEM)) {
			return nil, errors.New("no valid certificates in CA PEM")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

func normalizeFingerprint(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, ":", "")
	s = strings.TrimPrefix(s, "sha256/")
	return s
}

// Dial connects and authenticates. The returned client reads in the
// background until Close is called or the connection fails.
func Dial(ctx context.Context, o Options) (*Client, error) {
	wsURL, err := WebSocketURL(o.URL, o.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	tlsCfg, err := TLSConfig(o)
	if err != nil {
		return nil, err
	}
	// Record the leaf certificate's expiry. This runs after the usual checks
	// (or instead of them for a pinned certificate, which may be expired).
	var certNotAfter time.Time
	verify := tlsCfg.VerifyConnection
	tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if verify != nil {
			if err := verify(cs); err != nil {
				return err
			}
		}
		if len(cs.PeerCertificates) > 0 {
			certNotAfter = cs.PeerCertificates[0].NotAfter
		}
		return nil
	}
	httpClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig:     tlsCfg,
		Proxy:               http.ProxyFromEnvironment,
		TLSHandshakeTimeout: 10 * time.Second,
	}}
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPClient: httpClient})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	conn.SetReadLimit(64 << 20) // get_states can be large

	ver, err := authenticate(dialCtx, conn, o.Token)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "auth failed")
		return nil, err
	}

	rctx, rcancel := context.WithCancel(context.Background())
	c := &Client{
		conn:         conn,
		HAVersion:    ver,
		CertNotAfter: certNotAfter,
		pending:      map[int64]chan *message{},
		subs:         map[int64]func(json.RawMessage){},
		done:         make(chan struct{}),
		cancel:       rcancel,
	}
	go c.readLoop(rctx)
	return c, nil
}

func authenticate(ctx context.Context, conn *websocket.Conn, token string) (string, error) {
	var m message
	if err := readJSON(ctx, conn, &m); err != nil {
		return "", fmt.Errorf("auth handshake: %w", err)
	}
	if m.Type != "auth_required" {
		return "", fmt.Errorf("unexpected message %q during handshake", m.Type)
	}
	if err := writeJSON(ctx, conn, map[string]string{"type": "auth", "access_token": token}); err != nil {
		return "", err
	}
	if err := readJSON(ctx, conn, &m); err != nil {
		return "", fmt.Errorf("auth handshake: %w", err)
	}
	switch m.Type {
	case "auth_ok":
		return m.HAVersion, nil
	case "auth_invalid":
		return "", fmt.Errorf("%w: %s", ErrAuth, m.Message)
	default:
		return "", fmt.Errorf("unexpected message %q during handshake", m.Type)
	}
}

func readJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func (c *Client) readLoop(ctx context.Context) {
	var err error
	defer func() { c.shutdown(err) }()
	for {
		var data []byte
		_, data, err = c.conn.Read(ctx)
		if err != nil {
			return
		}
		// HA may coalesce multiple messages into a JSON array.
		var batch []*message
		if len(data) > 0 && data[0] == '[' {
			if err = json.Unmarshal(data, &batch); err != nil {
				return
			}
		} else {
			var m message
			if err = json.Unmarshal(data, &m); err != nil {
				return
			}
			batch = []*message{&m}
		}
		for _, m := range batch {
			c.dispatch(m)
		}
	}
}

func (c *Client) dispatch(m *message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch m.Type {
	case "event":
		if h := c.subs[m.ID]; h != nil {
			h(m.Event)
		}
	case "result", "pong":
		if ch, ok := c.pending[m.ID]; ok {
			delete(c.pending, m.ID)
			ch <- m
		}
	}
}

func (c *Client) shutdown(err error) {
	c.errMu.Lock()
	if c.err == nil {
		if err == nil {
			err = errors.New("connection closed")
		}
		c.err = err
	}
	c.errMu.Unlock()
	c.mu.Lock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
	c.mu.Unlock()
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

// Done is closed when the connection terminates.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns the reason the connection terminated.
func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

func (c *Client) Close() {
	c.cancel()
	c.conn.Close(websocket.StatusNormalClosure, "")
	<-c.done
}

// send registers a pending response (and optional event handler) and writes cmd with a fresh id.
func (c *Client) send(ctx context.Context, cmd map[string]any, handler func(json.RawMessage)) (int64, chan *message, error) {
	id := c.nextID.Add(1)
	ch := make(chan *message, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return 0, nil, c.Err()
	default:
	}
	c.pending[id] = ch
	if handler != nil {
		c.subs[id] = handler
	}
	c.mu.Unlock()

	msg := make(map[string]any, len(cmd)+1)
	for k, v := range cmd {
		msg[k] = v
	}
	msg["id"] = id
	c.writeMu.Lock()
	err := writeJSON(ctx, c.conn, msg)
	c.writeMu.Unlock()
	if err != nil {
		c.forget(id)
		return 0, nil, err
	}
	return id, ch, nil
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	delete(c.subs, id)
	c.mu.Unlock()
}

func (c *Client) await(ctx context.Context, id int64, ch chan *message) (*message, error) {
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, c.Err()
		}
		if m.Type == "result" && !m.Success {
			if m.Error != nil {
				return nil, m.Error
			}
			return nil, errors.New("home assistant: command failed")
		}
		return m, nil
	case <-ctx.Done():
		c.forget(id)
		return nil, ctx.Err()
	}
}

// Call sends a command (e.g. {"type": "get_states"}) and decodes the result into out (may be nil).
func (c *Client) Call(ctx context.Context, cmd map[string]any, out any) error {
	id, ch, err := c.send(ctx, cmd, nil)
	if err != nil {
		return err
	}
	m, err := c.await(ctx, id, ch)
	if err != nil {
		return err
	}
	if out != nil && len(m.Result) > 0 {
		return json.Unmarshal(m.Result, out)
	}
	return nil
}

// Ping round-trips a ping message.
func (c *Client) Ping(ctx context.Context) error {
	id, ch, err := c.send(ctx, map[string]any{"type": "ping"}, nil)
	if err != nil {
		return err
	}
	_, err = c.await(ctx, id, ch)
	return err
}

// Subscribe sends a subscription command and invokes handler for each event.
// handler runs on the read goroutine and must not block or call back into the client.
func (c *Client) Subscribe(ctx context.Context, cmd map[string]any, handler func(json.RawMessage)) error {
	id, ch, err := c.send(ctx, cmd, handler)
	if err != nil {
		return err
	}
	if _, err := c.await(ctx, id, ch); err != nil {
		c.forget(id)
		return err
	}
	return nil
}
