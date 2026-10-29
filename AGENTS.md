# AGENTS.md

hafleet is a single Go binary with an embedded React UI that manages several Home Assistant instances. The README has user-facing docs, all flags and the package layout; this file covers what you need to change the code safely.

## Commands

```sh
make test      # go vet, go test -race, tsc, vitest — run before calling a change done
make build     # web UI first, then bin/hafleet
make dev       # backend :8080 + Vite :5173 (creates data/dev.key on first run)
make fakeha    # fake Home Assistant with demo data, prints URL + token
```

- `web/embed.go` embeds `web/dist` via `go:embed`. Go builds and tests work without a UI build (`dist/.gitkeep` is committed), but `bin/hafleet` only serves a UI after `make web`.
- Single Go test: `go test -race ./internal/ai -run TestExplain`. Single web test: `cd web && npx vitest run src/components/TaskTable.test.tsx`.
- No CGO: SQLite is `modernc.org/sqlite`. Keep `CGO_ENABLED=0` builds working.

## Data flow

`hass` (WebSocket client) → `fleet` (one worker per instance, live snapshots) → `tasks` (normalize HA data into `Task`s) → `api` (REST + SSE) → `web`. `actions` executes allowlisted operations; `ai` reads fleet views and never talks to Home Assistant.

## Security rules (do not weaken)

- **Home Assistant tokens** are only decrypted inside the backend (`internal/secrets`, AES-GCM bound to the instance ID). Never return them from the API, log them, or pass them to `ai`.
- **Actions are allowlisted.** A task lists its allowed actions (`tasks.Action*`); `actions.Runner` and the chat `propose_action` tool both check `slices.Contains(t.Actions, ...)`. Never add a generic service-call passthrough. A new action needs a `tasks.Action*` constant, logic in `actions`, the enum in `internal/ai/chat.go`, and a label in `web/src/labels.ts`.
- **Audit:** every action is written to the audit log when submitted and when it finishes.
- **API routes** go through `s.Auth.Require` (session, Origin check, CSRF header on non-GET). Only login is unauthenticated.

## AI code (`internal/ai`)

- Everything sent to a model goes through `redact.go`: build inputs from `SafeInstanceView` / `safeTask`, and pass free text through `Redact(s, instanceSecrets(v)...)`. Add new task `Source` fields to `safeSourceKeys` only if they cannot contain private data. Cover redaction changes in `redact_test.go`.
- Check `s.view(instanceID)` first: it enforces the per-instance AI opt-in.
- Call `s.allow(user)` inside the generator (rate limit), wrap generation in `s.cached(...)`, and call `s.logUsage`.
- Cache keys include `promptVersion`, provider and model. Bump `promptVersion` when you change a prompt or schema.
- Model output is untrusted: drop URLs that fail `isPublicDocURL`, and never act on output without user confirmation. System prompts must state that Home Assistant / release-note text is untrusted data.
- Ollama inputs over `num_ctx` are rejected, never truncated.
- Tests use the `ai.Fake` provider; inspect `Fake.Requests()` to assert nothing private was sent.
- `s *Service` may be nil (AI disabled); handlers must handle that.

## Conventions

- Go: standard library first (`net/http` mux with method patterns, `log/slog`). Errors are wrapped with `fmt.Errorf("...: %w", err)`. API responses use `writeJSON` / `writeError`.
- Schema changes: append a new entry to `migrations` in `internal/store/store.go`. Never edit an existing migration.
- Tests run against `hass/hasstest` (fake HA server) and `store.OpenMemory()`, not a real Home Assistant.
- Web: React 19 + Mantine + TanStack Query. API calls go through `web/src/api.ts`, which sends the CSRF header. Live updates come from SSE in `web/src/events.ts`.
- Keep the README in sync when you add flags, task kinds, actions, or change what is sent to a model.
