# Phase 2 — Clients

Phase 2 turns the Phase 1 daemon into something a human can actually use: a
shared TypeScript protocol package, a React web UI served by the daemon, and an
Expo Android app. It also fixes the Go-side gaps that made clients impossible
before any TypeScript existed.

Companion docs: [../PLAN.md](../PLAN.md) · [ARCHITECTURE.md](ARCHITECTURE.md) · [SYSTEM-DESIGN.md](SYSTEM-DESIGN.md)

---

## 1. Go gap fixes (M0)

### A. Credential in `hello`

Browsers cannot set an `Authorization` header on a WebSocket handshake, so the
daemon now resolves auth **after** reading the `hello` frame:

```go
cred := headerCred
if cred == "" {
    cred = hello.Credential
}
```

`HelloPayload.Credential` is additive; `Version` stays `1`. Header beats hello.
`ValidateCredential` runs at most once per connection (it bumps `last_seen`).
The credential and pairing token are never logged.

### B. Certificate SANs + SPKI pinning

`certutil.LoadOrGenerate(certPath, keyPath, want SANs) (tls.Certificate, Pins, bool, error)`
covers loopback + every LAN/tailnet IP + `localhost`/hostname/hostname.local.
If `key.pem` exists it is reused, so regenerating the certificate keeps the
**SPKI** stable and paired devices do not re-pair. The QR field is now `spki`
(not `fingerprint`), and mDNS advertises `spki=` plus `spkialg=sha256`.

If the key is missing the SPKI changes and the daemon logs:
`host identity changed; all paired devices must re-pair`.

### C. Usable QR pairing with host confirmation

Confirmation moved from token-creation time to **connect time**:

- `Auth.PeekPairingToken` / `Auth.GeneratePairingTokenTTL`
- `hub.Approver` with `RequestApproval(ctx, ApprovalRequest)`
- `terminalApprover` prints From / Agent / **Code** / `Approve? [y/N] (60s) name:`.
  **Code = first 4 uppercase hex chars of the token**; both screens show the
  same 4 characters.
- Non-TTY guard: without a TTY no approver is installed and only out-of-band
  `harness pair --approve` tokens are accepted.
- `--pair-ttl` defaults to **30 min** (`0` = never expires); `harness pair --qr`
  reprints a QR with a fresh token.

### D. SPA handler

`internal/webui/webui.go` serves the embedded bundle with:

- `/assets/*` → `Cache-Control: public, max-age=31536000, immutable`, **404 if missing**
- other paths → stat, else `index.html` with `no-cache, must-revalidate` + ETag
- non-GET/HEAD → 405; `..` rejected
- CSP (`script-src 'self'; object-src 'none'; base-uri 'none'`), `nosniff`,
  `Referrer-Policy: no-referrer` on every response

### E. Device names

`SnapshotPayload.Devices []DeviceView` (`{deviceId, name}`) lets clients render
attribution by name; the reducer maps `byDevice` → name and the UI renders the
local device as **"you"**.

### Outbound queue + write ordering

The fixed 256-deep `client.send` is now a 4096 high-water queue. On overflow the
connection closes with `StatusTryAgainLater`, so the client reconnects from its
last contiguous seq instead of silently dropping events. All per-client writes
route through the queue; raw-conn writes remain only for the pre-registration
handshake. This removes the catch-up resync livelock and the replay-ack race.

### `--insecure-http`

Serves `http`/`ws` with a loud warning and **refuses to start unless every
advertised bind address is loopback, RFC1918, or CGNAT (100.64.0.0/10)**.

---

## 2. Monorepo

```
MSclaudeConnector/
├── Makefile · .nvmrc · .gitattributes · .github/workflows/ci.yml
├── clients/
│   ├── package.json (workspaces) · tsconfig.base.json · .eslintrc.cjs · allowed-deps.txt
│   ├── packages/harness-protocol/ → @harness/protocol
│   └── apps/{web,mobile}/ → @harness/web, @harness/mobile
└── internal/webui/web/ ← COMMITTED Vite output
```

`@harness/protocol` builds with `tsup` to ESM + CJS + `.d.ts`. Both clients
consume the built `dist/`, never TS source. `internal/protocol/drift_test.go`
pins the Go wire structs; if the Go source of truth changes, the mirror in
`clients/packages/harness-protocol/src/types.ts` must change too.

`clients/go.mod` is an empty module boundary: some npm packages ship Go source,
so without it `go build/vet/test ./...` would descend into
`clients/node_modules`.

> **Mobile install note.** `apps/mobile` is intentionally **not** part of the
> root npm workspace: the Expo dependency tree is large and was not practical to
> co-install in this environment. It has its own `package.json` and consumes
> `@harness/protocol` via `file:../../packages/harness-protocol`:
>
> ```bash
> cd clients && npm run protocol      # build dist first
> cd clients/apps/mobile && npm install
> npx expo run:android                # requires dev-client / prebuild for native TLS
> ```

---

## 3. `@harness/protocol` (M1)

Hand-written TS mirror plus `HarnessClient` and the reducer.

Key wire facts encoded and tested: envelope key is `"v"`; `ts` is Unix
**millis**; `ack.seq` is absent on idempotent replay; `tool_call_*` input/output
are **strings**; `turn_complete` and `error` have no body; `recentEvents` is
never populated. Every accessor validates defensively.

`HarnessClient`:

- **Auth modes:** `hello` (browser, credential in body) / `header` (RN, transport headers).
- **Frames:** exactly one `snapshot`, then ascending catch-up, then live tail.
  `snapshot.lastSeq` is the server tip and is **not** assigned to the client's
  `lastSeq`.
- **Seq:** `+1` apply; `<= lastSeq` ignore; `> lastSeq+1` gap → close and
  reconnect from last contiguous. Three consecutive gaps at the same seq →
  full resync (`lastSeq: 0`).
- **Backoff:** `min(30s, 500ms * 2^attempt) * (1 ± 0.3)`, reset on snapshot.
- **Terminal errors** (`auth_required`, `pairing_denied`, `device_mismatch`,
  `unauthorized`) stop reconnection and surface a re-pair prompt.
- **Commands:** `id = uuid()`, `idempotencyKey = id`, reused across reconnect
  replays. Outbox capped at 32, flushed on `live`.
- **Acks:** absent `seq` is a normal idempotent replay; a late
  `error{agent_error}` never rejects a settled promise.
- **Pairing:** `hello{pairingToken}` → one `paired` frame → persist identity →
  `connect()`.

Reducer: `SessionState` holds `items[]`, `pendingPermissions`/`pendingQuestions`,
`devices`, `usage`, `turnActive`. `text_delta` coalesces into an open assistant
item (clamped at 256 KB); `turn_complete` seals it; `user_prompt` starts a new
item and seals the open assistant; `tool_call_end` matches LIFO on the last
unfinished tool of the same name; `permission_resolved` removes the pending card
and greys the item with "Allowed by \<name\>". `applySnapshot` seeds
status/mode/tip/devices/pending and never touches `items` or `lastSeq`. The
reducer is pure: a split replay equals a whole replay (property test).

---

## 4. Web UI (M2–M4)

Screens `PairScreen` (URL + pasted token/JSON + 4-char code) and `SessionScreen`
(header / stream / pending cards / composer). Components: `PlainText`,
`ConnectionBadge` (syncing N/M, retry countdown), `StreamView`, item views,
`PermissionCard`, `QuestionCard`, `PromptInput`, `ModeSelector`,
`InterruptButton`, `UsageBar`.

State is a module-level `HarnessClient` singleton read through
`useSyncExternalStore`. Credentials live in `localStorage['harness.identity']`
(XSS here grants a remote shell; mitigated by CSP + no-HTML + the dependency
allowlist — a cookie upgrade is Phase 3).

Production is same-origin at `https://<host>:7432/`; the WS URL derives from
`location.host`. Dev uses the Vite proxy (`/ws` → `https://127.0.0.1:7432`,
`ws: true`, `secure: false`). `clients/scripts/web-build.mjs` builds to a temp
dir and atomically swaps into `internal/webui/web/`.

---

## 5. Expo app (M5–M6)

Two screens selected by `ConnPhase` (no router). `expo-camera` scans QR with
`barcodeScannerSettings={{ barcodeTypes: ['qr'] }}`; `parsePairingPayload` and
`candidateUrls` (tailnet → RFC1918 → loopback, 3s timeout per candidate) live in
the shared package. Credentials use `expo-secure-store`; `lastSeq` uses
`AsyncStorage` (debounced ~2s). An `AppState` listener reconnects with backoff
reset on foreground; items are capped at ~2000 with head-trimming.

### Android self-signed TLS

RN's WebSocket runs through OkHttp, so there is no JS-only trust override.
`modules/harness-tls` is an Expo local module (`~120` lines of Kotlin) that calls
`OkHttpClientProvider.setOkHttpClientFactory(...)`, installing an
`X509TrustManager` that accepts a chain when the leaf's SHA-256 **SPKI** matches
a QR-pinned value (constant-time) and otherwise delegates to the platform
default. The hostname verifier does the same. JS registers pins via
`HarnessTls.setPinnedHosts([{ host, port, spki }])`. Requires a dev-client /
prebuild. Until then, run over `--insecure-http`.

---

## 6. Untrusted rendering (four places)

Agent output is a prompt-injection and XSS vector; it renders as plain text
**always**:

1. **CSP header** — `script-src 'self'; object-src 'none'; base-uri 'none'`.
2. **One component per platform.** Web `PlainText.tsx` is a React text child in
   a `pre-wrap` span; mobile `PlainText.tsx` uses
   `<Text selectable dataDetectorType="none">`, never `android:autoLink`.
3. **CI lint** — `react/no-danger`; `no-restricted-syntax` banning
   `dangerouslySetInnerHTML`/`innerHTML`/`outerHTML`/`eval`/`Function`/
   `window.open`; `no-restricted-imports` banning markdown renderers **and**
   sanitizers.
4. **Dependency allowlist** (`clients/allowed-deps.txt`) checked by CI.

No auto-linkification; no URL is opened automatically or on tap.

---

## 7. Build & CI

`make protocol`, `make web-build`, `make web-verify`, `make test`, `make lint`,
`make deps`, `make mobile-run`, `make clean`.

CI (`.github/workflows/ci.yml`) runs four jobs:

- **go** — ubuntu + windows: `go vet`, `go build`, `go test ./... -race` (no Node)
- **ts** — protocol build, typecheck, lint, vitest, web build
- **freshness** — rebuild the bundle and `git diff --exit-code internal/webui/web`
- **deps** — production dependency allowlist

Dependabot covers gomod, npm (`/clients`), and GitHub Actions.

---

## 8. Manual acceptance checklist

1. `harness serve --agent claude --dir <project>`; QR on screen.
2. Phone scans → console approval block with matching 4-char code → approve → live stream.
3. PC B opens `https://<lan-ip>:7432/` with no hostname mismatch → pastes token → approve → live.
4. Prompt from phone → `user_prompt` on both with attribution "Pixel 8"; text streams to both.
5. Permission → card on both → answer on PC B → phone greys "Allowed by PC-B-Chrome" within ~1s.
6. Question from phone → PC B greys with attribution.
7. Kill phone mid-turn → agent runs → reopen → snapshot + catch-up, no gaps/duplicates.
8. Airplane-mode 60s mid-turn → restore → `reconnecting` → `syncing N/M` → `live`.
9. `harness pair --revoke <deviceId>` → phone's next connect → `unauthorized`, re-pair prompt.
10. Change PC A's IP → restart → cert regenerates with new SANs, **SPKI unchanged**, both reconnect without re-pairing.

The automated equivalents live in `internal/hub/*_test.go`, `internal/certutil`,
`internal/webui`, `internal/protocol/drift_test.go`, the vitest suites, and
`clients/scripts/smoke.mjs` (real daemon + mock agent + real client).

---

## 9. Deviations from the plan

- **Mobile is a standalone install** rather than a root npm workspace (see §2).
  Metro is still configured for the monorepo (`watchFolders`, explicit
  `nodeModulesPaths`, `disableHierarchicalLookup`, `unstable_enableSymlinks`)
  and consumes built `dist/`.
- The Android TLS module is written but not compiled in this environment; it is
  timeboxed per the plan and the fallback is `--insecure-http` on the LAN.
