# MSclaudeConnector

**A multi-device harness for live coding-agent sessions.** Run Claude Code (later OpenAI Codex) on your Windows PC, walk away, and keep driving that *same live session* from another PC or your phone.

> **Status:** Planning complete — pre-implementation. See [PLAN.md](PLAN.md) for the roadmap and POC gates.

---

## The Problem

A coding agent is running on **PC A**. You leave the desk. You want to open **PC B** or your **phone** and continue interacting with that exact session — watch it work, answer its questions, approve permissions, send the next prompt — without starting a new agent, and without the agent dying when a client disconnects.

## What It Is

- **One live session, many screens.** PC A owns the agent process and the authoritative state. PC B and the phone are clients — views + input devices, never separate agent instances.
- **Local-first.** No cloud backend, no accounts, no servers. LAN direct when you're home; Tailscale (WireGuard mesh) when you're not. Your code and conversations never leave your devices.
- **Agent-agnostic.** A Go `AgentAdapter` interface normalizes Claude Code (and later Codex) into one event stream; adding an agent means one adapter.
- **Crash-proof agents.** The agent is a child of the host daemon, not of any UI — close the app, the agent keeps running.

## How It Works

```mermaid
flowchart LR
    subgraph PCA["PC A — Windows Host (agent runs here)"]
        AGENT["Claude Code process"]
        D["Host Daemon (Go)<br/>supervisor · adapter ·<br/>event log · WSS hub"]
        AGENT <-->|"stdio: stream-json"| D
    end
    PHONE["Phone — React Native (Expo)"]
    PCB["PC B — Browser (web UI served by daemon)"]
    PHONE <-.->|"WSS — LAN direct or Tailscale"| D
    PCB <-.->|"WSS — LAN direct"| D
```

1. A **Go daemon** on PC A spawns Claude Code in structured headless mode (`--output-format stream-json`) and records everything in an append-only, seq-numbered event log — the single source of truth.
2. Clients pair once via QR (pinned TLS + per-device credentials), then receive a **snapshot + live tail** over WebSocket.
3. Commands (prompts, permission answers) go to the daemon, which assigns the order and broadcasts results to every screen — **first-write-wins**, with attribution.
4. Disconnected? The agent keeps going. Reconnect with `HELLO {lastSeq}` and catch up instantly.

## Documentation

| Doc | Contents |
|---|---|
| [PLAN.md](PLAN.md) | Locked decisions, tech stack, Phase 0–3 roadmap, POC gates, MVP scope, risks, backlog |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Components, state ownership, sync protocol, networking, security model, alternatives considered |
| [docs/SYSTEM-DESIGN.md](docs/SYSTEM-DESIGN.md) | 8 Mermaid diagrams: system context, topology, pairing, prompt flow, permissions, resume, data model, adapters |

## Tech Stack

| Layer | Choice |
|---|---|
| Host daemon | Go (single Windows binary) — `coder/websocket`, `modernc.org/sqlite`, `grandcat/zeroconf`, Job Objects |
| Agent control | Claude Code stream-json over stdio (PTY scraping deferred; Codex post-MVP) |
| Phone | React Native (Expo dev-client) + TypeScript — Android first |
| PC B | React web UI embedded in the daemon (`embed.FS`) |
| Remote access | Tailscale free tier — **zero cloud infrastructure** |

## Roadmap Snapshot

- **Phase 0 — POCs (gate):** stream-json session viability (kill-gate) · RN WebSocket backgrounding · mDNS reliability · Windows process reaping
- **Phase 1 — Host core:** daemon, Claude adapter, event log, WSS hub, pairing
- **Phase 2 — Clients:** Expo Android app + embedded web UI sharing one protocol package
- **Phase 3 — Hardening:** Tailscale path, security pass, crash resilience

**MVP success test:** start a session on PC A → approve a permission and send a prompt from your phone on cellular → PC B's browser shows the same live state → close the app mid-run → the agent keeps going.

## Security

This product is, by design, a remote-control bridge to a coding agent — pairing *is* the security perimeter. One-time short-lived pairing tokens, physical confirmation on the host screen, per-device revocable credentials, pinned certificates, per-interface binding, and agent output always rendered as untrusted. Full model in [docs/ARCHITECTURE.md §7–8](docs/ARCHITECTURE.md).

## Repository Layout

```
MSclaudeConnector/
├── README.md                ← you are here
├── PLAN.md                  ← execution plan & roadmap
└── docs/
    ├── ARCHITECTURE.md      ← architecture & tradeoffs
    └── SYSTEM-DESIGN.md     ← mermaid system diagrams
```

Source code lands after Phase 0 POCs (`/cmd`, `/internal`, `/clients` — layout TBD in implementation planning).
