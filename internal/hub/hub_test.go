package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/Itz-snj/MSclaudeConnector/internal/agent"
	"github.com/Itz-snj/MSclaudeConnector/internal/agent/mock"
	"github.com/Itz-snj/MSclaudeConnector/internal/auth"
	"github.com/Itz-snj/MSclaudeConnector/internal/logger"
	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
	"github.com/Itz-snj/MSclaudeConnector/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func setupHub(t *testing.T) (*Hub, *auth.Auth, *mock.Adapter, context.CancelFunc, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st := newTestStore(t)
	authz := auth.New(st)
	adapter := mock.New()
	if err := adapter.Start(agent.SessionConfig{AgentType: "mock", WorkingDir: "/tmp"}); err != nil {
		t.Fatalf("start adapter: %v", err)
	}
	sess, err := st.CreateSession("mock", "/tmp")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	h := New(Config{SessionID: sess.ID}, logger.New(), st, authz, adapter)
	go h.Run(ctx)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return h, authz, adapter, cancel, wsURL
}

func TestHubPairPromptPermissionResume(t *testing.T) {
	_, authz, _, cancel, wsURL := setupHub(t)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 30*time.Second)
	defer timeout()

	token, err := authz.GeneratePairingToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := authz.ApprovePairingToken(token, "phone"); err != nil {
		t.Fatalf("approve token: %v", err)
	}

	cred, deviceID := pair(t, ctx, wsURL, token)

	conn := dialAuth(t, ctx, wsURL, cred)
	defer conn.Close(websocket.StatusNormalClosure, "")
	sendHello(t, ctx, conn, deviceID, 0)
	snap := readSnapshot(t, ctx, conn)
	if snap.Status != "idle" {
		t.Fatalf("expected idle, got %s", snap.Status)
	}

	sendCommand(t, ctx, conn, "c1", protocol.CmdSendPrompt, protocol.SendPromptBody{Text: "hello"})
	readAck(t, ctx, conn, "c1")
	mustReadEvent(t, ctx, conn, protocol.EventUserPrompt)
	mustReadEvent(t, ctx, conn, protocol.EventStatusChange)
	mustReadEvent(t, ctx, conn, protocol.EventTextDelta)
	mustReadEvent(t, ctx, conn, protocol.EventTurnComplete)
	mustReadEvent(t, ctx, conn, protocol.EventStatusChange)

	sendCommand(t, ctx, conn, "c2", protocol.CmdSendPrompt, protocol.SendPromptBody{Text: "perm"})
	readAck(t, ctx, conn, "c2")
	ev := mustReadEvent(t, ctx, conn, protocol.EventPermissionRequest)
	var permBody protocol.PermissionRequestBody
	_ = json.Unmarshal(ev.Payload, &permBody)
	if permBody.RequestID == "" {
		t.Fatal("expected request id")
	}

	sendCommand(t, ctx, conn, "c3", protocol.CmdAnswerPermission, protocol.AnswerPermissionBody{RequestID: permBody.RequestID, Allow: true})
	readAck(t, ctx, conn, "c3")
	mustReadEvent(t, ctx, conn, protocol.EventPermissionResolved)

	_ = conn.Close(websocket.StatusNormalClosure, "")

	conn2 := dialAuth(t, ctx, wsURL, cred)
	defer conn2.Close(websocket.StatusNormalClosure, "")
	sendHello(t, ctx, conn2, deviceID, 0)
	snap = readSnapshot(t, ctx, conn2)
	if len(snap.PendingPermissions) != 0 {
		t.Fatalf("expected no pending permissions after resume, got %+v", snap.PendingPermissions)
	}
	ev = mustReadEvent(t, ctx, conn2, protocol.EventUserPrompt)
	if ev.Seq != 1 {
		t.Fatalf("expected resumed event seq 1, got %d", ev.Seq)
	}
}

func TestHubFirstWriteWins(t *testing.T) {
	_, authz, _, cancel, wsURL := setupHub(t)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 30*time.Second)
	defer timeout()

	token, _ := authz.GeneratePairingToken()
	authz.ApprovePairingToken(token, "phone")
	cred, deviceID := pair(t, ctx, wsURL, token)

	connA := dialAuth(t, ctx, wsURL, cred)
	defer connA.Close(websocket.StatusNormalClosure, "")
	sendHello(t, ctx, connA, deviceID, 0)
	_ = readSnapshot(t, ctx, connA)

	connB := dialAuth(t, ctx, wsURL, cred)
	defer connB.Close(websocket.StatusNormalClosure, "")
	sendHello(t, ctx, connB, deviceID, 0)
	_ = readSnapshot(t, ctx, connB)

	sendCommand(t, ctx, connA, "c1", protocol.CmdSendPrompt, protocol.SendPromptBody{Text: "perm"})
	readAck(t, ctx, connA, "c1")
	ev := mustReadEvent(t, ctx, connA, protocol.EventPermissionRequest)
	var permBody protocol.PermissionRequestBody
	_ = json.Unmarshal(ev.Payload, &permBody)

	sendCommand(t, ctx, connA, "a1", protocol.CmdAnswerPermission, protocol.AnswerPermissionBody{RequestID: permBody.RequestID, Allow: true})
	readAck(t, ctx, connA, "a1")
	ev = mustReadEvent(t, ctx, connA, protocol.EventPermissionResolved)
	var rb protocol.PermissionResolvedBody
	_ = json.Unmarshal(ev.Payload, &rb)
	if rb.ByDevice != deviceID {
		t.Fatalf("unexpected resolver %s", rb.ByDevice)
	}

	// Late answer from a second device must be rejected.
	sendCommand(t, ctx, connB, "a2", protocol.CmdAnswerPermission, protocol.AnswerPermissionBody{RequestID: permBody.RequestID, Allow: true})
	for {
		errEnv := readEnvelope(t, ctx, connB)
		if errEnv.Type == protocol.MsgError && errEnv.Error != nil && errEnv.Error.Code == "already_resolved" {
			break
		}
	}
}

func TestHubResumeMidTurn(t *testing.T) {
	_, authz, _, cancel, wsURL := setupHub(t)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 30*time.Second)
	defer timeout()

	token, _ := authz.GeneratePairingToken()
	authz.ApprovePairingToken(token, "phone")
	cred, deviceID := pair(t, ctx, wsURL, token)

	conn := dialAuth(t, ctx, wsURL, cred)
	sendHello(t, ctx, conn, deviceID, 0)
	_ = readSnapshot(t, ctx, conn)

	sendCommand(t, ctx, conn, "c1", protocol.CmdSendPrompt, protocol.SendPromptBody{Text: "perm"})
	readAck(t, ctx, conn, "c1")
	mustReadEvent(t, ctx, conn, protocol.EventUserPrompt)
	mustReadEvent(t, ctx, conn, protocol.EventStatusChange)
	mustReadEvent(t, ctx, conn, protocol.EventTextDelta)
	permEv := mustReadEvent(t, ctx, conn, protocol.EventPermissionRequest)

	_ = conn.Close(websocket.StatusNormalClosure, "")

	conn2 := dialAuth(t, ctx, wsURL, cred)
	defer conn2.Close(websocket.StatusNormalClosure, "")
	sendHello(t, ctx, conn2, deviceID, permEv.Seq)
	snap := readSnapshot(t, ctx, conn2)
	if len(snap.PendingPermissions) != 1 {
		t.Fatalf("expected one pending permission after resume, got %+v", snap.PendingPermissions)
	}

	sendCommand(t, ctx, conn2, "c2", protocol.CmdAnswerPermission, protocol.AnswerPermissionBody{RequestID: "r-mock-1", Allow: true})
	readAck(t, ctx, conn2, "c2")
	mustReadEvent(t, ctx, conn2, protocol.EventPermissionResolved)
	mustReadEvent(t, ctx, conn2, protocol.EventTurnComplete)
}

func pair(t *testing.T, ctx context.Context, wsURL, token string) (credential, deviceID string) {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	sendHello(t, ctx, conn, "", 0, token)
	env := readEnvelope(t, ctx, conn)
	if env.Type != protocol.MsgPaired || env.Paired == nil {
		t.Fatalf("expected paired, got %+v", env)
	}
	return env.Paired.Credential, env.Paired.DeviceID
}

func dialAuth(t *testing.T, ctx context.Context, wsURL, cred string) *websocket.Conn {
	t.Helper()
	header := http.Header{"Authorization": []string{"Bearer " + cred}}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial auth: %v", err)
	}
	return conn
}

func sendHello(t *testing.T, ctx context.Context, conn *websocket.Conn, deviceID string, lastSeq int64, pairingToken ...string) {
	t.Helper()
	hello := protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgHello,
		Hello: &protocol.HelloPayload{
			DeviceID: deviceID,
			LastSeq:  lastSeq,
		},
	}
	if len(pairingToken) > 0 {
		hello.Hello.PairingToken = pairingToken[0]
	}
	writeEnvelope(t, ctx, conn, hello)
}

func sendCommand(t *testing.T, ctx context.Context, conn *websocket.Conn, id string, kind protocol.CommandKind, payload interface{}) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgCommand,
		Command: &protocol.CommandPayload{
			ID:             id,
			IdempotencyKey: id,
			Kind:           kind,
			Payload:        raw,
		},
	}
	writeEnvelope(t, ctx, conn, env)
}

func writeEnvelope(t *testing.T, ctx context.Context, conn *websocket.Conn, env protocol.Envelope) {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readEnvelope(t *testing.T, ctx context.Context, conn *websocket.Conn) protocol.Envelope {
	t.Helper()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatal("expected text message")
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return env
}

func readSnapshot(t *testing.T, ctx context.Context, conn *websocket.Conn) *protocol.SnapshotPayload {
	t.Helper()
	env := readEnvelope(t, ctx, conn)
	if env.Type != protocol.MsgSnapshot || env.Snapshot == nil {
		t.Fatalf("expected snapshot, got %+v", env)
	}
	return env.Snapshot
}

func readAck(t *testing.T, ctx context.Context, conn *websocket.Conn, commandID string) {
	t.Helper()
	env := readEnvelope(t, ctx, conn)
	if env.Type != protocol.MsgAck || env.Ack == nil || env.Ack.CommandID != commandID {
		t.Fatalf("expected ack for %s, got %+v", commandID, env)
	}
}

func mustReadEvent(t *testing.T, ctx context.Context, conn *websocket.Conn, kind protocol.EventKind) *protocol.EventPayload {
	t.Helper()
	for {
		env := readEnvelope(t, ctx, conn)
		if env.Type == protocol.MsgEvent && env.Event != nil && env.Event.Kind == kind {
			return env.Event
		}
	}
}
