package hub

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Itz-snj/MSclaudeConnector/internal/auth"
	"github.com/Itz-snj/MSclaudeConnector/internal/logger"
	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
	"github.com/coder/websocket"
)

func sendHelloCred(t *testing.T, ctx context.Context, conn *websocket.Conn, deviceID, cred string, lastSeq int64) {
	t.Helper()
	writeEnvelope(t, ctx, conn, protocol.Envelope{
		Version: protocol.Version,
		Type:    protocol.MsgHello,
		Hello: &protocol.HelloPayload{
			DeviceID:   deviceID,
			Credential: cred,
			LastSeq:    lastSeq,
		},
	})
}

func newPair(t *testing.T, ctx context.Context, authz *auth.Auth, wsURL, name string) (cred, deviceID string) {
	t.Helper()
	token, err := authz.GeneratePairingToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := authz.ApprovePairingToken(token, name); err != nil {
		t.Fatalf("approve token: %v", err)
	}
	return pair(t, ctx, wsURL, token)
}

func TestHelloCredentialAuth(t *testing.T) {
	_, authz, _, cancel, wsURL := setupHub(t)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 15*time.Second)
	defer timeout()

	cred, deviceID := newPair(t, ctx, authz, wsURL, "browser")

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	sendHelloCred(t, ctx, conn, deviceID, cred, 0)
	snap := readSnapshot(t, ctx, conn)
	if snap.SessionID == "" {
		t.Fatal("expected snapshot with session id")
	}

	// Credential-only hello should have the device id filled in server-side.
	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "")
	sendHelloCred(t, ctx, conn2, "", cred, 0)
	_ = readSnapshot(t, ctx, conn2)
}

func TestHeaderBeatsHelloCredential(t *testing.T) {
	_, authz, _, cancel, wsURL := setupHub(t)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 15*time.Second)
	defer timeout()

	credA, deviceA := newPair(t, ctx, authz, wsURL, "pc-a")
	credB, _ := newPair(t, ctx, authz, wsURL, "pc-b")

	header := http.Header{"Authorization": []string{"Bearer " + credA}}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// If the hello credential were consulted, this would resolve to device B
	// and then fail device_mismatch (hello.deviceId is A).
	sendHelloCred(t, ctx, conn, deviceA, credB, 0)
	_ = readSnapshot(t, ctx, conn)
}

func TestDeviceMismatchViaHelloCredential(t *testing.T) {
	_, authz, _, cancel, wsURL := setupHub(t)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 15*time.Second)
	defer timeout()

	cred, _ := newPair(t, ctx, authz, wsURL, "pc-a")

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	sendHelloCred(t, ctx, conn, "deadbeefdeadbeef", cred, 0)
	env := readEnvelope(t, ctx, conn)
	if env.Type != protocol.MsgError || env.Error == nil || env.Error.Code != "device_mismatch" {
		t.Fatalf("expected device_mismatch, got %+v", env)
	}
}

type countingAuth struct {
	*auth.Auth
	validateCalls int32
}

func (c *countingAuth) ValidateCredential(credential string) (string, bool) {
	atomic.AddInt32(&c.validateCalls, 1)
	return c.Auth.ValidateCredential(credential)
}

func TestValidateCredentialCalledOnce(t *testing.T) {
	h, authz, _, cancel, wsURL := setupHub(t)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 15*time.Second)
	defer timeout()

	ca := &countingAuth{Auth: authz}
	h.auth = ca

	cred, deviceID := newPair(t, ctx, authz, wsURL, "browser")
	if got := atomic.LoadInt32(&ca.validateCalls); got != 0 {
		t.Fatalf("pairing should not validate credentials, got %d calls", got)
	}

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	sendHelloCred(t, ctx, conn, deviceID, cred, 0)
	_ = readSnapshot(t, ctx, conn)
	if got := atomic.LoadInt32(&ca.validateCalls); got != 1 {
		t.Fatalf("expected exactly 1 ValidateCredential call, got %d", got)
	}

	// Header + hello credential together must still validate once (header wins).
	header := http.Header{"Authorization": []string{"Bearer " + cred}}
	conn2, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "")
	sendHelloCred(t, ctx, conn2, deviceID, cred, 0)
	_ = readSnapshot(t, ctx, conn2)
	if got := atomic.LoadInt32(&ca.validateCalls); got != 2 {
		t.Fatalf("expected 2 total ValidateCredential calls, got %d", got)
	}
}

func TestCatchUpLargeLogNoDrop(t *testing.T) {
	h, authz, _, cancel, wsURL := setupHub(t)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 30*time.Second)
	defer timeout()

	const n = 600
	for i := 0; i < n; i++ {
		if _, err := h.store.AppendEvent(h.cfg.SessionID, protocol.EventTextDelta, protocol.TextDeltaBody{Content: "x"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	cred, deviceID := newPair(t, ctx, authz, wsURL, "browser")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	sendHelloCred(t, ctx, conn, deviceID, cred, 0)

	snap := readSnapshot(t, ctx, conn)
	if snap.LastSeq != n {
		t.Fatalf("expected snapshot lastSeq %d, got %d", n, snap.LastSeq)
	}

	var prev int64
	for i := 0; i < n; i++ {
		ev := readEnvelope(t, ctx, conn)
		if ev.Type != protocol.MsgEvent || ev.Event == nil {
			t.Fatalf("event %d: unexpected envelope %+v", i, ev)
		}
		if ev.Event.Seq != prev+1 {
			t.Fatalf("seq gap: got %d after %d", ev.Event.Seq, prev)
		}
		prev = ev.Event.Seq
	}
}

func TestOutboundOverflowClosesWithTryAgainLater(t *testing.T) {
	// Deterministic unit check: a full queue reports overflow and signals it.
	synthetic := &client{
		deviceID: "synthetic",
		send:     make(chan []byte, 1),
		overflow: make(chan struct{}, 1),
		log:      logger.New(),
	}
	h := &Hub{log: logger.New()}
	synthetic.send <- []byte("full")
	if h.enqueueRaw(synthetic, []byte("x")) {
		t.Fatal("expected enqueueRaw to report overflow")
	}
	select {
	case <-synthetic.overflow:
	default:
		t.Fatal("expected overflow signal")
	}

	// Integration check: overflow closes the real connection with TryAgainLater.
	realHub, authz, _, cancel, wsURL := setupHub(t)
	defer cancel()
	ctx, timeout := context.WithTimeout(context.Background(), 15*time.Second)
	defer timeout()

	cred, deviceID := newPair(t, ctx, authz, wsURL, "browser")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	sendHelloCred(t, ctx, conn, deviceID, cred, 0)
	_ = readSnapshot(t, ctx, conn)

	var c *client
	realHub.mu.RLock()
	for cl := range realHub.clients {
		c = cl
	}
	realHub.mu.RUnlock()
	if c == nil {
		t.Fatal("client not registered")
	}

	select {
	case c.overflow <- struct{}{}:
	default:
		t.Fatal("overflow signal already set")
	}

	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusTryAgainLater {
		t.Fatalf("expected StatusTryAgainLater, got %v (%v)", websocket.CloseStatus(err), err)
	}
}
