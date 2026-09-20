package claude

import (
	"encoding/json"
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
print(json.dumps({"type":"system","subtype":"init","session_id":"sess-fake-1"}))
sys.stdout.flush()
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    msg = json.loads(line)
    typ = msg.get("type")
    text = ""
    if typ == "user":
        m = msg.get("message") or {}
        text = m.get("content") or ""
    elif typ == "user_message":
        text = msg.get("content") or ""
    if typ in ("user", "user_message"):
        if text == "perm":
            print(json.dumps({
                "type":"control_request",
                "request_id":"r1",
                "request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"git push"}}
            }))
            sys.stdout.flush()
            for resp_line in sys.stdin:
                resp = json.loads(resp_line.strip())
                if resp.get("type") in ("control_response", "permission_response"):
                    print(json.dumps({"type":"assistant","message":{"content":[{"type":"text","text":"allowed"}]}}))
                    print(json.dumps({"type":"result","usage":{"input_tokens":1,"output_tokens":1}}))
                    sys.stdout.flush()
                    break
        else:
            print(json.dumps({"type":"assistant","message":{"content":[{"type":"text","text":"Ack: " + text}]}}))
            print(json.dumps({"type":"result"}))
            sys.stdout.flush()
    elif typ in ("control_request", "interrupt"):
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

	if err := a.Command(agent.Command{Kind: protocol.CmdSendPrompt, Payload: protocol.SendPromptBody{Text: "perm"}}); err != nil {
		t.Fatalf("send perm prompt: %v", err)
	}
	var reqID string
	deadline = time.After(5 * time.Second)
	for reqID == "" {
		select {
		case ev := <-ch:
			if ev.Kind == protocol.EventPermissionRequest {
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

	dir := t.TempDir()
	a := New(logger.New())
	if err := a.Start(agent.SessionConfig{
		AgentType:  "claude",
		WorkingDir: dir,
		Env:        os.Environ(),
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer a.Stop()

	ch := a.Events()
	if err := a.Command(agent.Command{Kind: protocol.CmdSendPrompt, Payload: protocol.SendPromptBody{Text: "Reply with exactly: pong"}}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	deadline := time.After(90 * time.Second)
	var sawText, sawComplete bool
	for !sawComplete {
		select {
		case ev := <-ch:
			t.Logf("event %s %s", ev.Kind, string(ev.Payload))
			if ev.Kind == protocol.EventTextDelta {
				sawText = true
			}
			if ev.Kind == protocol.EventTurnComplete {
				sawComplete = true
			}
			if ev.Kind == protocol.EventPermissionRequest {
				var body protocol.PermissionRequestBody
				_ = json.Unmarshal(ev.Payload, &body)
				_ = a.Command(agent.Command{Kind: protocol.CmdAnswerPermission, Payload: protocol.AnswerPermissionBody{RequestID: body.RequestID, Allow: false}})
			}
		case <-deadline:
			t.Fatal("POC-1 timed out waiting for turn_complete")
		}
	}
	if !sawText {
		t.Fatal("POC-1 expected text output")
	}
	if a.SessionID() == "" {
		t.Log("warning: no session_id captured from system/init; resume may not work")
	}
}
