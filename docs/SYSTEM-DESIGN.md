# SYSTEM-DESIGN.md — Multi-Device Coding-Agent Harness

**Last updated:** 2026-09-16
Companion docs: [../PLAN.md](../PLAN.md) · [ARCHITECTURE.md](ARCHITECTURE.md)

All diagrams are Mermaid — they render natively on GitHub and in VS Code (with a Mermaid extension), or paste into [mermaid.live](https://mermaid.live).

---

## 1. High-Level System Context

```mermaid
flowchart LR
    subgraph PCA["PC A — Windows Host (agent runs here)"]
        AGENT["Claude Code process<br/>(Codex: post-MVP)"]
        subgraph D["Host Daemon — single Go binary"]
            PM["Process Supervisor<br/>(Windows Job Objects)"]
            AD["AgentAdapter<br/>stream-json over stdio"]
            SS["Session Store<br/>append-only event log<br/>(JSONL / embedded SQLite)"]
            HUB["Sync Hub<br/>WSS server + auth"]
            MD["mDNS broadcaster"]
            WEB["Embedded Web UI<br/>(embed.FS)"]
        end
        PM -->|spawns / supervises| AGENT
        AGENT <-->|stdio: stream-json| AD
        AD -->|normalized events| SS
        SS --> HUB
        HUB --> WEB
    end

    PHONE["Phone — React Native (Expo)<br/>Android first"]
    PCB["PC B — any browser"]

    PHONE <-.->|WSS — LAN direct or Tailscale| HUB
    PCB <-.->|HTTPS + WSS — LAN direct| HUB
```

PC A is the single writer and single source of truth. Clients are views + input devices; the agent never runs anywhere else. The daemon owns the agent process, so clients can come and go freely.

---

## 2. Network Topology — LAN vs Remote (no cloud)

```mermaid
flowchart TB
    subgraph HOME["Same LAN (home / office)"]
        H1["PC A — Daemon :7432<br/>mDNS: _harness._tcp.local"]
        B1["PC B — Browser"]
        P1["Phone — Wi-Fi"]
        B1 -->|discover via mDNS<br/>WSS direct| H1
        P1 -->|QR scan or mDNS<br/>WSS direct| H1
    end
    subgraph AWAY["Different network (cellular)"]
        P2["Phone — 4G/5G"]
        H2["PC A — Daemon (tailnet IP)"]
        P2 <-->|WireGuard tunnel<br/>(Tailscale free tier)| H2
    end
```

Zero servers are owned or operated by this project. Tailscale is user-installed infrastructure, not our backend — no cloud DB, no relay, no accounts in MVP. Both paths terminate at the same WSS endpoint on the daemon with the same auth.

---

## 3. Sequence — Device Pairing (once per device)

```mermaid
sequenceDiagram
    participant H as Host Daemon (PC A)
    participant P as Phone / PC B
    H->>H: First run: generate host keypair<br/>+ one-time pairing token (5 min TTL)
    H->>P: QR: {LAN IP, tailnet IP, port,<br/>one-time token, cert fingerprint}
    P->>H: WSS connect + present one-time token
    H->>H: Show device name on PC A screen<br/>for physical confirmation
    H-->>P: Long-lived device credential<br/>(bearer token + pinned host cert)
    Note over P: Credential stored in device keystore.<br/>Revocable per-device from host.
    P->>H: All future connects:<br/>pinned TLS + device bearer token
```

Pairing is the entire security perimeter — the product is a remote-control bridge for a coding agent, so enrollment requires physical access to the host screen and produces short-lived one-time tokens, never reusable secrets.

---

## 4. Sequence — Prompt Flow (multi-client fan-out)

```mermaid
sequenceDiagram
    participant PH as Phone
    participant H as Host Daemon
    participant PB as PC B (Web UI)
    participant A as Claude process
    PH->>H: command send_prompt {id: c-77, text}
    H->>H: Validate → assign seq → append to log
    H-->>PH: ack {c-77}
    H-->>PB: event: user_prompt (seq 411)
    H->>A: stdin: user message (stream-json)
    loop streaming
        A-->>H: stream-json events<br/>(text deltas, tool calls)
        H->>H: Normalize → append (seq++)
        H-->>PH: events 412..N
        H-->>PB: events 412..N
    end
    A-->>H: turn_complete
    H-->>PH: event: turn_complete
    H-->>PB: event: turn_complete
```

The host assigns every event a sequence number before broadcasting, so all clients observe the identical total order. A prompt sent from the phone appears on PC B as a normal stream event — clients never talk to each other.

---

## 5. Sequence — Permission Request (first-write-wins)

```mermaid
sequenceDiagram
    participant A as Claude
    participant H as Host Daemon
    participant PH as Phone
    participant PB as PC B
    A-->>H: permission_request (Bash: "git push")
    H->>H: Append event, mark r9 PENDING
    H-->>PH: permission_request {r9}
    H-->>PB: permission_request {r9}
    PB->>H: answer_permission {r9, allow}
    PH->>H: answer_permission {r9, allow}
    Note over H: PC B arrived first → applied.<br/>Phone's answer for r9 ignored.
    H->>A: stdin: allow
    H-->>PB: permission_resolved {r9, allow, by: "pc-b"}
    H-->>PH: permission_resolved {r9, allow, by: "pc-b"}
    Note over PH,PB: Both UIs grey out the prompt.<br/>Everyone sees who answered.
```

Pending actions are resolved exactly once, at the host, in arrival order. The resolution event carries the answering device's name so every screen shows the same outcome and its origin.

---

## 6. Sequence — Disconnect & Resume (agent keeps running)

```mermaid
sequenceDiagram
    participant P as Phone
    participant H as Host Daemon
    participant A as Claude
    P--xH: connection drops (tunnel, doze, app closed)
    Note over H,A: Agent unaffected —<br/>daemon owns the process.<br/>Events keep appending to log.
    P->>H: HELLO {deviceId, lastSeq: 410}
    H-->>P: SNAPSHOT (materialized state:<br/>status, pending requests, recent context)
    H-->>P: events 411..latest (catch-up)
    H-->>P: live tail (seq++)
    Note over P: Client is read-only while disconnected.<br/>No offline command queue in MVP.
```

Client lifetime is fully decoupled from agent lifetime. Resume costs one round trip: snapshot for derived state, then a catch-up tail from the client's last seen sequence number.

---

## 7. Data / State Model

```mermaid
erDiagram
    HOST ||--o{ DEVICE : "has paired"
    HOST ||--o{ SESSION : "owns"
    SESSION ||--o{ EVENT : "ordered log"
    SESSION {
        string id
        string agentType "claude | codex"
        string workingDir
        string status "running | idle | ended"
        int lastSeq
    }
    EVENT {
        int seq "host-assigned, total order"
        string kind "text_delta | tool_call | permission_request | question | resolved | status"
        json payload
        time ts
    }
    DEVICE {
        string deviceId
        string name
        string credentialHash
        time pairedAt
        time lastSeen
    }
```

Single writer (host) + total order (seq) ⇒ no CRDTs, no conflict resolution, trivial convergence. Materialized state (pending permissions, status) is rebuilt from the log on daemon restart, so the log is the only thing that must survive a crash.

---

## 8. Agent Adapter Abstraction

```mermaid
flowchart LR
    subgraph ADAPTERS["AgentAdapter interface (Go)"]
        CA["ClaudeAdapter<br/>claude -p<br/>--output-format stream-json<br/>--input-format stream-json"]
        FA["CodexAdapter (post-MVP)<br/>codex exec --json /<br/>app-server JSON-RPC"]
    end
    subgraph NORMAL["Normalized event stream"]
        EV["TextDelta | ToolCall |<br/>PermissionRequest | Question |<br/>TurnComplete | StatusChange | Usage | Error"]
    end
    subgraph CMDS["Normalized commands"]
        CM["SendPrompt | AnswerPermission |<br/>AnswerQuestion | Interrupt | SetMode"]
    end
    CA --> NORMAL
    FA --> NORMAL
    CMDS --> CA
    CMDS --> FA
```

Clients and the sync hub only ever see normalized events — vendor specifics stop at the adapter. Adding a third agent means implementing one more adapter; nothing above this layer changes.
