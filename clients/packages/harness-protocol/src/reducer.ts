import {
  asArray,
  asBool,
  asNumber,
  asString,
  isRecord,
  type DeviceView,
  type EventPayload,
  type PermissionView,
  type QuestionView,
  type SnapshotPayload,
  type UsageUpdateBody,
} from './types.js';

export const MAX_TEXT_BYTES = 256 * 1024;
export const MAX_NOTICES = 50;

export interface PromptItem {
  id: string;
  kind: 'prompt';
  seq: number;
  ts: number;
  text: string;
  byDevice: string;
}

export interface AssistantItem {
  id: string;
  kind: 'assistant';
  seq: number;
  ts: number;
  text: string;
  sealed: boolean;
}

export interface ToolItem {
  id: string;
  kind: 'tool';
  seq: number;
  ts: number;
  name: string;
  input?: string;
  output?: string;
  done: boolean;
}

export interface Resolution {
  allow: boolean;
  byDevice: string;
  byName: string;
}

export interface PermissionItem {
  id: string;
  kind: 'permission';
  seq: number;
  ts: number;
  requestId: string;
  permissionKind: string;
  summary: string;
  resolved?: Resolution;
}

export interface QuestionItem {
  id: string;
  kind: 'question';
  seq: number;
  ts: number;
  questionId: string;
  text: string;
  resolved?: { text: string; byDevice: string; byName: string };
}

export interface NoticeItem {
  id: string;
  kind: 'notice';
  seq: number;
  ts: number;
  text: string;
  level: 'info' | 'error';
}

export type Item =
  | PromptItem
  | AssistantItem
  | ToolItem
  | PermissionItem
  | QuestionItem
  | NoticeItem;

export interface SessionState {
  sessionId: string | null;
  status: string;
  mode: string;
  /** Server tip at snapshot time. */
  tipSeq: number;
  /** Last contiguously applied seq. */
  lastSeq: number;
  items: Item[];
  pendingPermissions: PermissionView[];
  pendingQuestions: QuestionView[];
  devices: DeviceView[];
  deviceNames: Record<string, string>;
  usage: UsageUpdateBody | null;
  turnActive: boolean;
}

export function newSessionState(): SessionState {
  return {
    sessionId: null,
    status: 'idle',
    mode: '',
    tipSeq: 0,
    lastSeq: 0,
    items: [],
    pendingPermissions: [],
    pendingQuestions: [],
    devices: [],
    deviceNames: {},
    usage: null,
    turnActive: false,
  };
}

/** Seed materialized state from a snapshot. Never touches items or lastSeq. */
export function applySnapshot(state: SessionState, snap: SnapshotPayload): SessionState {
  // A different session id means a brand new session; drop accumulated stream.
  const base =
    state.sessionId !== null && snap.sessionId !== state.sessionId ? newSessionState() : state;

  const devices = asArray<DeviceView>(snap.devices).filter(
    (d) => isRecord(d) && typeof d.deviceId === 'string' && typeof d.name === 'string',
  );
  const deviceNames: Record<string, string> = { ...base.deviceNames };
  for (const d of devices) deviceNames[d.deviceId] = d.name;

  return {
    ...base,
    sessionId: snap.sessionId,
    status: asString(snap.status, base.status),
    mode: asString(snap.mode, base.mode),
    tipSeq: asNumber(snap.lastSeq, 0),
    pendingPermissions: dedupeBy(asArray<PermissionView>(snap.pendingPermissions), 'requestId'),
    pendingQuestions: dedupeBy(asArray<QuestionView>(snap.pendingQuestions), 'questionId'),
    devices,
    deviceNames,
  };
}

/** Apply one event. Assumes the caller has already validated seq contiguity. */
export function applyEvent(state: SessionState, ev: EventPayload): SessionState {
  const next: SessionState = { ...state, lastSeq: ev.seq };
  const payload = isRecord(ev.payload) ? ev.payload : {};

  switch (ev.kind) {
    case 'user_prompt': {
      const text = asString(payload.text);
      const byDevice = asString(payload.byDevice);
      next.items = sealOpenAssistant(state.items).concat({
        id: `prompt-${ev.seq}`,
        kind: 'prompt',
        seq: ev.seq,
        ts: ev.ts,
        text,
        byDevice,
      });
      next.turnActive = true;
      break;
    }
    case 'text_delta': {
      const content = asString(payload.content);
      if (!content) {
        next.items = state.items;
        break;
      }
      next.items = appendAssistant(state.items, ev.seq, ev.ts, content);
      break;
    }
    case 'turn_complete': {
      next.items = sealOpenAssistant(state.items);
      next.turnActive = false;
      break;
    }
    case 'status_change': {
      const status = asString(payload.status, state.status);
      next.status = status;
      next.turnActive = status === 'running';
      break;
    }
    case 'mode_changed': {
      next.mode = asString(payload.mode, state.mode);
      break;
    }
    case 'usage_update': {
      next.usage = {
        inputTokens: asNumber(payload.inputTokens),
        outputTokens: asNumber(payload.outputTokens),
      };
      break;
    }
    case 'tool_call_start': {
      const name = asString(payload.name, 'tool');
      next.items = state.items.concat({
        id: `tool-${ev.seq}`,
        kind: 'tool',
        seq: ev.seq,
        ts: ev.ts,
        name,
        input: optionalString(payload.input),
        done: false,
      });
      break;
    }
    case 'tool_call_end': {
      const name = asString(payload.name, 'tool');
      const output = optionalString(payload.output);
      const { items, matched } = finishTool(state.items, name, output);
      next.items = matched
        ? items
        : items.concat({
            id: `tool-${ev.seq}`,
            kind: 'tool',
            seq: ev.seq,
            ts: ev.ts,
            name,
            ...(output !== undefined ? { output } : {}),
            done: true,
          });
      break;
    }
    case 'permission_request': {
      const requestId = asString(payload.requestId);
      const permissionKind = asString(payload.kind, 'permission');
      const summary = asString(payload.summary);
      if (!requestId) {
        next.items = state.items;
        break;
      }
      next.pendingPermissions = dedupeBy(
        [...state.pendingPermissions, { requestId, kind: permissionKind, summary }],
        'requestId',
      );
      const exists = state.items.some((i) => i.kind === 'permission' && i.requestId === requestId);
      next.items = exists
        ? state.items
        : state.items.concat({
            id: `perm-${requestId}`,
            kind: 'permission',
            seq: ev.seq,
            ts: ev.ts,
            requestId,
            permissionKind,
            summary,
          });
      break;
    }
    case 'permission_resolved': {
      const requestId = asString(payload.requestId);
      const resolution: Resolution = {
        allow: asBool(payload.allow),
        byDevice: asString(payload.byDevice),
        byName: '',
      };
      resolution.byName = nameFor(state, resolution.byDevice);
      next.pendingPermissions = state.pendingPermissions.filter(
        (p) => p.requestId !== requestId,
      );
      next.items = state.items.map((item) =>
        item.kind === 'permission' && item.requestId === requestId
          ? { ...item, resolved: resolution }
          : item,
      );
      break;
    }
    case 'question': {
      const questionId = asString(payload.questionId);
      const text = asString(payload.text);
      if (!questionId) {
        next.items = state.items;
        break;
      }
      next.pendingQuestions = dedupeBy(
        [...state.pendingQuestions, { questionId, text }],
        'questionId',
      );
      const exists = state.items.some((i) => i.kind === 'question' && i.questionId === questionId);
      next.items = exists
        ? state.items
        : state.items.concat({
            id: `q-${questionId}`,
            kind: 'question',
            seq: ev.seq,
            ts: ev.ts,
            questionId,
            text,
          });
      break;
    }
    case 'question_resolved': {
      const questionId = asString(payload.questionId);
      const answer = asString(payload.text);
      const byDevice = asString(payload.byDevice);
      next.pendingQuestions = state.pendingQuestions.filter((q) => q.questionId !== questionId);
      next.items = state.items.map((item) =>
        item.kind === 'question' && item.questionId === questionId
          ? {
              ...item,
              resolved: { text: answer, byDevice, byName: nameFor(state, byDevice) },
            }
          : item,
      );
      break;
    }
    case 'error': {
      const message = asString(payload.message, 'agent error');
      next.items = pushNotice(state.items, ev.seq, ev.ts, message, 'error');
      break;
    }
    default: {
      next.items = state.items;
      break;
    }
  }

  return next;
}

/** Head-trim items to a maximum count (mobile memory bound). */
export function trimItems(items: Item[], max: number): Item[] {
  if (items.length <= max) return items;
  return items.slice(items.length - max);
}

/** Human-readable device name; "you" is applied by the UI with myDeviceId. */
export function nameFor(state: SessionState, deviceId: string): string {
  if (!deviceId) return 'unknown';
  return state.deviceNames[deviceId] ?? deviceId.slice(0, 8);
}

export function displayName(state: SessionState, deviceId: string, myDeviceId?: string): string {
  if (myDeviceId && deviceId === myDeviceId) return 'you';
  return nameFor(state, deviceId);
}

export function textByteLength(text: string): number {
  if (typeof TextEncoder !== 'undefined') return new TextEncoder().encode(text).length;
  return text.length;
}

function clampText(text: string): string {
  if (textByteLength(text) <= MAX_TEXT_BYTES) return text;
  // Trim from the front so the most recent output is preserved.
  let out = text;
  while (out.length > 0 && textByteLength(out) > MAX_TEXT_BYTES) {
    out = out.slice(0, Math.floor(out.length * 0.9));
  }
  return out;
}

function appendAssistant(items: Item[], seq: number, ts: number, content: string): Item[] {
  const last = items[items.length - 1];
  if (last && last.kind === 'assistant' && !last.sealed) {
    const updated: AssistantItem = { ...last, text: clampText(last.text + content) };
    return items.slice(0, -1).concat(updated);
  }
  return items.concat({
    id: `assistant-${seq}`,
    kind: 'assistant',
    seq,
    ts,
    text: clampText(content),
    sealed: false,
  });
}

function sealOpenAssistant(items: Item[]): Item[] {
  const last = items[items.length - 1];
  if (last && last.kind === 'assistant' && !last.sealed) {
    return items.slice(0, -1).concat({ ...last, sealed: true });
  }
  return items;
}

function finishTool(
  items: Item[],
  name: string,
  output: string | undefined,
): { items: Item[]; matched: boolean } {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind === 'tool' && !item.done && item.name === name) {
      const updated: ToolItem = {
        ...item,
        done: true,
        ...(output !== undefined ? { output } : {}),
      };
      return { items: items.slice(0, i).concat(updated, items.slice(i + 1)), matched: true };
    }
  }
  return { items, matched: false };
}

function pushNotice(
  items: Item[],
  seq: number,
  ts: number,
  text: string,
  level: 'info' | 'error',
): Item[] {
  const withNotice = items.concat({ id: `notice-${seq}`, kind: 'notice', seq, ts, text, level });
  const noticeIndexes: number[] = [];
  withNotice.forEach((item, idx) => {
    if (item.kind === 'notice') noticeIndexes.push(idx);
  });
  if (noticeIndexes.length <= MAX_NOTICES) return withNotice;
  const drop = new Set(noticeIndexes.slice(0, noticeIndexes.length - MAX_NOTICES));
  return withNotice.filter((_, idx) => !drop.has(idx));
}

function dedupeBy<T>(items: T[], key: keyof T): T[] {
  const seen = new Set<unknown>();
  const out: T[] = [];
  for (const item of items) {
    if (!isRecord(item)) continue;
    const value = item[key as string];
    if (seen.has(value)) continue;
    seen.add(value);
    out.push(item);
  }
  return out;
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined;
}
