package hub

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Itz-snj/MSclaudeConnector/internal/agent"
	"github.com/Itz-snj/MSclaudeConnector/internal/agent/mock"
	"github.com/Itz-snj/MSclaudeConnector/internal/auth"
	"github.com/Itz-snj/MSclaudeConnector/internal/logger"
	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
	"github.com/coder/websocket"
)

type fakeApprover struct {
	mu    sync.Mutex
	calls []ApprovalRequest
	allow bool
	name  string
}

func (f *fakeApprover) RequestApproval(_ context.Context, req ApprovalRequest) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	return f.name, f.allow
}

func (f *fakeApprover) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func setupHubWithApprover(t *testing.T, approver Approver) (*Hub, *auth.Auth, string, context.CancelFunc) {
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
	h := New(Config{SessionID: sess.ID, Approver: approver}, logger.New(), st, authz, adapter)
	go h.Run(ctx)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return h, authz, wsURL, cancel
}

func TestPairingApprovedAtConnectTime(t *testing.T) {
	approver := &fakeApprover{allow: true, name: "Pixel 8"}
	_, authz, wsURL, cancel := setupHubWithApprover(t, approver)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 15*time.Second)
	defer timeout()

	token, err := authz.GeneratePairingToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sendHello(t, ctx, conn, "", 0, token)
	env := readEnvelope(t, ctx, conn)
	if env.Type != protocol.MsgPaired || env.Paired == nil {
		t.Fatalf("expected paired, got %+v", env)
	}
	if env.Paired.Name != "Pixel 8" || env.Paired.Credential == "" || env.Paired.DeviceID == "" {
		t.Fatalf("unexpected paired payload %+v", env.Paired)
	}
	cred := env.Paired.Credential
	deviceID := env.Paired.DeviceID
	_ = conn.Close(websocket.StatusNormalClosure, "")

	if approver.callCount() != 1 {
		t.Fatalf("expected one approval request, got %d", approver.callCount())
	}

	// The token is one-time: reusing it must be denied without a new prompt.
	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "")
	sendHello(t, ctx, conn2, "", 0, token)
	env = readEnvelope(t, ctx, conn2)
	if env.Type != protocol.MsgError || env.Error == nil || env.Error.Code != "pairing_denied" {
		t.Fatalf("expected pairing_denied, got %+v", env)
	}
	if approver.callCount() != 1 {
		t.Fatalf("no second approval should be requested, got %d", approver.callCount())
	}

	// Authenticated snapshot should attribute the device by name (Gap E).
	conn3, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn3.Close(websocket.StatusNormalClosure, "")
	sendHelloCred(t, ctx, conn3, deviceID, cred, 0)
	snap := readSnapshot(t, ctx, conn3)
	found := false
	for _, d := range snap.Devices {
		if d.DeviceID == deviceID && d.Name == "Pixel 8" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected device roster to include Pixel 8, got %+v", snap.Devices)
	}
}

func TestPairingDeniedAtConnectTime(t *testing.T) {
	approver := &fakeApprover{allow: false}
	_, authz, wsURL, cancel := setupHubWithApprover(t, approver)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 15*time.Second)
	defer timeout()

	token, err := authz.GeneratePairingToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	sendHello(t, ctx, conn, "", 0, token)
	env := readEnvelope(t, ctx, conn)
	if env.Type != protocol.MsgError || env.Error == nil || env.Error.Code != "pairing_denied" {
		t.Fatalf("expected pairing_denied, got %+v", env)
	}
}
