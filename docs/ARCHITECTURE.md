# ARCHITECTURE.md — Multi-Device Coding-Agent Harness

**Last updated:** 2026-09-16
Companion docs: [../PLAN.md](../PLAN.md) · [SYSTEM-DESIGN.md](SYSTEM-DESIGN.md) (diagrams)

---

## 1. Design Principles

1. **Single writer, single source of truth.** PC A owns the agent process and the authoritative event log. Clients are views + input devices.
2. **Local-first, minimum infrastructure.** Everything that can live on the host or client does. No cloud backend in MVP.
3. **The agent outlives every client.** Agents are children of the daemon, never of a UI.
4. **Structured over scraped.** Drive CLIs via their machine-readable protocols (stream-json), not terminal scraping.
5. **The cloud, when it ever appears, is a dumb pipe.** Any future relay is stateless and end-to-end encrypted — content never leaves your devices readable.

---

## 2. Components

### 2.1 Host Daemon (Go, single binary, runs on PC A)

| Sub-component | Responsibility |
|---|---|
| **Process Supervisor** | Spawns `claude` (later `codex`) as child processes with stdio pipes; Windows Job Objects guarantee the agent tree dies with the daemon; interrupt / stop / restart-with-resume lifecycle |
| **AgentAdapter** | Per-agent implementation behind one Go interface; normalizes vendor event streams; accepts normalized commands |
| **Session Store** | Append-only, seq-numbered event log per session (JSONL files or embedded SQLite via `modernc.org/sqlite` — pure Go, no CGO) |
| **Sync Hub** | WSS server: pairing/auth, snapshot + live-tail fan-out, command intake with acks, first-write-wins resolution |
| **Discovery** | mDNS broadcaster (`_harness._tcp.local`); QR/manual-IP fallback |
| **Web UI server** | Serves the embedded React app (`embed.FS`) — this is the PC B client and PC A's local UI |

### 2.2 Clients

- **Phone app** — React Native (Expo dev-client), Android first. Renders the event stream, sends prompts, answers permission requests/questions. Reconnect + resume by seq.
- **Web UI** — React app served by the daemon itself. Used by PC B (any browser) and by PC A's local user.
- **Shared TS package** — protocol types + WS client with reconnect/resume, consumed by both clients. One protocol implementation, two UIs.

### 2.3 Protocol

One versioned JSON schema over WSS. Two directions of meaning:

- `event` (host → clients): seq-numbered, append-only stream
- `command` (client → host): acked, idempotency-keyed

---

## 3. State Ownership

**The authoritative state lives only on PC A, as an append-only event log.**

```
Session { id, agentType, workingDir, status, lastSeq }
Event   { seq, ts, kind, payload }        // seq assigned by host — total order
```

- Everything is an event: user prompts, text deltas, tool call start/end, permission requests, questions, resolutions, status changes, usage.
- **Materialized state** (pending permission, running/idle, recent context) is *derived* — rebuilt from the log on daemon restart.
- Clients connect → receive **snapshot + events since their last seq** → then live tail.
- Because there is exactly one writer with a total order, **no CRDTs, no vector clocks, no conflict resolution**. This is the biggest complexity-saving decision in the system.
- Client disconnects are irrelevant to the agent; the daemon owns the process.

---

## 4. Synchronization Model

- **Connect/resume:** `HELLO {deviceId, lastSeq}` → `SNAPSHOT` + events `lastSeq+1..N` → live tail.
- **Commands:** validated → host assigns seq → appended → broadcast. Every client sees the same order. Commands carry idempotency keys so reconnect retries can't double-fire; each gets an `ack`/`error` correlated by client-supplied `id`.
- **First-write-wins** for pending actions: `permission_request {requestId}` is resolved by the first `answer_permission` the host receives; the host broadcasts `permission_resolved {requestId, byDevice}`; late answers are ignored; all UIs grey out with attribution. Same pattern for questions.
- **Disconnected clients are read-only** in MVP. No offline command queue (post-MVP).

---

## 5. Networking

### LAN
1. Host broadcasts mDNS; clients discover, or scan the pairing QR (contains IPs, port, one-time token, cert fingerprint).
2. Direct WSS with pinned self-signed cert. Low latency, zero infrastructure.

### Remote (different networks)
**Tailscale mesh (MVP decision).** Phone and PC A join a free tailnet; the phone reaches PC A's daemon exactly as if on LAN. Encrypted (WireGuard), NAT-traversing, nothing for us to build or operate. The pairing QR carries the tailnet IP.

### Cloud necessity audit

| Need | Cloud? |
|---|---|
| LAN connectivity | No |
| Remote connectivity | No — Tailscale (user-installed infra, not ours) |
| Discovery | No — mDNS + QR |
| Authentication | No — offline pairing keys |
| NAT traversal without Tailscale | Post-MVP: one stateless E2E-encrypted relay |
| Source/transcripts in cloud | **Never** |
| Push notifications | Unavoidable (FCM/APNs) — deferred to post-MVP |

**Future relay design (documented, not built):** a ~200-line Go binary on a free-tier VPS. Host dials out persistently; clients dial in; relay forwards opaque frames between them by room ID (derived from host public key). Payloads are E2E-encrypted with keys established at pairing — the relay sees nothing. No DB, no accounts, no queues.

---

## 6. Agent Integration — Hybrid, Structured-First

| Approach | Verdict |
|---|---|
| **Structured headless protocol** — Claude: `claude -p --output-format stream-json --input-format stream-json`; Codex: `codex exec --json` / `app-server` JSON-RPC | **Primary.** Typed events, programmatic permission/question round-trips, resume support. The only approach that makes multi-client sync sane. |
| **PTY scraping** (ConPTY + terminal emulation) | **Fallback only.** Fragile across CLI updates; permission prompts hard to resolve reliably from N clients. Not in MVP. |
| Vendor cloud APIs | **Out of scope.** The product drives the user's existing CLI sessions on their machine. |

### Abstraction

```go
type AgentAdapter interface {
    Start(cfg SessionConfig) error
    Events() <-chan AgentEvent          // normalized stream
    Command(cmd AgentCommand) error     // SendPrompt | AnswerPermission |
                                        // AnswerQuestion | Interrupt | SetMode
    Stop() error
}
```

Normalized event kinds: `TextDelta, ToolCallStart, ToolCallEnd, PermissionRequest, Question, TurnComplete, StatusChange, UsageUpdate, Error`.

The store, hub, and clients only ever see normalized events. Vendor specifics stop at the adapter. Adding an agent = one adapter.

### Windows specifics

- Daemon runs as a normal user process (console app for MVP; tray/service post-MVP — services complicate user-session process interaction, so deferred).
- Working directory = the user's project; environment (incl. API keys) passes through on PC A — **keys never leave the host**, a security feature.
- While the daemon owns a session, local interaction on PC A goes through the daemon's web UI (open question #1 in PLAN.md).

---

## 7. Pairing & Authentication

1. Host first run → host identity keypair + **one-time pairing token** (short TTL) shown as QR with LAN IP, tailnet IP, port, and cert fingerprint.
2. Client scans → WSS connect → presents one-time token → **PC A screen physically confirms** the device name → host issues a **long-lived device credential** (bearer token; host cert pinned).
3. Future connects: pinned TLS + device bearer. Per-device revocation from the host.
4. Same flow over the tailnet; over the future relay, E2E keys derive from the pairing exchange — relay is untrusted.
5. Default posture: all mutation commands require a paired device; no anonymous access; bind to LAN/tailnet interfaces only.

---

## 8. Security Model

| Threat | Control |
|---|---|
| Token theft → remote shell via agent | One-time short-TTL pairing tokens; per-device revocable credentials; physical confirmation on PC A; no tokens in logs/QR after expiry |
| Prompt injection through agent output | Clients render agent text as untrusted: no raw HTML, no auto-opened links |
| Hostile LAN exposure | Per-interface binding; auth regardless; warn on public subnets |
| TLS warning fatigue | Cert pinning via QR fingerprint, not warnings |
| Secret leakage (keys, code in transcripts) | All data local; future relay E2E-encrypted and content-blind; no content analytics |
| Approval fatigue | Clear command summaries; per-session `allow_always`; per-device roles post-MVP |
| Supply chain | Lockfiles, Dependabot, signed builds |

---

## 9. Alternatives Considered

| # | Alternative | Tradeoff | Verdict |
|---|---|---|---|
| A | **LAN-only** (no Tailscale, no relay) | Simplest possible; but fails the core "walk away, use phone on cellular" story | Rejected as the *whole* answer; kept as the default local path |
| B | **Custom relay from day one** | No Tailscale install for users; but adds a cloud dependency, ops burden, and ~1–2 weeks to MVP | Deferred post-MVP; design documented (§5) |
| C | **WebRTC P2P** | "No server" purism; but RN WebRTC is painful, TURN *is* a relay anyway, and DataChannels buy nothing over WS for JSON events | Rejected |
| D | **PTY-scraping as primary integration** | Full TUI fidelity; but brittle across CLI releases and near-impossible to make permission prompts reliably multi-client | Rejected as primary; retained as fallback |
| E | **React Native Windows for PC B** | One RN codebase; but heavy C++ toolchain, slow iteration, marginal benefit over a browser tab | Rejected; host-served web UI instead |
| F | **Tailscale-only (chosen)** | Users install Tailscale once; we build and operate zero infrastructure; encrypted mesh | **Chosen for MVP** |

---

## 10. Key Technical Uncertainties → POCs

See PLAN.md §4 Phase 0 for methods and pass criteria.

1. **POC-1 (kill-gate):** stream-json session viability (permissions, interrupt, resume) over stdio
2. **POC-2:** RN/Expo WS behavior under OS backgrounding
3. **POC-3:** mDNS reliability on real Windows/Android networks
4. **POC-4:** Windows Job Object process-tree reaping

POC-1 and POC-2 are the true gates; everything else is engineering.
