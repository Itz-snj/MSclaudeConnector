package protocol

import (
	"reflect"
	"sort"
	"testing"
)

// goldenJSONFields pins every wire struct's field -> json tag mapping. When the
// Go source of truth changes, the TypeScript mirror in clients/packages/
// harness-protocol must change in lockstep; this test fails loudly if not.
var goldenJSONFields = map[string]map[string]string{
	"Envelope": {
		"Version":  "v",
		"Type":     "type",
		"Hello":    "hello,omitempty",
		"Snapshot": "snapshot,omitempty",
		"Event":    "event,omitempty",
		"Command":  "command,omitempty",
		"Ack":      "ack,omitempty",
		"Error":    "error,omitempty",
		"Paired":   "paired,omitempty",
	},
	"HelloPayload": {
		"DeviceID":     "deviceId",
		"LastSeq":      "lastSeq",
		"PairingToken": "pairingToken,omitempty",
		"Credential":   "credential,omitempty",
	},
	"SnapshotPayload": {
		"SessionID":          "sessionId",
		"Status":             "status",
		"Mode":               "mode,omitempty",
		"LastSeq":            "lastSeq",
		"PendingPermissions": "pendingPermissions,omitempty",
		"PendingQuestions":   "pendingQuestions,omitempty",
		"Devices":            "devices,omitempty",
		"RecentEvents":       "recentEvents,omitempty",
	},
	"DeviceView": {
		"DeviceID": "deviceId",
		"Name":     "name",
	},
	"PermissionView": {
		"RequestID": "requestId",
		"Kind":      "kind",
		"Summary":   "summary",
	},
	"QuestionView": {
		"QuestionID": "questionId",
		"Text":       "text",
	},
	"EventPayload": {
		"Seq":     "seq",
		"TS":      "ts",
		"Kind":    "kind",
		"Payload": "payload",
	},
	"CommandPayload": {
		"ID":             "id",
		"IdempotencyKey": "idempotencyKey",
		"Kind":           "kind",
		"Payload":        "payload",
	},
	"PairedPayload": {
		"DeviceID":   "deviceId",
		"Name":       "name",
		"Credential": "credential",
	},
	"AckPayload": {
		"CommandID": "commandId",
		"Seq":       "seq,omitempty",
	},
	"ErrorPayload": {
		"CommandID": "commandId,omitempty",
		"Code":      "code",
		"Message":   "message",
	},
	"SendPromptBody": {
		"Text": "text",
	},
	"AnswerPermissionBody": {
		"RequestID":   "requestId",
		"Allow":       "allow",
		"AllowAlways": "allowAlways,omitempty",
	},
	"AnswerQuestionBody": {
		"QuestionID": "questionId",
		"Text":       "text",
	},
	"SetModeBody": {
		"Mode": "mode",
	},
	"TextDeltaBody": {
		"Content": "content",
	},
	"ToolCallBody": {
		"Name":   "name",
		"Input":  "input,omitempty",
		"Output": "output,omitempty",
	},
	"PermissionRequestBody": {
		"RequestID": "requestId",
		"Kind":      "kind",
		"Summary":   "summary",
	},
	"QuestionBody": {
		"QuestionID": "questionId",
		"Text":       "text",
	},
	"PermissionResolvedBody": {
		"RequestID": "requestId",
		"Allow":     "allow",
		"ByDevice":  "byDevice",
	},
	"QuestionResolvedBody": {
		"QuestionID": "questionId",
		"Text":       "text",
		"ByDevice":   "byDevice",
	},
	"ModeChangedBody": {
		"Mode":     "mode",
		"ByDevice": "byDevice",
	},
	"StatusChangeBody": {
		"Status": "status",
	},
	"UsageUpdateBody": {
		"InputTokens":  "inputTokens",
		"OutputTokens": "outputTokens",
	},
	"UserPromptBody": {
		"Text":     "text",
		"ByDevice": "byDevice",
	},
}

var wireStructs = []interface{}{
	Envelope{},
	HelloPayload{},
	SnapshotPayload{},
	DeviceView{},
	PermissionView{},
	QuestionView{},
	EventPayload{},
	CommandPayload{},
	PairedPayload{},
	AckPayload{},
	ErrorPayload{},
	SendPromptBody{},
	AnswerPermissionBody{},
	AnswerQuestionBody{},
	SetModeBody{},
	TextDeltaBody{},
	ToolCallBody{},
	PermissionRequestBody{},
	QuestionBody{},
	PermissionResolvedBody{},
	QuestionResolvedBody{},
	ModeChangedBody{},
	StatusChangeBody{},
	UsageUpdateBody{},
	UserPromptBody{},
}

func TestWireStructJSONTags(t *testing.T) {
	for _, s := range wireStructs {
		typ := reflect.TypeOf(s)
		name := typ.Name()
		want, ok := goldenJSONFields[name]
		if !ok {
			t.Fatalf("struct %s has no golden entry; add it to drift_test.go", name)
		}
		got := map[string]string{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			got[f.Name] = f.Tag.Get("json")
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d fields, want %d (got %v)", name, len(got), len(want), got)
		}
		for field, tag := range want {
			if got[field] != tag {
				t.Fatalf("%s.%s: json tag %q, want %q", name, field, got[field], tag)
			}
		}
	}
}

func TestGoldenCoversAllStructs(t *testing.T) {
	if len(goldenJSONFields) != len(wireStructs) {
		t.Fatalf("golden has %d structs but %d are checked", len(goldenJSONFields), len(wireStructs))
	}
}

func TestEventKindGolden(t *testing.T) {
	got := []string{
		string(EventTextDelta), string(EventToolCallStart), string(EventToolCallEnd),
		string(EventPermissionRequest), string(EventQuestion), string(EventPermissionResolved),
		string(EventQuestionResolved), string(EventModeChanged), string(EventTurnComplete),
		string(EventStatusChange), string(EventUsageUpdate), string(EventError), string(EventUserPrompt),
	}
	want := []string{
		"text_delta", "tool_call_start", "tool_call_end", "permission_request", "question",
		"permission_resolved", "question_resolved", "mode_changed", "turn_complete",
		"status_change", "usage_update", "error", "user_prompt",
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event kinds drifted:\n got %v\nwant %v", got, want)
	}
}

func TestCommandKindGolden(t *testing.T) {
	got := []string{
		string(CmdSendPrompt), string(CmdAnswerPermission), string(CmdAnswerQuestion),
		string(CmdInterrupt), string(CmdSetMode),
	}
	want := []string{"send_prompt", "answer_permission", "answer_question", "interrupt", "set_mode"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command kinds drifted:\n got %v\nwant %v", got, want)
	}
}
