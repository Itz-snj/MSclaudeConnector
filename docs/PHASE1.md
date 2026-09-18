# Phase 1 — Host Core Daemon

**Status:** Complete  
**Scope:** Windows-host daemon (cross-compiling from macOS dev), Claude Code adapter skeleton, SQLite event log, WSS sync hub, pairing/auth, mDNS/QR discovery, placeholder web UI.

---

## What was built

### CLI + config (`cmd/harness`, `internal/config`, `internal/logger`)
- `harness serve` — starts the daemon (agent: `claude` or `mock`).
- `harness pair` — generate / approve / revoke / list device credentials.
- `harness status` — shows sessions and paired devices.
- JSON config + structured `log/slog` logging.

### Process supervisor (`internal/supervisor`)
- Build-tagged implementation:
  - `supervisor_windows.go` — Windows Job Objects with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` so the agent tree dies with the daemon.
  - `supervisor_other.go` — process-group fallback for macOS/Linux development.
- Start / stop / tree-reap API used by the agent adapters.

### Agent abstraction (`internal/agent`)
- `AgentAdapter` interface: `Start`, `Events`, `Command`, `Stop`.
- Normalized events/commands in `internal/protocol`.
- `mock` adapter for deterministic hub/store tests.
- `claude` adapter that drives `claude --print --output-format stream-json --input-format stream-json` over stdio.

### Event log & device registry (`internal/store`)
- `modernc.org/sqlite` (pure Go, no CGO), WAL mode + busy timeout.
- Tables: `sessions`, `events` (host-assigned `seq`), `devices`, `pending_tokens`.
- Atomic seq assignment via `UPDATE ... RETURNING last_seq` in a transaction.
- Snapshot rebuild from the event log.

### Sync hub (`internal/hub`)
- `coder/websocket` WSS server mounted at `/ws`.
- Auth:
  - One-time pairing token (store-backed, 5 min TTL) → per-device bearer credential.
  - Device credentials stored as SHA-256 hashes; revocation via CLI.
- Protocol:
  - `HELLO {deviceId, lastSeq}` → `SNAPSHOT` + catch-up `event`s → live tail.
  - `command {id, idempotencyKey, kind, payload}` → `ack` / `error`.
- First-write-wins permission resolution with `byDevice` attribution.
- All client writes serialized through a per-client write loop.

### Discovery & pairing UX (`internal/auth`, `internal/certutil`, `internal/discovery`, `internal/netutil`, `internal/webui`)
- Self-signed ECDSA TLS cert generated on first run; SHA-256 fingerprint for pinning.
- Terminal QR code + JSON blob containing addresses, port, token, fingerprint.
- mDNS `_harness._tcp` broadcaster (`grandcat/zeroconf`).
- Placeholder web UI served at `/` via `embed.FS`.

### Tests
- `internal/store` — sessions, events, snapshots, devices, pending tokens.
- `internal/supervisor` — start/stop process reaping.
- `internal/hub` — pair/prompt/permission/resume, first-write-wins, mid-turn resume.
- `internal/agent/claude` — fake-claude stdio test; real POC-1 gate skipped unless `ANTHROPIC_API_KEY` is set.

---

## Verification

```bash
GOTOOLCHAIN=local go test ./... -timeout 180s -count=1
GOTOOLCHAIN=local go build ./...
GOOS=windows GOARCH=amd64 GOTOOLCHAIN=local go build ./...
```

All green.

---

## How to run

```bash
# Build
go build -o harness ./cmd/harness

# Mock agent (no Claude needed)
./harness serve --agent mock --dir /path/to/project --bind 127.0.0.1

# Real Claude (requires ANTHROPIC_API_KEY)
export ANTHROPIC_API_KEY=...
./harness serve --agent claude --dir /path/to/project

# Pairing
./harness pair --generate
./harness pair --approve <token>:phone
```

Clients connect to `wss://<host>:<port>/ws` with the pinned certificate and present the one-time token.

---

## Known limitations / next work

- The **real Claude POC-1 gate** (`TestPOC1RealClaude`) is automated but currently skipped because no `ANTHROPIC_API_KEY` was available in the build environment. Once the key is set, run:
  ```bash
  ANTHROPIC_API_KEY=... go test ./internal/agent/claude -run TestPOC1RealClaude -v
  ```
  The exact stream-json schema in `internal/agent/claude/adapter.go` will be tightened based on the result.
- Web UI is a placeholder; Phase 2 builds the React web UI and Expo Android client.
- Resume after daemon restart is not yet implemented (new session per daemon start).
- Tailscale-specific binding guidance is documented but not enforced beyond `--bind`.
