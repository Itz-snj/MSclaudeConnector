# PLAN.md — Multi-Device Coding-Agent Harness

**Status:** Planning complete, pre-implementation
**Last updated:** 2026-09-16

Companion docs: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) · [docs/SYSTEM-DESIGN.md](docs/SYSTEM-DESIGN.md)

---

## 1. Problem Statement

A coding agent (Claude Code, later OpenAI Codex) runs on **PC A** (Windows). The user walks away and wants to continue interacting with that **same live agent session** from **PC B** (another Windows machine) or a **phone**.

### Requirements

- PC A remains fully usable while remote clients are connected.
- PC B and phone are **clients**, not separate agent instances.
- Remote clients see agent activity, questions, permission requests, responses — and send commands/prompts.
- Changes made from any client appear on all others.
- The agent keeps running when a UI client disconnects.
- Claude and Codex supported via an **agent-agnostic abstraction**.
- **Local-first, minimum infrastructure**: prefer host/client-side functionality; no cloud DBs, microservices, queues, Kubernetes.
- Free-tier cloud only where genuinely required (connectivity / NAT traversal) — and only post-MVP.
- **Never** store source code, terminal history, or agent conversations in the cloud.

---

## 2. Locked Decisions

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | Remote (non-LAN) access for MVP | **Tailscale-only** | Zero infrastructure to build; encrypted WireGuard mesh; free tier. "Remote mode" = install doc + tailnet IP in pairing QR. |
| 2 | PC B client | **Host-served web UI** | Go daemon embeds a React app via `embed.FS`; any browser works; shares protocol/types with the phone app. No React Native Windows toolchain. |
| 3 | Phone app | **Expo dev-client** | Fastest iteration; WebSockets work out of the box. Eject later only if Android background persistence demands it. |
| 4 | Host language | **Go** | Single static binary, excellent Windows support, goroutines fit the fan-out model, pure-Go SQLite avoids CGO. |
| 5 | Transport | **WebSocket (WSS) everywhere** | Small JSON messages, works on RN + browsers, trivially relayed, simple reconnect/resume via sequence numbers. |
| 6 | State model | **Append-only event log on PC A, single writer** | Total order via host-assigned seq → no CRDTs, no conflict resolution. |
| 7 | Agent integration | **Structured stream-json first; PTY deferred** | Typed events + programmatic permission round-trips make multi-client sync sane. PTY scraping is fragile fallback only. |
| 8 | Cloud backend | **None in MVP** | No servers, no DBs, no accounts. First cloud cost ever: optional stateless E2E relay, post-MVP. |
| 9 | Diagram format | **Mermaid** | Renders natively on GitHub and VS Code; zero extra tooling. |

---

## 3. Tech Stack

| Layer | Choice | Notes |
|---|---|---|
| Host daemon | Go 1.22+ | Single binary, console app for MVP (tray/service post-MVP) |
| WebSocket | `coder/websocket` | Actively maintained, context-first API |
| Session store | JSONL files or `modernc.org/sqlite` | Pure Go → no CGO, trivial Windows cross-compile |
| LAN discovery | `grandcat/zeroconf` | mDNS `_harness._tcp.local`; QR/manual-IP fallback |
| Process supervision | Windows Job Objects (`golang.org/x/sys/windows`) | Daemon crash reaps the whole agent process tree |
| Agent control (Claude) | `claude -p --output-format stream-json --input-format stream-json` over stdio | Resume via `--resume`/`--continue` |
| Agent control (Codex, post-MVP) | `codex exec --json` / `codex app-server` JSON-RPC | Coverage validated by POC before committing |
| Phone app | React Native (Expo dev-client) + TypeScript | Android first; iOS post-MVP |
| PC B client | React web UI embedded in daemon (`embed.FS`) | Shares TS protocol package with phone app |
| Shared client lib | TS package: protocol types + WS client (reconnect/resume) | Used by both RN app and web UI |
| Remote access | Tailscale free tier | User-installed; not our infrastructure |
| CI | GitHub Actions | Build Windows binary, web UI, RN app |

---

## 4. Roadmap

### Phase 0 — POCs (Week 1) — *gates everything*

| POC | Question | Method | Pass criteria |
|---|---|---|---|
| **POC-1** (kill-gate) | Can Claude Code's stream-json headless mode sustain a long-lived interactive session — prompts, permission round-trips, interrupt, resume — driven purely over stdio? | Go harness spawns `claude` with stream-json in/out; run a 30-min scripted session; kill and `--resume`; log every gap | All MVP flows complete programmatically; gaps documented and acceptable |
| POC-2 | RN (Expo) WebSocket behavior under lock/background (Android doze, iOS suspension) | Expo app + local mock WS server; measure disconnect timing, reconnect cost, resume-by-seq feel | Reconnect + snapshot resume feels instant; failure modes understood |
| POC-3 | mDNS reliability between Windows and Android on real networks | Zeroconf broadcast/discovery on 2–3 networks | Works on typical home LANs; QR/manual-IP fallback confirmed acceptable where blocked |
| POC-4 | Do Windows Job Objects reap the agent process tree on daemon crash? | Crash-test harness | No orphaned `claude`/node processes after forced daemon kill |

**Contingency for POC-1 failure:** drive Claude via the Agent SDK (TS) wrapped as a stdio child of the Go daemon. Decide then; do not pre-build.

### Phase 1 — Host Core (Weeks 2–3)

- Go daemon: config, structured logging, process supervisor (Job Objects)
- `AgentAdapter` interface + **ClaudeAdapter** (stream-json over stdio)
- JSONL event log with host-assigned seq; snapshot rebuild on restart
- WS hub: pairing (QR + one-time token → device credential), pinned-cert TLS, `HELLO/SNAPSHOT/tail` resume, acked idempotent commands, first-write-wins permission resolution
- CLI verbs: `harness serve`, `harness pair`, `harness status`

**Done when:** a scripted WS client can pair, send prompts, receive the full normalized event stream, answer a permission request, disconnect mid-turn, and resume without loss.

### Phase 2 — Clients (Weeks 3–4, overlapping late Phase 1)

- Shared TS package: protocol types + WS client (reconnect/resume)
- Expo app (Android): session stream view (text deltas, tool calls), prompt input, permission/question cards (allow / allow-always / deny), connection states, QR pairing
- Web UI: same screens, served by the daemon

**Done when:** phone and PC B browser simultaneously follow one live session; an answer from either greys out on the other with attribution.

### Phase 3 — MVP Hardening (Weeks 4–5)

- Tailscale path end-to-end (pairing QR carries tailnet IP)
- Security pass: per-interface binding, token expiry, no secrets in logs, agent output rendered as untrusted (no raw HTML, no auto-opened links)
- Crash/restart resilience: daemon restart → session resumes → clients resync
- Docs: install, pairing, Tailscale setup

---

## 5. MVP Scope

**In scope:** Windows host daemon · Claude Code only · Expo Android app · host-served web UI (PC B) · LAN direct + Tailscale remote · pairing/auth · live stream + prompts + permission/question answers · reconnect/resume.

**Explicitly out of scope:** Codex adapter (interface exists, implementation second) · custom relay · push notifications · multiple concurrent sessions · PTY mode · offline command queue · Windows service/tray polish · multi-user/teams · iOS.

### MVP Success Test

Start a Claude session on PC A → walk away → from the phone on cellular (Tailscale): watch it work, approve a permission, send a follow-up prompt → open PC B's browser and see the same live state → close the phone app mid-run → **the agent keeps going**.

---

## 6. Risks & Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Product is an RCE bridge by design; leaked pairing token = shell via agent | Critical | One-time short-lived pairing tokens; per-device revocable credentials; PC A physically confirms each pairing; tokens never logged |
| Prompt injection via agent output rendered on clients | High | Treat all agent output as untrusted: no raw HTML, no auto-opening URLs |
| LAN exposure on hostile networks | High | Bind per-interface (LAN/tailnet only, never open 0.0.0.0); auth required regardless; warn on public subnets |
| Self-signed TLS click-through habits | Medium | Cert pinning via QR fingerprint instead of warnings |
| Secrets in transcripts | High | Everything stays local; future relay is E2E-encrypted and content-blind; no content analytics |
| Permission fatigue (rubber-stamping from phone) | Medium | Clear command summaries/diffs; `allow_always` scoped per-session; per-device viewer/approver roles post-MVP |
| Dependency supply chain (RN + Go) | Medium | Lockfiles, Dependabot, signed builds |
| POC-1 fails: stream-json can't sustain interactive sessions | Project-critical | Contingency: Agent SDK (TS) wrapped as stdio child; PTY scraping as last resort |

---

## 7. Post-MVP Backlog (prioritized)

1. **CodexAdapter** — POC `codex exec --json` / `app-server` event coverage first; this validates the abstraction earns its name
2. Android foreground service + local notifications for "permission requested" while backgrounded (Expo eject decision happens here)
3. iOS build
4. Multiple concurrent sessions
5. Optional E2E-encrypted stateless relay (free-tier VPS) for users who refuse Tailscale
6. Push via FCM/APNs — first true cloud dependency; only if notifications prove necessary
7. Per-device roles (viewer vs approver), offline command queue, Windows tray app/service

---

## 8. Open Questions

1. **Concurrent local use** — while the daemon owns a session, may PC A's user also type into Claude's own TUI? Recommended stance: no — all interaction through daemon UIs (local user uses the web UI). If simultaneous TUI attach is required, the PTY question reopens (significant scope change).
2. **iOS timing** — Android-first assumed; confirm iOS can wait until post-MVP.
3. **Permission granularity** — MVP: every paired device can approve everything. Roles deferred. Acceptable?
