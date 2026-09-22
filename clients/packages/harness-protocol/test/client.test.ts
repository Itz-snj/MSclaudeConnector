import { describe, expect, it } from 'vitest';
import {
  HarnessClient,
  type HarnessIdentity,
  type HarnessClientOptions,
  type SocketLike,
} from '../src/client.js';
import type { Envelope } from '../src/types.js';

class FakeSocket implements SocketLike {
  readyState = 0;
  sent: string[] = [];
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code?: number; reason?: string }) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;

  constructor(
    public url: string,
    public headers: Record<string, string>,
  ) {}

  open(): void {
    this.readyState = 1;
    this.onopen?.({});
  }

  emit(env: Envelope): void {
    this.onmessage?.({ data: JSON.stringify(env) });
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(code = 1000, reason = ''): void {
    this.readyState = 3;
    this.onclose?.({ code, reason });
  }

  envelopes(): Envelope[] {
    return this.sent.map((s) => JSON.parse(s) as Envelope);
  }

  commandEnvelopes(): Envelope[] {
    return this.envelopes().filter((e) => e.type === 'command');
  }
}

interface Harness {
  client: HarnessClient;
  sockets: FakeSocket[];
  timers: { fn: () => void; ms: number }[];
  runNext: () => void;
  runAll: () => void;
}

function makeHarness(overrides: Partial<HarnessClientOptions> = {}): Harness {
  const sockets: FakeSocket[] = [];
  const timers: { fn: () => void; ms: number }[] = [];
  const client = new HarnessClient({
    url: 'wss://host:7432/ws',
    createSocket: (url, headers) => {
      const socket = new FakeSocket(url, headers);
      sockets.push(socket);
      return socket;
    },
    setTimer: (fn, ms) => {
      const handle = { fn, ms };
      timers.push(handle);
      return handle as unknown as ReturnType<typeof setTimeout>;
    },
    clearTimer: (handle) => {
      const idx = timers.indexOf(handle as unknown as { fn: () => void; ms: number });
      if (idx >= 0) timers.splice(idx, 1);
    },
    random: () => 0.5, // zero jitter
    ...overrides,
  });
  return {
    client,
    sockets,
    timers,
    runNext: () => {
      const next = timers.shift();
      next?.fn();
    },
    runAll: () => {
      while (timers.length > 0) timers.shift()?.fn();
    },
  };
}

function identity(overrides: Partial<HarnessIdentity> = {}): HarnessIdentity {
  return { deviceId: 'd1', credential: 'cred-1', url: 'wss://host:7432/ws', ...overrides };
}

function snapshot(sessionId = 's1', lastSeq = 0, status = 'idle'): Envelope {
  return { v: 1, type: 'snapshot', snapshot: { sessionId, status, lastSeq } };
}

function event(seq: number, kind: string, payload: unknown = {}): Envelope {
  return { v: 1, type: 'event', event: { seq, ts: seq, kind, payload } as never };
}

function goLive(h: Harness, tip = 0, sessionId = 's1'): FakeSocket {
  const socket = h.sockets[h.sockets.length - 1];
  socket.open();
  socket.emit(snapshot(sessionId, tip));
  return socket;
}

describe('HarnessClient auth', () => {
  it('sends the credential in the hello body in hello mode', () => {
    const h = makeHarness({ identity: identity() });
    h.client.connect();
    const socket = h.sockets[0];
    socket.open();
    const hello = socket.envelopes()[0];
    expect(hello.type).toBe('hello');
    expect(hello.hello?.credential).toBe('cred-1');
    expect(socket.headers.Authorization).toBeUndefined();
  });

  it('sends the credential as a transport header in header mode', () => {
    const h = makeHarness({ authMode: 'header', identity: identity() });
    h.client.connect();
    const socket = h.sockets[0];
    socket.open();
    expect(socket.headers.Authorization).toBe('Bearer cred-1');
    const hello = socket.envelopes()[0];
    expect(hello.hello?.credential).toBeUndefined();
  });
});

describe('HarnessClient sequencing', () => {
  it('applies contiguous events and reports live once at the tip', () => {
    const h = makeHarness({ identity: identity() });
    h.client.connect();
    const socket = h.sockets[0];
    socket.open();
    socket.emit(snapshot('s1', 2));
    expect(h.client.getState().phase).toBe('syncing');
    socket.emit(event(1, 'text_delta', { content: 'a' }));
    socket.emit(event(2, 'text_delta', { content: 'b' }));
    expect(h.client.getState().phase).toBe('live');
    expect(h.client.getState().session.lastSeq).toBe(2);
  });

  it('reconnects from the last contiguous seq after a gap', () => {
    const h = makeHarness({ identity: identity() });
    h.client.connect();
    const s0 = goLive(h, 1);
    s0.emit(event(1, 'text_delta', { content: 'a' }));
    s0.emit(event(5, 'text_delta', { content: 'gap' }));

    expect(h.client.getState().phase).toBe('reconnecting');
    expect(h.client.getState().session.lastSeq).toBe(1);

    h.runNext();
    const s1 = h.sockets[1];
    s1.open();
    expect(s1.envelopes()[0].hello?.lastSeq).toBe(1);
  });

  it('full-resyncs (lastSeq 0) after three consecutive gaps at the same seq', () => {
    const h = makeHarness({ identity: identity() });
    h.client.connect();
    for (let i = 0; i < 3; i++) {
      const socket = h.sockets[h.sockets.length - 1];
      socket.open();
      socket.emit(snapshot('s1', 5));
      socket.emit(event(5, 'text_delta', { content: 'gap' }));
      h.runNext();
    }
    const last = h.sockets[h.sockets.length - 1];
    last.open();
    expect(last.envelopes()[0].hello?.lastSeq).toBe(0);
  });
});

describe('HarnessClient errors', () => {
  it('treats auth errors as terminal and does not reconnect', () => {
    const h = makeHarness({ identity: identity() });
    h.client.connect();
    const socket = h.sockets[0];
    socket.open();
    socket.emit({ v: 1, type: 'error', error: { code: 'pairing_denied', message: 'no' } });
    expect(h.client.getState().phase).toBe('unauthorized');
    expect(h.timers).toHaveLength(0);
  });

  it('reconnects on an abnormal close', () => {
    const h = makeHarness({ identity: identity() });
    h.client.connect();
    const socket = h.sockets[0];
    socket.open();
    socket.emit(snapshot('s1', 0));
    socket.onclose?.({ code: 1006, reason: 'dropped' });
    expect(h.client.getState().phase).toBe('reconnecting');
    expect(h.timers).toHaveLength(1);
  });

  it('does not reject a settled promise on a late agent_error', async () => {
    const h = makeHarness({ identity: identity() });
    h.client.connect();
    const socket = goLive(h, 0);

    const promise = h.client.sendPrompt('hi');
    const command = socket.commandEnvelopes()[0];
    const id = command.command?.id ?? '';
    socket.emit({ v: 1, type: 'ack', ack: { commandId: id, seq: 3 } });
    await expect(promise).resolves.toMatchObject({ seq: 3 });

    expect(() =>
      socket.emit({ v: 1, type: 'error', error: { commandId: id, code: 'agent_error', message: 'late' } }),
    ).not.toThrow();
  });

  it('resolves acks with or without a seq (idempotent replay)', async () => {
    const h = makeHarness({ identity: identity() });
    h.client.connect();
    const socket = goLive(h, 0);

    const p1 = h.client.sendPrompt('one');
    const c1 = socket.commandEnvelopes()[0];
    const id1 = c1.command?.id ?? '';
    socket.emit({ v: 1, type: 'ack', ack: { commandId: id1, seq: 1 } });
    await expect(p1).resolves.toMatchObject({ seq: 1 });

    const p2 = h.client.sendPrompt('two');
    const commands = socket.commandEnvelopes();
    const c2 = commands[commands.length - 1];
    const id2 = c2.command?.id ?? '';
    socket.emit({ v: 1, type: 'ack', ack: { commandId: id2 } });
    await expect(p2).resolves.toMatchObject({ commandId: id2 });
  });
});

describe('HarnessClient outbox', () => {
  it('flushes queued commands with the same idempotencyKey after reconnect', async () => {
    const h = makeHarness({ identity: identity() });
    h.client.connect();
    const s0 = goLive(h, 0);

    const promise = h.client.sendPrompt('offline then live');
    const c0 = s0.commandEnvelopes()[0];
    const key = c0.command?.idempotencyKey;
    expect(key).toBeTruthy();
    expect(c0.command?.id).toBe(key);

    // Drop and reconnect before the ack arrives.
    s0.onclose?.({ code: 1006, reason: 'dropped' });
    h.runNext();
    const s1 = h.sockets[1];
    s1.open();
    s1.emit(snapshot('s1', 0));

    const resent = s1.commandEnvelopes().find((c) => c.command?.idempotencyKey === key);
    expect(resent).toBeDefined();
    expect(resent?.command?.id).toBe(key);

    const commandId = c0.command?.id ?? '';
    s1.emit({ v: 1, type: 'ack', ack: { commandId, seq: 1 } });
    await expect(promise).resolves.toMatchObject({ seq: 1 });
  });
});

describe('HarnessClient pairing', () => {
  it('pairs, then opens an authenticated connection', async () => {
    const h = makeHarness();
    const promise = h.client.pair({ token: 'tok-1234', spki: 'abc' });
    const pairSocket = h.sockets[0];
    pairSocket.open();
    expect(pairSocket.envelopes()[0].hello?.pairingToken).toBe('tok-1234');

    pairSocket.emit({
      v: 1,
      type: 'paired',
      paired: { deviceId: 'new-device', name: 'Pixel 8', credential: 'issued' },
    });
    const issued = await promise;
    expect(issued.deviceId).toBe('new-device');
    expect(issued.spki).toBe('abc');

    expect(h.sockets).toHaveLength(2);
    const normal = h.sockets[1];
    normal.open();
    const hello = normal.envelopes()[0];
    expect(hello.hello?.credential).toBe('issued');
    expect(hello.hello?.deviceId).toBe('new-device');
  });

  it('rejects when pairing is denied', async () => {
    const h = makeHarness();
    const promise = h.client.pair({ token: 'tok' });
    const pairSocket = h.sockets[0];
    pairSocket.open();
    pairSocket.emit({ v: 1, type: 'error', error: { code: 'pairing_denied', message: 'no' } });
    await expect(promise).rejects.toThrow('pairing_denied');
  });
});
