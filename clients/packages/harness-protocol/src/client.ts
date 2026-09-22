import {
  applyEvent,
  applySnapshot,
  newSessionState,
  trimItems,
  type SessionState,
} from './reducer.js';
import {
  PROTOCOL_VERSION,
  parseEnvelope,
  TERMINAL_ERROR_CODES,
  type AckPayload,
  type AnswerPermissionBody,
  type AnswerQuestionBody,
  type CommandKind,
  type CommandPayload,
  type Envelope,
  type ErrorPayload,
  type EventPayload,
  type HelloPayload,
  type SnapshotPayload,
} from './types.js';
import { uuid } from './uuid.js';

export type AuthMode = 'hello' | 'header';

export type ConnPhase =
  | 'idle'
  | 'connecting'
  | 'pairing'
  | 'syncing'
  | 'live'
  | 'reconnecting'
  | 'unauthorized'
  | 'error';

export interface HarnessIdentity {
  deviceId: string;
  credential: string;
  url: string;
  spki?: string;
  name?: string;
}

export interface ClientState {
  phase: ConnPhase;
  session: SessionState;
  myDeviceId: string | null;
  retryAttempt: number;
  retryInMs: number | null;
  /** Non-null while catching up after a reconnect: applied/total events. */
  syncing: { applied: number; total: number } | null;
  error: { code: string; message: string } | null;
}

/** Minimal surface of the platform WebSocket used by the client. */
export interface SocketLike {
  readyState: number;
  send(data: string): void;
  close(code?: number, reason?: string): void;
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code?: number; reason?: string }) => void) | null;
  onerror: ((ev: unknown) => void) | null;
}

export type TimerHandle = ReturnType<typeof setTimeout>;

export interface HarnessClientOptions {
  url: string;
  authMode?: AuthMode;
  identity?: HarnessIdentity | null;
  createSocket?: (url: string, headers: Record<string, string>) => SocketLike;
  setTimer?: (fn: () => void, ms: number) => TimerHandle;
  clearTimer?: (handle: TimerHandle) => void;
  random?: () => number;
  /** Cap retained stream items (mobile sets ~2000; web leaves unbounded). */
  maxItems?: number;
}

interface PendingCommand {
  resolve: (ack: AckPayload) => void;
  reject: (err: Error) => void;
  settled: boolean;
}

const BACKOFF_BASE_MS = 500;
const BACKOFF_FACTOR = 2;
const BACKOFF_MAX_MS = 30_000;
const BACKOFF_JITTER = 0.3;
const MAX_OUTBOX = 32;

function defaultCreateSocket(url: string, headers: Record<string, string>): SocketLike {
  const RNWebSocket = globalThis.WebSocket as unknown as {
    new (u: string, protocols?: unknown, options?: unknown): SocketLike;
  };
  if (Object.keys(headers).length > 0) {
    return new RNWebSocket(url, undefined, { headers });
  }
  return new RNWebSocket(url);
}

export class HarnessClient {
  private url: string;
  private authMode: AuthMode;
  private identity: HarnessIdentity | null;
  private socket: SocketLike | null = null;
  private createSocket: (url: string, headers: Record<string, string>) => SocketLike;
  private setTimer: (fn: () => void, ms: number) => TimerHandle;
  private clearTimer: (handle: TimerHandle) => void;
  private random: () => number;
  private maxItems: number | undefined;

  private session: SessionState;
  private phase: ConnPhase = 'idle';
  private error: { code: string; message: string } | null = null;
  private retryAttempt = 0;
  private retryInMs: number | null = null;
  private reconnectTimer: TimerHandle | null = null;
  private syncBase = 0;
  private gapSeq: number | null = null;
  private gapCount = 0;
  private fullResync = false;
  private closedByUser = false;

  private outbox = new Map<string, CommandPayload>();
  private pending = new Map<string, PendingCommand>();

  private listeners = new Set<() => void>();
  private current: ClientState;

  constructor(options: HarnessClientOptions) {
    this.url = options.url;
    this.authMode = options.authMode ?? 'hello';
    this.identity = options.identity ?? null;
    this.createSocket = options.createSocket ?? defaultCreateSocket;
    this.setTimer = options.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
    this.clearTimer = options.clearTimer ?? ((h) => clearTimeout(h));
    this.random = options.random ?? Math.random;
    this.maxItems = options.maxItems;
    this.session = newSessionState();
    this.current = this.buildState();
  }

  // --- external store surface ---

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  getState = (): ClientState => this.current;

  // --- lifecycle ---

  setIdentity(identity: HarnessIdentity | null): void {
    this.identity = identity;
    this.notify();
  }

  /** Restore a persisted lastSeq before connecting (mobile app restart). */
  setLastSeq(seq: number): void {
    if (!Number.isFinite(seq) || seq <= this.session.lastSeq) return;
    this.session = { ...this.session, lastSeq: seq };
    this.notify();
  }

  reset(): void {
    this.closedByUser = false;
    this.session = newSessionState();
    this.error = null;
    this.gapCount = 0;
    this.gapSeq = null;
    this.fullResync = false;
  }

  connect(identity?: HarnessIdentity): void {
    if (identity) this.identity = identity;
    this.closedByUser = false;
    this.gapCount = 0;
    this.retryAttempt = 0;
    this.retryInMs = null;
    if (this.reconnectTimer) {
      this.clearTimer(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.openNormalSocket();
  }

  close(): void {
    this.closedByUser = true;
    if (this.reconnectTimer) {
      this.clearTimer(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.socket?.close(1000, 'client closing');
    this.socket = null;
    this.setPhase('idle');
  }

  /** Pair with a one-time token, persist the issued credential, then connect. */
  async pair(params: { token: string; url?: string; spki?: string }): Promise<HarnessIdentity> {
    this.url = params.url ?? this.url;
    this.setPhase('pairing');
    this.error = null;

    const identity = await new Promise<HarnessIdentity>((resolve, reject) => {
      let socket: SocketLike;
      try {
        socket = this.createSocket(this.url, {});
      } catch (err) {
        reject(err instanceof Error ? err : new Error(String(err)));
        return;
      }
      let settled = false;
      const finish = (fn: () => void) => {
        if (settled) return;
        settled = true;
        fn();
      };

      socket.onopen = () => {
        this.sendEnvelope(socket, {
          v: PROTOCOL_VERSION,
          type: 'hello',
          hello: { deviceId: '', lastSeq: 0, pairingToken: params.token },
        });
      };
      socket.onmessage = (event) => {
        const env = this.decode(event.data);
        if (!env) return;
        if (env.type === 'paired' && env.paired) {
          finish(() => {
            try {
              socket.close(1000, 'paired');
            } catch {
              /* ignore */
            }
            const issued: HarnessIdentity = {
              deviceId: env.paired!.deviceId,
              credential: env.paired!.credential,
              name: env.paired!.name,
              url: this.url,
            };
            if (params.spki) issued.spki = params.spki;
            this.identity = issued;
            resolve(issued);
          });
          return;
        }
        if (env.type === 'error' && env.error) {
          finish(() => {
            try {
              socket.close(1000, 'pairing failed');
            } catch {
              /* ignore */
            }
            reject(new Error(`${env.error!.code}: ${env.error!.message}`));
          });
        }
      };
      socket.onerror = () => {
        finish(() => reject(new Error('pairing socket error')));
      };
      socket.onclose = () => {
        finish(() => reject(new Error('pairing socket closed before completion')));
      };
    });

    this.connect(identity);
    return identity;
  }

  // --- commands ---

  sendPrompt(text: string): Promise<AckPayload> {
    return this.sendCommand('send_prompt', { text });
  }

  answerPermission(body: AnswerPermissionBody): Promise<AckPayload> {
    return this.sendCommand('answer_permission', body);
  }

  answerQuestion(body: AnswerQuestionBody): Promise<AckPayload> {
    return this.sendCommand('answer_question', body);
  }

  setMode(mode: string): Promise<AckPayload> {
    return this.sendCommand('set_mode', { mode });
  }

  interrupt(): Promise<AckPayload> {
    return this.sendCommand('interrupt', {});
  }

  private sendCommand(kind: CommandKind, payload: unknown): Promise<AckPayload> {
    if (this.outbox.size >= MAX_OUTBOX) {
      return Promise.reject(new Error('offline_queue_full'));
    }
    const id = uuid();
    const command: CommandPayload = { id, idempotencyKey: id, kind, payload };
    this.outbox.set(id, command);
    return new Promise<AckPayload>((resolve, reject) => {
      this.pending.set(id, { resolve, reject, settled: false });
      this.flushOutbox();
    });
  }

  private flushOutbox(): void {
    if (this.phase !== 'live' || !this.socket || this.socket.readyState !== 1) return;
    for (const command of this.outbox.values()) {
      this.sendEnvelope(this.socket, {
        v: PROTOCOL_VERSION,
        type: 'command',
        command,
      });
    }
  }

  // --- socket handling ---

  private openNormalSocket(): void {
    const firstConnect = this.session.sessionId === null;
    this.setPhase(firstConnect ? 'connecting' : 'reconnecting');

    const headers: Record<string, string> = {};
    if (this.authMode === 'header' && this.identity) {
      headers.Authorization = `Bearer ${this.identity.credential}`;
    }
    if (this.fullResync) {
      this.session = newSessionState();
      this.fullResync = false;
    }

    let socket: SocketLike;
    try {
      socket = this.createSocket(this.url, headers);
    } catch (err) {
      this.error = { code: 'socket_error', message: String(err) };
      this.scheduleReconnect();
      return;
    }
    this.socket = socket;
    this.attachNormal(socket);
  }

  private attachNormal(socket: SocketLike): void {
    socket.onopen = () => {
      const hello: HelloPayload = {
        deviceId: this.identity?.deviceId ?? '',
        lastSeq: this.session.lastSeq,
      };
      if (this.authMode === 'hello' && this.identity) {
        hello.credential = this.identity.credential;
      }
      this.sendEnvelope(socket, { v: PROTOCOL_VERSION, type: 'hello', hello });
    };

    socket.onmessage = (event) => {
      const env = this.decode(event.data);
      if (env) this.handleEnvelope(env);
    };

    socket.onerror = () => {
      /* close follows */
    };

    socket.onclose = (event) => {
      if (this.closedByUser) return;
      if (event?.code === 1008 || event?.code === 4001) {
        this.setPhase('unauthorized');
        return;
      }
      if (this.phase === 'unauthorized' || this.phase === 'error') return;
      this.scheduleReconnect();
    };
  }

  private handleEnvelope(env: Envelope): void {
    switch (env.type) {
      case 'snapshot':
        if (env.snapshot) this.handleSnapshot(env.snapshot);
        return;
      case 'event':
        if (env.event) this.handleEvent(env.event);
        return;
      case 'ack':
        if (env.ack) this.handleAck(env.ack);
        return;
      case 'error':
        if (env.error) this.handleError(env.error);
        return;
      default:
        return;
    }
  }

  private handleSnapshot(snap: SnapshotPayload): void {
    const wasDifferentSession = this.session.sessionId !== null && this.session.sessionId !== snap.sessionId;
    this.session = applySnapshot(this.session, snap);
    if (wasDifferentSession) this.outbox.clear();
    this.syncBase = this.session.lastSeq;
    this.retryAttempt = 0;
    // Deliberately do NOT reset gap tracking here: if a reconnect keeps landing
    // on the same gap, that streak must reach 3 to trigger a full resync.

    if (this.session.lastSeq >= this.session.tipSeq) {
      this.setPhase('live');
      this.flushOutbox();
    } else {
      this.setPhase('syncing');
    }
  }

  private handleEvent(ev: EventPayload): void {
    const seq = typeof ev.seq === 'number' ? ev.seq : 0;
    if (seq <= this.session.lastSeq) return; // duplicate
    if (seq > this.session.lastSeq + 1) {
      this.noteGap(seq);
      return;
    }
    this.session = applyEvent(this.session, ev);
    if (this.maxItems !== undefined && this.session.items.length > this.maxItems) {
      this.session = { ...this.session, items: trimItems(this.session.items, this.maxItems) };
    }
    this.gapSeq = null;
    this.gapCount = 0;
    if (this.session.lastSeq >= this.session.tipSeq) {
      if (this.phase !== 'live') {
        this.setPhase('live');
        this.flushOutbox();
      } else {
        this.notify();
      }
    } else {
      this.notify();
    }
  }

  private noteGap(seq: number): void {
    if (this.gapSeq === seq) this.gapCount += 1;
    else {
      this.gapSeq = seq;
      this.gapCount = 1;
    }
    if (this.gapCount >= 3) {
      // Repeated gaps at the same seq mean the backlog cannot be replayed
      // incrementally; rebuild from a full snapshot instead.
      this.fullResync = true;
      this.gapCount = 0;
      this.gapSeq = null;
    }
    this.detachAndClose(1011, 'seq gap');
    if (!this.closedByUser) this.scheduleReconnect();
  }

  private detachAndClose(code: number, reason: string): void {
    const socket = this.socket;
    this.socket = null;
    if (!socket) return;
    socket.onclose = null;
    socket.onerror = null;
    socket.onmessage = null;
    try {
      socket.close(code, reason);
    } catch {
      /* ignore */
    }
  }

  private handleAck(ack: AckPayload): void {
    const pending = this.pending.get(ack.commandId);
    if (!pending || pending.settled) return;
    pending.settled = true;
    this.pending.delete(ack.commandId);
    this.outbox.delete(ack.commandId);
    pending.resolve(ack);
  }

  private handleError(err: ErrorPayload): void {
    if (TERMINAL_ERROR_CODES.has(err.code) && !err.commandId) {
      this.error = { code: err.code, message: err.message };
      this.closedByUser = true;
      this.detachAndClose(1008, err.code);
      this.setPhase('unauthorized');
      return;
    }
    if (!err.commandId) {
      this.error = { code: err.code, message: err.message };
      this.notify();
      return;
    }
    const pending = this.pending.get(err.commandId);
    if (!pending || pending.settled) return; // late error must not reject a settled promise
    pending.settled = true;
    this.pending.delete(err.commandId);
    this.outbox.delete(err.commandId);
    pending.reject(new Error(`${err.code}: ${err.message}`));
  }

  private scheduleReconnect(): void {
    if (this.closedByUser) return;
    const delay = this.computeBackoff(this.retryAttempt);
    this.retryAttempt += 1;
    this.retryInMs = delay;
    this.setPhase('reconnecting');
    if (this.reconnectTimer) this.clearTimer(this.reconnectTimer);
    this.reconnectTimer = this.setTimer(() => {
      this.reconnectTimer = null;
      this.retryInMs = null;
      if (!this.closedByUser) this.openNormalSocket();
    }, delay);
  }

  private computeBackoff(attempt: number): number {
    const raw = Math.min(BACKOFF_MAX_MS, BACKOFF_BASE_MS * Math.pow(BACKOFF_FACTOR, attempt));
    const jitter = 1 + (this.random() * 2 - 1) * BACKOFF_JITTER;
    return Math.max(0, Math.round(raw * jitter));
  }

  // --- helpers ---

  private decode(data: unknown): Envelope | null {
    let value = data;
    if (typeof value === 'string') {
      try {
        value = JSON.parse(value);
      } catch {
        return null;
      }
    } else if (typeof Blob !== 'undefined' && value instanceof Blob) {
      return null; // binary payloads are not part of the protocol
    }
    return parseEnvelope(value);
  }

  private sendEnvelope(socket: SocketLike, env: Envelope): void {
    try {
      socket.send(JSON.stringify(env));
    } catch {
      /* write failures surface via close */
    }
  }

  private setPhase(phase: ConnPhase): void {
    this.phase = phase;
    this.notify();
  }

  private buildState(): ClientState {
    const syncing =
      this.session.tipSeq > this.session.lastSeq
        ? {
            applied: Math.max(0, this.session.lastSeq - this.syncBase),
            total: Math.max(0, this.session.tipSeq - this.syncBase),
          }
        : null;
    return {
      phase: this.phase,
      session: this.session,
      myDeviceId: this.identity?.deviceId ?? null,
      retryAttempt: this.retryAttempt,
      retryInMs: this.retryInMs,
      syncing,
      error: this.error,
    };
  }

  private notify(): void {
    this.current = this.buildState();
    for (const listener of this.listeners) listener();
  }
}
