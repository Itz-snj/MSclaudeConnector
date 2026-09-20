# Phase 1 — Host Core Daemon

**Status:** Complete  
**Scope:** Windows-host daemon (cross-compiling from macOS dev), Claude Code adapter skeleton, SQLite event log, WSS sync hub, pairing/auth, mDNS/QR discovery, placeholder web UI.

---

## Current architecture condition

The daemon is a complete Phase 1 implementation of the architecture in `ARCHITECTURE.md` / `SYSTEM-DESIGN.md`. Single writer, single source of truth: the SQLite event log on the host. Clients are views + input devices over WSS.

**Solid / implemented**
- Host-assigned total order via `seq` (no CRDTs, no conflict resolution)
- Agent outlives every client (child of the daemon, never of a UI)
- Pairing + per-device credentials + revocation
- Snapshot + catch-up tail resume (`HELLO {lastSeq}`)
- First-write-wins resolution (permissions and questions) with `byDevice` attribution
- All five commands wired end to end: `send_prompt`, `answer_permission`, `answer_question`, `interrupt`, `set_mode`
- mDNS + QR discovery, self-signed TLS + cert pinning
- Windows cross-compile (Job Objects) + macOS/Linux dev fallback

**Skeleton / deferred**
- Real Claude stream-json schema (POC-1 pending `ANTHROPIC_API_KEY`)
- Daemon restart resume (new session per daemon start)
- React web UI + Expo app (Phase 2)
- `SnapshotPayload.RecentEvents` is defined but not yet populated
- Per-device roles (viewer vs approver)
- The `claude` adapter has no vendor mapping for `answer_question` yet — Claude Code's stream-json protocol has no distinct mid-turn "question" message today (questions currently surface as permission requests); `Command()` returns an explicit error for it. `set_mode` maps to a `control_request{subtype:"set_permission_mode"}` guess, unverified until POC-1 exercises it live.

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
- `claude` adapter that drives `claude -p --output-format stream-json --input-format stream-json` (plus `--resume <id>` when resuming) over stdio.

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
  - All five command kinds are wired: `send_prompt`, `answer_permission`, `answer_question`, `interrupt`, `set_mode`.
- First-write-wins resolution for both permissions and questions, with `byDevice` attribution; `SnapshotPayload` carries `pendingPermissions`, `pendingQuestions`, and the current `mode`.
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
- The hub wires all five commands (`send_prompt`, `answer_permission`, `answer_question`, `interrupt`, `set_mode`), but the `claude` adapter itself only forwards `send_prompt`, `answer_permission`, `interrupt`, and `set_mode` (best-effort `control_request{subtype:"set_permission_mode"}`, unverified) to the real CLI — `answer_question` returns an explicit "no vendor equivalent" error until Claude Code exposes a real mid-turn question mechanism in stream-json.
- Tailscale-specific binding guidance is documented but not enforced beyond `--bind`.

### Available CLI flags

```
harness serve --agent claude|mock --dir <workdir> --bind lan|tailnet|<ip> --port <n> --data <datadir>
harness pair  --generate | --approve <token>:<name> | --revoke <deviceId> | --list
harness status
```

