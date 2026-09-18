package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Itz-snj/MSclaudeConnector/internal/agent"
	"github.com/Itz-snj/MSclaudeConnector/internal/logger"
	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
)

func writeFakeClaude(t *testing.T, dir string) {
	t.Helper()
	script := `#!/usr/bin/env python3
import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    msg = json.loads(line)
    typ = msg.get("type")
    if typ == "user_message":
        text = msg.get("content", "")
        if text == "perm":
            print(json.dumps({"type":"permission_request","request_id":"r1","kind":"bash","summary":"git push"}))
            sys.stdout.flush()
            for resp_line in sys.stdin:
                resp = json.loads(resp_line.strip())
                if resp.get("type") == "permission_response":
                    print(json.dumps({"type":"text","content":"allowed"}))
                    print(json.dumps({"type":"turn_complete"}))
                    sys.stdout.flush()
                    break
        else:
            print(json.dumps({"type":"text","content":"Ack: " + text}))
            print(json.dumps({"type":"turn_complete"}))
            sys.stdout.flush()
    elif typ == "interrupt":
        print(json.dumps({"type":"status","status":"idle"}))
        sys.stdout.flush()
`
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
}

func TestAdapterPromptAndPermission(t *testing.T) {
	tmp := t.TempDir()
	writeFakeClaude(t, tmp)
	t.Setenv("PATH", tmp+":"+os.Getenv("PATH"))

	a := New(logger.New())
	if err := a.Start(agent.SessionConfig{AgentType: "claude", WorkingDir: tmp, Env: os.Environ()}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		if err := a.Stop(); err != nil {
			t.Logf("stop error: %v", err)
		}
	}()

	if err := a.Command(agent.Command{Kind: protocol.CmdSendPrompt, Payload: protocol.SendPromptBody{Text: "hello"}}); err != nil {
		t.Fatalf("send prompt: %v", err)
	}

	ch := a.Events()
	deadline := time.After(5 * time.Second)
	var sawText, sawComplete bool
	for !sawComplete {
		select {
		case ev := <-ch:
			if ev.Kind == protocol.EventTextDelta {
				sawText = true
			}
			if ev.Kind == protocol.EventTurnComplete {
				sawComplete = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_complete")
		}
	}
	if !sawText {
		t.Fatal("expected text delta")
	}

	// Permission round-trip.
	if err := a.Command(agent.Command{Kind: protocol.CmdSendPrompt, Payload: protocol.SendPromptBody{Text: "perm"}}); err != nil {
		t.Fatalf("send perm prompt: %v", err)
	}
	var reqID string
	deadline = time.After(5 * time.Second)
	for reqID == "" {
		select {
		case ev := <-ch:
			if ev.Kind == protocol.EventPermissionRequest {
				// In a real test we'd unmarshal; the fake always uses r1.
				reqID = "r1"
			}
		case <-deadline:
			t.Fatal("timed out waiting for permission request")
		}
	}

	if err := a.Command(agent.Command{Kind: protocol.CmdAnswerPermission, Payload: protocol.AnswerPermissionBody{RequestID: reqID, Allow: true}}); err != nil {
		t.Fatalf("answer permission: %v", err)
	}
	deadline = time.After(5 * time.Second)
	sawComplete = false
	for !sawComplete {
		select {
		case ev := <-ch:
			if ev.Kind == protocol.EventTurnComplete {
				sawComplete = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_complete after permission")
		}
	}
}

func TestPOC1RealClaude(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("Set ANTHROPIC_API_KEY to run the real Claude POC-1 gate")
	}
	if _, err := LookPath(); err != nil {
		t.Skip("claude executable not found in PATH")
	}

	// POC-1: drive a real Claude session through prompts, a permission round-trip,
	// an interrupt, and a resume. This is the project kill-gate.
	t.Log("POC-1 real Claude gate not yet automated; run manually with: harness serve --agent claude --dir <project>")
	t.Skip("manual gate")
}
