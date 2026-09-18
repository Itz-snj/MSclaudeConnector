package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateSession(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.CreateSession("claude", "/tmp/project")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("expected session id")
	}
	if sess.Status != "idle" {
		t.Fatalf("expected idle, got %s", sess.Status)
	}

	loaded, err := s.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if loaded == nil || loaded.ID != sess.ID {
		t.Fatal("session not found")
	}
}

func TestAppendEventAndSnapshot(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.CreateSession("claude", "/tmp/project")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err = s.AppendEvent(sess.ID, protocol.EventUserPrompt, protocol.UserPromptBody{Text: "hello", ByDevice: "phone"})
	if err != nil {
		t.Fatalf("append user_prompt: %v", err)
	}
	_, err = s.AppendEvent(sess.ID, protocol.EventStatusChange, protocol.StatusChangeBody{Status: "running"})
	if err != nil {
		t.Fatalf("append status: %v", err)
	}
	_, err = s.AppendEvent(sess.ID, protocol.EventPermissionRequest, protocol.PermissionRequestBody{RequestID: "r1", Kind: "bash", Summary: "git push"})
	if err != nil {
		t.Fatalf("append permission request: %v", err)
	}

	snap, err := s.GetSnapshot(sess.ID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Status != "running" {
		t.Fatalf("expected running status, got %s", snap.Status)
	}
	if snap.LastSeq != 3 {
		t.Fatalf("expected lastSeq 3, got %d", snap.LastSeq)
	}
	if len(snap.PendingPermissions) != 1 || snap.PendingPermissions[0].RequestID != "r1" {
		t.Fatalf("expected one pending permission r1, got %+v", snap.PendingPermissions)
	}

	_, err = s.AppendEvent(sess.ID, protocol.EventPermissionResolved, protocol.PermissionResolvedBody{RequestID: "r1", Allow: true, ByDevice: "pc-b"})
	if err != nil {
		t.Fatalf("append resolved: %v", err)
	}

	snap, err = s.GetSnapshot(sess.ID)
	if err != nil {
		t.Fatalf("snapshot after resolve: %v", err)
	}
	if len(snap.PendingPermissions) != 0 {
		t.Fatalf("expected no pending permissions, got %+v", snap.PendingPermissions)
	}
}

func TestEventsSince(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.CreateSession("claude", "/tmp/project")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	for i := 0; i < 3; i++ {
		_, err := s.AppendEvent(sess.ID, protocol.EventTextDelta, protocol.TextDeltaBody{Content: "x"})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	events, err := s.GetEventsSince(sess.ID, 1, 0)
	if err != nil {
		t.Fatalf("events since: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events after seq 1, got %d", len(events))
	}
	if events[0].Seq != 2 || events[1].Seq != 3 {
		t.Fatalf("wrong seqs: %+v", events)
	}
}

func TestDevices(t *testing.T) {
	s := newTestStore(t)
	if err := s.AddDevice("d1", "phone", "secret-token"); err != nil {
		t.Fatalf("add device: %v", err)
	}

	d, err := s.GetDeviceByCredential("secret-token")
	if err != nil {
		t.Fatalf("get by credential: %v", err)
	}
	if d == nil || d.DeviceID != "d1" || d.Name != "phone" {
		t.Fatalf("unexpected device: %+v", d)
	}

	if err := s.RevokeDevice("d1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	d, err = s.GetDeviceByCredential("secret-token")
	if err != nil {
		t.Fatalf("get revoked: %v", err)
	}
	if d != nil {
		t.Fatal("expected revoked device to be missing")
	}
}

func TestMain(m *testing.M) {
	// Ensure a temp module cache path isn't needed; tests use t.TempDir().
	os.Exit(m.Run())
}
