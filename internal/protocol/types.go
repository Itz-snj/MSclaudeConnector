// Package protocol defines the versioned JSON message envelope used between
// host and clients over WebSocket. Types here are the Go source of truth;
// the React web UI and Expo app will mirror them in TypeScript.
package protocol

import "encoding/json"

const Version = 1

// Directional roles.
type MessageType string

const (
	MsgHello              MessageType = "hello"
	MsgSnapshot           MessageType = "snapshot"
	MsgEvent              MessageType = "event"
	MsgCommand            MessageType = "command"
	MsgAck                MessageType = "ack"
	MsgError              MessageType = "error"
	MsgPaired             MessageType = "paired"
	MsgPermissionResolved MessageType = "permission_resolved"
)

// Envelope is the top-level wrapper for every message.
type Envelope struct {
	Version int         `json:"v"`
	Type    MessageType `json:"type"`
	// Exactly one of the following should be set depending on Type.
	Hello    *HelloPayload    `json:"hello,omitempty"`
	Snapshot *SnapshotPayload `json:"snapshot,omitempty"`
	Event    *EventPayload    `json:"event,omitempty"`
	Command  *CommandPayload  `json:"command,omitempty"`
	Ack      *AckPayload      `json:"ack,omitempty"`
	Error    *ErrorPayload    `json:"error,omitempty"`
	Paired   *PairedPayload   `json:"paired,omitempty"`
}

type HelloPayload struct {
	DeviceID     string `json:"deviceId"`
	LastSeq      int64  `json:"lastSeq"`
	PairingToken string `json:"pairingToken,omitempty"`
}

type SnapshotPayload struct {
	SessionID         string            `json:"sessionId"`
	Status            string            `json:"status"`
	LastSeq           int64             `json:"lastSeq"`
	PendingPermissions []PermissionView `json:"pendingPermissions,omitempty"`
	RecentEvents      []EventPayload   `json:"recentEvents,omitempty"` // optional: last N for context
}

type PermissionView struct {
	RequestID string `json:"requestId"`
	Kind      string `json:"kind"`
	Summary   string `json:"summary"`
}

// EventKind values are produced by the AgentAdapter and stored in the event log.
type EventKind string

const (
	EventTextDelta         EventKind = "text_delta"
	EventToolCallStart     EventKind = "tool_call_start"
	EventToolCallEnd       EventKind = "tool_call_end"
	EventPermissionRequest EventKind = "permission_request"
	EventQuestion          EventKind = "question"
	EventPermissionResolved EventKind = "permission_resolved"
	EventTurnComplete      EventKind = "turn_complete"
	EventStatusChange      EventKind = "status_change"
	EventUsageUpdate       EventKind = "usage_update"
	EventError             EventKind = "error"
	EventUserPrompt        EventKind = "user_prompt"
)

type EventPayload struct {
	Seq     int64           `json:"seq"`
	TS      int64           `json:"ts"` // Unix milliseconds
	Kind    EventKind       `json:"kind"`
	Payload json.RawMessage `json:"payload"` // kind-specific JSON object
}

// CommandKind values are sent by clients to mutate session state.
type CommandKind string

const (
	CmdSendPrompt       CommandKind = "send_prompt"
	CmdAnswerPermission CommandKind = "answer_permission"
	CmdAnswerQuestion   CommandKind = "answer_question"
	CmdInterrupt        CommandKind = "interrupt"
	CmdSetMode          CommandKind = "set_mode"
)

type CommandPayload struct {
	ID             string          `json:"id"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Kind           CommandKind     `json:"kind"`
	Payload        json.RawMessage `json:"payload"`
}

type PairedPayload struct {
	DeviceID   string `json:"deviceId"`
	Name       string `json:"name"`
	Credential string `json:"credential"`
}

type AckPayload struct {
	CommandID string `json:"commandId"`
	Seq       int64  `json:"seq,omitempty"`
}

type ErrorPayload struct {
	CommandID string `json:"commandId,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// --- Command payload bodies ---

type SendPromptBody struct {
	Text string `json:"text"`
}

type AnswerPermissionBody struct {
	RequestID string `json:"requestId"`
	Allow     bool   `json:"allow"`
	AllowAlways bool `json:"allowAlways,omitempty"`
}

type AnswerQuestionBody struct {
	QuestionID string `json:"questionId"`
	Text       string `json:"text"`
}

// --- Event payload bodies ---

type TextDeltaBody struct {
	Content string `json:"content"`
}

type ToolCallBody struct {
	Name      string `json:"name"`
	Input     string `json:"input,omitempty"`
	Output    string `json:"output,omitempty"`
}

type PermissionRequestBody struct {
	RequestID string `json:"requestId"`
	Kind      string `json:"kind"`    // e.g. "bash", "edit"
	Summary   string `json:"summary"` // human-readable, untrusted
}

type QuestionBody struct {
	QuestionID string `json:"questionId"`
	Text       string `json:"text"`
}

type PermissionResolvedBody struct {
	RequestID string `json:"requestId"`
	Allow     bool   `json:"allow"`
	ByDevice  string `json:"byDevice"`
}

type StatusChangeBody struct {
	Status string `json:"status"`
}

type UsageUpdateBody struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

type UserPromptBody struct {
	Text     string `json:"text"`
	ByDevice string `json:"byDevice"`
}
