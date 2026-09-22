import { describe, expect, it } from 'vitest';
import {
  applyEvent,
  applySnapshot,
  displayName,
  MAX_NOTICES,
  newSessionState,
  textByteLength,
  type SessionState,
} from '../src/reducer.js';
import type { EventKind, EventPayload } from '../src/types.js';

function ev(seq: number, kind: EventKind, payload?: unknown, ts = seq): EventPayload {
  return { seq, ts, kind, payload };
}

function seedDevices(state: SessionState): SessionState {
  return applySnapshot(state, {
    sessionId: 's1',
    status: 'idle',
    lastSeq: 0,
    devices: [
      { deviceId: 'd1', name: 'Pixel 8' },
      { deviceId: 'd2', name: 'PC-B-Chrome' },
    ],
  });
}

describe('reducer', () => {
  it('coalesces text deltas into a single assistant item', () => {
    let s = newSessionState();
    s = applyEvent(s, ev(1, 'text_delta', { content: 'Hel' }));
    s = applyEvent(s, ev(2, 'text_delta', { content: 'lo' }));
    expect(s.items).toHaveLength(1);
    expect(s.items[0]).toMatchObject({ kind: 'assistant', text: 'Hello', sealed: false });
  });

  it('seals an open assistant on turn_complete and starts a new one after', () => {
    let s = newSessionState();
    s = applyEvent(s, ev(1, 'text_delta', { content: 'one' }));
    s = applyEvent(s, ev(2, 'turn_complete'));
    s = applyEvent(s, ev(3, 'text_delta', { content: 'two' }));
    expect(s.items).toHaveLength(2);
    expect(s.items[0]).toMatchObject({ kind: 'assistant', text: 'one', sealed: true });
    expect(s.items[1]).toMatchObject({ kind: 'assistant', text: 'two', sealed: false });
    expect(s.turnActive).toBe(false);
  });

  it('user_prompt adds an item and seals the open assistant', () => {
    let s = seedDevices(newSessionState());
    s = applyEvent(s, ev(1, 'text_delta', { content: 'working' }));
    s = applyEvent(s, ev(2, 'user_prompt', { text: 'do it', byDevice: 'd1' }));
    expect(s.items[0]).toMatchObject({ kind: 'assistant', sealed: true });
    expect(s.items[1]).toMatchObject({ kind: 'prompt', text: 'do it', byDevice: 'd1' });
    expect(s.turnActive).toBe(true);
  });

  it('matches tool_call_end LIFO on the last unfinished tool of the same name', () => {
    let s = newSessionState();
    s = applyEvent(s, ev(1, 'tool_call_start', { name: 'bash' }));
    s = applyEvent(s, ev(2, 'tool_call_start', { name: 'bash' }));
    s = applyEvent(s, ev(3, 'tool_call_end', { name: 'bash', output: 'first-out' }));
    expect(s.items[0]).toMatchObject({ done: false });
    expect(s.items[1]).toMatchObject({ done: true, output: 'first-out' });
    s = applyEvent(s, ev(4, 'tool_call_end', { name: 'bash', output: 'second-out' }));
    expect(s.items[0]).toMatchObject({ done: true, output: 'second-out' });
  });

  it('permission request -> resolved removes pending and greys with attribution', () => {
    let s = seedDevices(newSessionState());
    s = applyEvent(s, ev(1, 'permission_request', { requestId: 'r1', kind: 'bash', summary: 'git push' }));
    expect(s.pendingPermissions).toHaveLength(1);
    expect(s.items[0]).toMatchObject({ kind: 'permission', requestId: 'r1' });
    expect((s.items[0] as { resolved?: unknown }).resolved).toBeUndefined();

    s = applyEvent(s, ev(2, 'permission_resolved', { requestId: 'r1', allow: true, byDevice: 'd2' }));
    expect(s.pendingPermissions).toHaveLength(0);
    expect(s.items[0]).toMatchObject({
      kind: 'permission',
      resolved: { allow: true, byDevice: 'd2', byName: 'PC-B-Chrome' },
    });
  });

  it('question request -> resolved removes pending and records the answer', () => {
    let s = seedDevices(newSessionState());
    s = applyEvent(s, ev(1, 'question', { questionId: 'q1', text: 'Which branch?' }));
    expect(s.pendingQuestions).toHaveLength(1);
    s = applyEvent(s, ev(2, 'question_resolved', { questionId: 'q1', text: 'main', byDevice: 'd1' }));
    expect(s.pendingQuestions).toHaveLength(0);
    expect(s.items[0]).toMatchObject({
      kind: 'question',
      resolved: { text: 'main', byDevice: 'd1', byName: 'Pixel 8' },
    });
  });

  it('tracks mode, status, usage, and turn activity', () => {
    let s = newSessionState();
    s = applyEvent(s, ev(1, 'mode_changed', { mode: 'plan', byDevice: 'd1' }));
    s = applyEvent(s, ev(2, 'status_change', { status: 'running' }));
    s = applyEvent(s, ev(3, 'usage_update', { inputTokens: 10, outputTokens: 20 }));
    expect(s.mode).toBe('plan');
    expect(s.status).toBe('running');
    expect(s.turnActive).toBe(true);
    expect(s.usage).toEqual({ inputTokens: 10, outputTokens: 20 });
  });

  it('caps notices at MAX_NOTICES', () => {
    let s = newSessionState();
    for (let i = 1; i <= MAX_NOTICES + 10; i++) {
      s = applyEvent(s, ev(i, 'error', { message: `e${i}` }));
    }
    const notices = s.items.filter((i) => i.kind === 'notice');
    expect(notices).toHaveLength(MAX_NOTICES);
    expect((notices[0] as { text: string }).text).toBe('e11');
  });

  it('clamps assistant text at 256 KB, keeping the tail', () => {
    let s = newSessionState();
    const chunk = 'a'.repeat(64 * 1024);
    for (let i = 1; i <= 6; i++) s = applyEvent(s, ev(i, 'text_delta', { content: chunk }));
    const assistant = s.items[0] as { text: string };
    expect(textByteLength(assistant.text)).toBeLessThanOrEqual(256 * 1024);
  });

  it('snapshot with a new sessionId resets items and lastSeq', () => {
    let s = seedDevices(newSessionState());
    s = applyEvent(s, ev(1, 'text_delta', { content: 'old session' }));
    expect(s.lastSeq).toBe(1);

    s = applySnapshot(s, { sessionId: 's2', status: 'idle', lastSeq: 7, devices: [] });
    expect(s.sessionId).toBe('s2');
    expect(s.items).toHaveLength(0);
    expect(s.lastSeq).toBe(0);
    expect(s.tipSeq).toBe(7);
  });

  it('snapshot with the same sessionId preserves items and lastSeq', () => {
    let s = seedDevices(newSessionState());
    s = applyEvent(s, ev(1, 'text_delta', { content: 'kept' }));
    s = applySnapshot(s, { sessionId: 's1', status: 'running', lastSeq: 5, devices: [] });
    expect(s.items).toHaveLength(1);
    expect(s.lastSeq).toBe(1);
    expect(s.tipSeq).toBe(5);
    expect(s.status).toBe('running');
  });

  it('renders the local device as "you"', () => {
    const s = seedDevices(newSessionState());
    expect(displayName(s, 'd1', 'd1')).toBe('you');
    expect(displayName(s, 'd2', 'd1')).toBe('PC-B-Chrome');
    expect(displayName(s, 'unknown', 'd1')).toBe('unknown');
  });

  it('is pure: applying the event log split equals applying it whole', () => {
    const events: EventPayload[] = [
      ev(1, 'status_change', { status: 'running' }),
      ev(2, 'user_prompt', { text: 'hi', byDevice: 'd1' }),
      ev(3, 'text_delta', { content: 'work' }),
      ev(4, 'tool_call_start', { name: 'bash', input: 'ls' }),
      ev(5, 'tool_call_end', { name: 'bash', output: 'ok' }),
      ev(6, 'permission_request', { requestId: 'r1', kind: 'bash', summary: 'rm -rf' }),
      ev(7, 'permission_resolved', { requestId: 'r1', allow: false, byDevice: 'd2' }),
      ev(8, 'text_delta', { content: 'more' }),
      ev(9, 'turn_complete', {}),
      ev(10, 'status_change', { status: 'idle' }),
    ];
    const base = seedDevices(newSessionState());
    const whole = events.reduce((acc, e) => applyEvent(acc, e), base);
    const split = events.slice(0, 4).reduce((acc, e) => applyEvent(acc, e), base);
    const rest = events.slice(4).reduce((acc, e) => applyEvent(acc, e), split);
    expect(rest).toEqual(whole);
  });
});
