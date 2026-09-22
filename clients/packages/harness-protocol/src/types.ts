/**
 * TypeScript mirror of internal/protocol/types.go — the wire source of truth.
 *
 * Wire facts worth remembering:
 *  - the envelope version key is `"v"` (not `"version"`)
 *  - `ts` is Unix milliseconds
 *  - `ack.seq` is absent on an idempotent replay
 *  - `tool_call_*` input/output are strings
 *  - `turn_complete` and `error` event bodies are absent
 *  - `recentEvents` is never populated by the host
 *
 * internal/protocol/drift_test.go pins the Go side; keep this file in lockstep.
 */

export const PROTOCOL_VERSION = 1;

export type MessageType =
  | 'hello'
  | 'snapshot'
  | 'event'
  | 'command'
  | 'ack'
  | 'error'
  | 'paired'
  | 'permission_resolved';

export interface Envelope {
  v: number;
  type: MessageType;
  hello?: HelloPayload;
  snapshot?: SnapshotPayload;
  event?: EventPayload;
  command?: CommandPayload;
  ack?: AckPayload;
  error?: ErrorPayload;
  paired?: PairedPayload;
}

export interface HelloPayload {
  deviceId: string;
  lastSeq: number;
  pairingToken?: string;
  /** Browsers cannot set an Authorization header on a WebSocket handshake. */
  credential?: string;
}

export interface DeviceView {
  deviceId: string;
  name: string;
}

export interface PermissionView {
  requestId: string;
  kind: string;
  summary: string;
}

export interface QuestionView {
  questionId: string;
  text: string;
}

export interface SnapshotPayload {
  sessionId: string;
  status: string;
  mode?: string;
  /** Server tip at snapshot time; NOT the client's last contiguous seq. */
  lastSeq: number;
  pendingPermissions?: PermissionView[];
  pendingQuestions?: QuestionView[];
  devices?: DeviceView[];
  recentEvents?: EventPayload[];
}

export type EventKind =
  | 'text_delta'
  | 'tool_call_start'
  | 'tool_call_end'
  | 'permission_request'
  | 'question'
  | 'permission_resolved'
  | 'question_resolved'
  | 'mode_changed'
  | 'turn_complete'
  | 'status_change'
  | 'usage_update'
  | 'error'
  | 'user_prompt';

export interface EventPayload {
  seq: number;
  /** Unix milliseconds. */
  ts: number;
  kind: EventKind;
  payload?: unknown;
}

export type CommandKind =
  | 'send_prompt'
  | 'answer_permission'
  | 'answer_question'
  | 'interrupt'
  | 'set_mode';

export interface CommandPayload {
  id: string;
  idempotencyKey: string;
  kind: CommandKind;
  payload: unknown;
}

export interface PairedPayload {
  deviceId: string;
  name: string;
  credential: string;
}

export interface AckPayload {
  commandId: string;
  /** Absent on an idempotent replay. */
  seq?: number;
}

export interface ErrorPayload {
  commandId?: string;
  code: string;
  message: string;
}

export interface SendPromptBody {
  text: string;
}

export interface AnswerPermissionBody {
  requestId: string;
  allow: boolean;
  allowAlways?: boolean;
}

export interface AnswerQuestionBody {
  questionId: string;
  text: string;
}

export interface SetModeBody {
  mode: string;
}

export interface TextDeltaBody {
  content: string;
}

export interface ToolCallBody {
  name: string;
  input?: string;
  output?: string;
}

export interface PermissionRequestBody {
  requestId: string;
  kind: string;
  summary: string;
}

export interface QuestionBody {
  questionId: string;
  text: string;
}

export interface PermissionResolvedBody {
  requestId: string;
  allow: boolean;
  byDevice: string;
}

export interface QuestionResolvedBody {
  questionId: string;
  text: string;
  byDevice: string;
}

export interface ModeChangedBody {
  mode: string;
  byDevice: string;
}

export interface StatusChangeBody {
  status: string;
}

export interface UsageUpdateBody {
  inputTokens: number;
  outputTokens: number;
}

export interface UserPromptBody {
  text: string;
  byDevice: string;
}

/** Terminal error codes: reconnecting is pointless until the client re-pairs. */
export const TERMINAL_ERROR_CODES = new Set([
  'auth_required',
  'pairing_denied',
  'device_mismatch',
  'unauthorized',
]);

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function asString(value: unknown, fallback = ''): string {
  return typeof value === 'string' ? value : fallback;
}

export function asNumber(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback;
}

export function asBool(value: unknown, fallback = false): boolean {
  return typeof value === 'boolean' ? value : fallback;
}

export function asArray<T>(value: unknown): T[] {
  return Array.isArray(value) ? (value as T[]) : [];
}

/** Parse an unknown JSON value into an Envelope, defensively. */
export function parseEnvelope(raw: unknown): Envelope | null {
  if (!isRecord(raw)) return null;
  const type = raw.type;
  if (typeof type !== 'string') return null;
  const env: Envelope = { v: asNumber(raw.v, PROTOCOL_VERSION), type: type as MessageType };
  if (isRecord(raw.hello)) env.hello = raw.hello as unknown as HelloPayload;
  if (isRecord(raw.snapshot)) env.snapshot = raw.snapshot as unknown as SnapshotPayload;
  if (isRecord(raw.event)) env.event = raw.event as unknown as EventPayload;
  if (isRecord(raw.command)) env.command = raw.command as unknown as CommandPayload;
  if (isRecord(raw.ack)) env.ack = raw.ack as unknown as AckPayload;
  if (isRecord(raw.error)) env.error = raw.error as unknown as ErrorPayload;
  if (isRecord(raw.paired)) env.paired = raw.paired as unknown as PairedPayload;
  return env;
}
