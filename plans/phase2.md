# Phase 2 — Clients

The daemon (Phase 1) is complete and committed at `96852de`. It owns a Claude Code process and an append-only SQLite event log, with all five commands wired hub-side. But **there is no UI a human can use** — `internal/webui/web/index.html` is a static placeholder that doesn't even open a WebSocket, and there is zero TypeScript in the repo.

Phase 2 builds the clients: a shared TS protocol package, a React web UI served by the daemon, and an Expo Android app. Done when a phone and PC B's browser simultaneously follow one live session, and an answer from either greys out on the other with attribution.

**Exploration surfaced five gaps that make Phase 2 impossible as written.** They are not optional polish; three of them are hard blockers, and they are fixed first, in Go, before any TypeScript exists:

| | Gap | Why it blocks |
|---|---|---|
| **A** | Browsers cannot set an `Authorization` header on a WebSocket; `hub.go:81` only reads that header | The web UI literally cannot authenticate |
| **B** | Cert SANs are only `localhost`/`127.0.0.1`/`::1` (`certutil.go:71-72`), but the QR advertises LAN IPs | `https://192.168.x.x:7432` fails hostname verification — the page won't even load on PC B |
| **C** | The startup QR token is created **unapproved** (`main.go:110`) and expires in 5 min while the QR stays on screen | Scanning the QR always fails until someone runs `harness pair --approve` in a second terminal |
| **D** | `webui.go` is a bare `http.FileServer` | SPA deep links 404; no cache headers; no CSP |
| **E** | `ByDevice` carries a 32-hex device id, and `client.name` is set to `deviceID` (`hub.go:176`) | "with attribution" renders as `a3f9…` |

Two further defects found while reading, fixed in the same milestone:

- **Outbound drop → resync livelock.** `handleConnection` pushes snapshot + *all* catch-up events into a 256-deep `c.send`; overflow drops silently. A client that detects the gap reconnects, replays the same long catch-up, drops again — forever. Seq-gap detection alone cannot fix this.
- **Write-ordering race.** `handleCommand` writes directly to `c.conn` in three places (the `cmd.ID == ""` error, the `send_prompt` `bad_request`, and the idempotent-replay `h.ack`), bypassing the `writeLoop` queue. The replay-ack is a common path, so this is not theoretical.

---

## M0 — Go gap fixes

### A. `hello.credential` auth

`internal/protocol/types.go` — additive, `Version` stays 1:

```go
type HelloPayload struct {
	DeviceID     string `json:"deviceId"`
	LastSeq      int64  `json:"lastSeq"`
	PairingToken string `json:"pairingToken,omitempty"`
	Credential   string `json:"credential,omitempty"` // browsers cannot set Authorization
}
```

`internal/hub/hub.go` — restructure `handleConnection` to resolve auth *after* reading the hello frame:

```go
func (h *Hub) handleConnection(ctx context.Context, conn *websocket.Conn, headerCred string) error {
	hello, err := readHello(ctx, conn)
	if err != nil { return err }

	cred := headerCred
	if cred == "" { cred = hello.Credential }
	deviceID, authenticated := "", false
	if cred != "" {
		if d, ok := h.auth.ValidateCredential(cred); ok { deviceID, authenticated = d, true }
	}
	if !authenticated { return h.handlePairing(ctx, conn, hello) }
	return h.serveAuthenticated(ctx, conn, deviceID, hello)
}
```

Properties to preserve:
- `ValidateCredential` called exactly once per connection (bumps `last_seen`).
- Header beats hello.
- Silent downgrade to pairing preserved.
- `device_mismatch` unchanged, catches device A's deviceId paired with device B's credential.

Never log `hello.Credential` or `hello.PairingToken`.

### Outbound queue + write ordering

Replace the fixed 256-deep `client.send` with a high-water-mark queue (`clientQueueHighWater = 4096`). On overflow, close the connection with `websocket.StatusTryAgainLater` — client reconnects from last contiguous seq. Route all per-client writes through the queue. Keep raw-conn writes only for pre-registration handshake.

### B. Cert SANs + SPKI pinning

`internal/certutil/certutil.go`:

```go
type SANs struct { DNSNames []string; IPs []net.IP }
type Pins struct {
	SPKI        string // sha256 of SubjectPublicKeyInfo
	Certificate string // sha256 of DER cert
}
func LoadOrGenerate(certPath, keyPath string, want SANs) (tls.Certificate, Pins, bool, error)
```

**Pin the SPKI, not the certificate.** Reusing `key.pem` across regenerations keeps SPKI stable.

- `generateSelfSigned` takes SANs + optional existing key.
- `key.pem` exists but cert missing/uncovered/near-expiry → load key, mint new cert, rewrite `cert.pem` atomically.
- SAN set in `main.go`: loopback + `netutil.LANIPs()` + localhost/hostname/hostname.local.
- QR field `fingerprint` → `spki`; mDNS TXT `fingerprint=` → `spki=` plus `spkialg=sha256`.
- If regenerated, log Warn. If SPKI changed (key missing), log Error: "host identity changed; all paired devices must re-pair".

### C. Usable QR pairing with host confirmation

Move confirmation from token-creation time to **connect time**.

```go
// internal/auth/auth.go
func (a *Auth) PeekPairingToken(token string) (*store.PendingToken, bool)
func (a *Auth) GeneratePairingTokenTTL(ttl time.Duration) (string, error)
```

```go
// internal/hub/hub.go
type ApprovalRequest struct { Token, RemoteAddr, UserAgent string }
type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (name string, ok bool)
}
```

`cmd/harness/main.go` supplies `terminalApprover`:
- Mutex serializes prompts.
- Prints From / Agent / **Code** / `Approve? [y/N] (60s) name:`.
- **Code = first 4 uppercase hex chars of the token.** Both screens show the same 4 chars.
- 60s deadline via goroutine + select on ctx. Name defaults from User-Agent.
- **Non-TTY guard:** if `os.Stdin.Stat()` shows no CharDevice, don't install approver.

`--pair-ttl` defaults to **30 min** (`0` = lifetime). Add `harness pair --qr` to reprint.

### D. SPA handler

`internal/webui/webui.go` — `spaHandler` wrapping the embedded FS:
- `/assets/*` → `Cache-Control: public, max-age=31536000, immutable`; **404 if missing**.
- Other paths → stat; file → serve; else `index.html` with `no-cache, must-revalidate` + ETag.
- Reject non-GET/HEAD with 405; reject `..`.
- Security headers on every response:
  ```
  Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline';
    connect-src 'self' wss: ws:; img-src 'self' data:; font-src 'self'; object-src 'none';
    base-uri 'none'; form-action 'none'; frame-ancestors 'none'
  X-Content-Type-Options: nosniff
  Referrer-Policy: no-referrer
  ```

### E. Device names

`SnapshotPayload` gains `Devices []DeviceView` (`{deviceId, name}`). Set `client.name` from the store. TS reducer maps `byDevice` → name, rendering `byDevice === myDeviceId` as **"you"**.

### Also in M0

`--insecure-http`: serves `http`/`ws` with a loud warning, and **refuses to start unless every bind address is in `127.0.0.0/8`, RFC1918, or `100.64.0.0/10`**.

**Done when:** `go test ./... -race` green on ubuntu+windows; scripted client authenticates with credential-in-hello; LAN IP in SANs; CSP headers present; QR scan produces console approval prompt and pairs.

---

## Monorepo layout

npm workspaces rooted at `clients/`, **not** the repo root.

```
MSclaudeConnector/
├── plans/PHASE2.md
├── Makefile · .nvmrc · .gitattributes · .github/workflows/ci.yml
├── clients/
│   ├── package.json (workspaces) · tsconfig.base.json · .eslintrc.cjs · allowed-deps.txt
│   ├── packages/harness-protocol/ → @harness/protocol
│   └── apps/{web,mobile}/ → @harness/web, @harness/mobile
└── internal/webui/web/ ← COMMITTED Vite output
```

**Metro resolution:** Consume built `dist/`, never TS source. `@harness/protocol` builds with `tsup` to ESM+CJS+`.d.ts`. `metro.config.js`: `watchFolders`, explicit `nodeModulesPaths`, `disableHierarchicalLookup`, `unstable_enableSymlinks`. Verify `npm ls react react-native` shows single copy in CI.

---

## M1 — `@harness/protocol`

Hand-written TS mirror of protocol types (no codegen — 200-line Go file is stable). `internal/protocol/drift_test.go` reflects structs, diffs against golden list.

Key wire facts: envelope key is `"v"`; `ts` is Unix **millis**; `ack.seq` absent on idempotent replay; `tool_call_*` input/output are **strings**; `turn_complete` and `error` have **no** body; `recentEvents` never populated. Every accessor validates defensively.

### `HarnessClient` behaviors

- **Auth modes:** `'hello'` (browser) in body; `'header'` (RN) in transport headers.
- **Frame ordering:** exactly one `snapshot`, then ascending catch-up, then live tail. `snapshot.lastSeq` is server tip — **not** assigned to client's `lastSeq`.
- **Seq handling:** `+1` → apply; `<= lastSeq` → ignore; `> lastSeq+1` → gap, close, reconnect from last contiguous. Three consecutive gaps at same seq → full resync.
- **Backoff:** `min(max, base * factor^attempt) * (1 ± jitter)`; 500ms / 2 / 30s / 0.3. Reset on successful snapshot. `auth_required`/`pairing_denied`/`device_mismatch` → **terminal**, re-pair prompt.
- **Commands:** `id = uuid()`, `idempotencyKey = id`, reused across reconnect replays. Offline queue (32), flushed on `live`.
- **Ack correlation:** absent `seq` = normal idempotent replay. Late `error{agent_error}` must not reject settled promise.
- **Pairing:** `hello{pairingToken}` → one `paired` frame → server closes → persist `{deviceId, credential, url, spki}` → `connect()`.

### Reducer

`SessionState` holds `items[]` (prompt|assistant|tool|permission|question|notice), `pendingPermissions`/`pendingQuestions` as arrays, `resolutions`, `devices`, `usage`, `notices` (capped 50), `turnActive`.

- `text_delta` → append/start assistant item. Clamp at 256 KB.
- `turn_complete` → seal + `turnActive = false`. `user_prompt` → new item, seals open assistant.
- `tool_call_end` → LIFO match on last unfinished tool with same name.
- `permission_resolved` → remove from pending, leave item greyed with "Allowed by \<name>".
- `applySnapshot` seeds status/mode/tip/devices/pending, **does not touch `items` or `lastSeq`**.
- **Purity:** full replay from `lastSeq: 0` equals incremental application.

**Done when:** vitest + `tsc --noEmit` green; `dist/` emits with `.d.ts`; Node smoke script drives real mock session.

---

## Untrusted rendering (enforced in four places)

Agent output is a prompt-injection and XSS vector. **Agent text renders as plain text, always.**

1. **CSP header** — `script-src 'self'; object-src 'none'; base-uri 'none'`.
2. **One rendering component per platform.** Web `PlainText.tsx` renders as React text child in `pre-wrap` span. Mobile `PlainText.tsx` uses `<Text selectable dataDetectorType="none">`, never `android:autoLink`. **No other component may interpolate agent strings.**
3. **CI lint** — `react/no-danger`; `no-restricted-syntax` banning `dangerouslySetInnerHTML`/`innerHTML`/`outerHTML`/`eval`; banning `window.open`/`Linking`; `no-restricted-imports` banning markdown renderers **and sanitizers**.
4. **Dependency allowlist** (`clients/allowed-deps.txt`) in CI.

No auto-linkification; no URL opened automatically or on tap.

---

## M2–M4 — Web UI

Screens: `PairScreen` (URL + pasted token + 4-char code) and `SessionScreen` (header / stream / pending cards / composer). Components: `PlainText`, `ConnectionBadge` (syncing N/M, retry countdown), `StreamView`, `Prompt/Assistant/ToolCall/Notice`, `PermissionCard`, `QuestionCard`, `PromptInput`, `ModeSelector`, `InterruptButton`, `UsageBar`.

**State:** module-level `HarnessClient` singleton + `useSyncExternalStore`. Coalesce `text_delta` into 50ms rAF flush.

**Production same-origin** — served at `https://<host>:7432/`, WS URL from `location.host`.

**Dev loop:** use Vite proxy:

```ts
server: { port: 5173, proxy: { '/ws': { target: 'https://127.0.0.1:7432', ws: true, secure: false } } },
build: { outDir: '../../../internal/webui/web', assetsDir: 'assets', sourcemap: false, target: 'es2022' }
```

**Credential storage:** `localStorage['harness.identity']`. XSS on this origin grants remote shell. Mitigated by CSP + no-HTML + allowlist.

**M2 (read-only) is first demo** — pair, badge, stream, tool items. **M3** adds interactive controls; two browser tabs show cross-attribution greying. **M4** wires build: Vite `outDir` → `internal/webui/web/`, committed bundle, Makefile, CI.

---

## M5–M6 — Expo app

**No router** — two screens by `ConnPhase`. `expo-camera` with `barcodeScannerSettings={{ barcodeTypes: ['qr'] }}`. QR parsing in shared `pairing.ts`; `candidateUrls()` orders tailnet first, RFC1918, 3s timeout per candidate.

**Credentials:** `expo-secure-store` (Android Keystore). `lastSeq` → `AsyncStorage`.

### Android self-signed TLS

**No JavaScript-only solution.** RN's WebSocket goes through OkHttp.

**Answer:** `modules/harness-tls` — Expo local module (~120 lines Kotlin) calling `OkHttpClientProvider.setOkHttpClientFactory(...)`. Installs `X509TrustManager` that, for registered harness host:port, compares SHA-256 of leaf SPKI constant-time against QR-pinned value, delegates to platform default for others. JS: `HarnessTls.setPinnedHosts([{host, port, spki}])`. Requires dev-client/prebuild.

**M5 runs over `--insecure-http`** so UI is never blocked on native work. Kotlin lands in M6, timeboxed to 2 days; escalates to "demo over --insecure-http on LAN, pinning slips to Phase 3" if overruns.

**Backgrounding:** `AppState` listener; on `active`, reconnect with `attempt` reset to 0. Persist `lastSeq` debounced ~2s. Cap `items` at ~2000 with head-trimming.

---

## Build wiring

**Makefile:** `web-install`, `protocol`, `web-build`, `web-verify`, `go-build`, `build`, `test`, `lint`, `mobile-run`, `clean`. `web-build` builds to temp dir and swaps.

**`.gitattributes`:** `internal/webui/web/** linguist-generated=true -diff`. **`.gitignore`:** `clients/node_modules/`, `clients/packages/*/dist/`, `clients/apps/mobile/{.expo,android,ios}/`, `*.tsbuildinfo`.

**CI (`.github/workflows/ci.yml`)** — four jobs: `go` (ubuntu+windows; vet, build, test -race; no Node) · `ts` (typecheck, lint, vitest, build) · `freshness` (rebuild + diff) · `deps` (allowlist). Dependabot for gomod/npm/actions.

**Freshness reproducibility:** `.nvmrc` + exact-pinned `vite`, `npm ci` only, ubuntu-only. If flaky, degrade (compare filenames + SHA-256), never delete bundle.

---

## Verification

**Go tests:** Extend `internal/hub/hub_test.go`: `TestHelloCredentialAuth`, `TestHeaderBeatsHelloCredential`, `TestDeviceMismatchViaHelloCredential`, `TestValidateCredentialCalledOnce`, `TestCatchUpLargeLogNoDrop`, `TestOutboundOverflowClosesWithTryAgainLater`. New `pairing_test.go`: fake `Approver`. `certutil_test.go`: SANs present, regen when uncovered, **SPKI unchanged**. `webui_test.go`: deep link → index; `/assets/missing.js` → **404**; CSP + nosniff; `POST` → 405. `protocol/drift_test.go`.

**TS tests (vitest):** Reducer per `EventKind`; delta coalescing; tool LIFO; permission pending → resolved → greyed; snapshot with new `sessionId` resets; **property test** full replay ≡ incremental. Client: hello per auth mode; seq gap → reconnect at last contiguous; 3 gaps → full resync; terminal vs retryable; ack with/without `seq`; late `error` doesn't reject settled promise; offline queue flushes with same `idempotencyKey`; pairing closes/reconnects.

**Manual acceptance — Phase 2 done-criteria:**
1. `harness serve --agent claude --dir <project>`; QR on screen.
2. Phone scans → console approval block with matching 4-char code → approve → live stream.
3. PC B opens `https://<lan-ip>:7432/` — **no hostname mismatch** — pastes token → approve → live.
4. Prompt from phone → `user_prompt` on both with attribution "Pixel 8"; text streams to both.
5. Permission → card on both → **answer on PC B** → phone greys "Allowed by PC-B-Chrome" within ~1s. ← **core criterion**
6. Question from phone → PC B greys with attribution.
7. Kill phone mid-turn → agent runs → reopen → snapshot + catch-up, no gaps/duplicates.
8. Airplane-mode 60s mid-turn → restore → `reconnecting` → `syncing N/M` → `live`; identical to PC B.
9. `harness pair --revoke <deviceId>` → phone's next connect → `unauthorized`, re-pair prompt.
10. Change PC A's IP → restart → cert regenerates with new SANs, **SPKI unchanged**, both reconnect **without re-pairing**.

---

## Milestones

| | Scope | Done when |
|---|---|---|
| **M0** | Go gaps A–E, queue + ordering, `--insecure-http`, `--pair-ttl`, `pair --qr`, drift test | `go test -race` green both; credential-in-hello works; LAN IP in SANs; CSP present; QR scan produces console prompt and pairs |
| **M1** | `@harness/protocol` complete with tests | vitest + `tsc --noEmit` green; `dist/` with `.d.ts`; Node smoke script drives real mock |
| **M2** | Web UI read-only — **first demo** | Browser follows live mock, survives restart |
| **M3** | Web UI interactive | Two browser tabs show cross-attribution greying |
| **M4** | Build wiring + committed bundle + CI | Fresh clone with no Node runs `go build`, serves UI over LAN |
| **M5** | Expo scaffold, QR, read-only (over `--insecure-http`) | Phone shows live, survives background/foreground |
| **M6** | Android TLS + interactive mobile — **Phase 2 complete** | `wss://` pinned; all 10 acceptance steps pass |
| **M7** | `docs/PHASE2.md`, README, mark complete | Docs match shipped behavior |

---

## Risks

1. **Android WSS pinning** — only native code, no fallback. Mitigated by `--insecure-http` in M0; 2-day budget.
2. **Metro workspace resolution** — half-day budget; `postinstall` copy escape hatch.
3. **Bundle freshness flakiness** — degrade, never drop.
4. **Committed-bundle merge conflicts** — `.gitattributes -diff`; rebuild as last commit on branch.
5. **30-min QR token** — acceptable only because console approval is real perimeter.
6. **Credential in `localStorage`** — CSP + no-HTML + allowlist; cookie upgrade Phase 3.
7. **Tool-call mis-pairing** — cosmetic, documented, Phase 3 addition.

**Cut list, in order:** mobile `ModeSelector`/`UsageBar`/`InterruptButton` (web-only) → persisted `lastSeq` on mobile → Gap E roster → strict byte freshness → QR auto-reissue → stream virtualization.

**Do not cut:** outbound-queue fix, Gap B, `PlainText` + CSP chokepoint, idempotency-key stability.
