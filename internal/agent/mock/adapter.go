package mock

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Itz-snj/MSclaudeConnector/internal/agent"
	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
)

// Adapter is a deterministic fake agent used to validate the hub and store
// without needing a real Claude/Codex binary.
type Adapter struct {
	mu       sync.Mutex
	events   chan protocol.EventPayload
	commands chan agent.Command
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	cfg      agent.SessionConfig
}

func New() *Adapter {
	return &Adapter{}
}

func (a *Adapter) Start(cfg agent.SessionConfig) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return errors.New("mock adapter already started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.cfg = cfg
	a.events = make(chan protocol.EventPayload, 64)
	a.commands = make(chan agent.Command, 16)
	a.wg.Add(1)
	go a.loop(ctx)
	return nil
}

func (a *Adapter) Events() <-chan protocol.EventPayload {
	return a.events
}

func (a *Adapter) Command(cmd agent.Command) error {
	a.mu.Lock()
	ch := a.commands
	a.mu.Unlock()
	if ch == nil {
		return errors.New("adapter not started")
	}
	select {
	case ch <- cmd:
		return nil
	default:
		return errors.New("command buffer full")
	}
}

func (a *Adapter) Stop() error {
	a.mu.Lock()
	cancel := a.cancel
	a.cancel = nil
	a.commands = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		a.wg.Wait()
	}
	return nil
}

func (a *Adapter) loop(ctx context.Context) {
	defer a.wg.Done()
	pendingPermission := ""
	pendingQuestion := ""
	for {
		select {
		case <-ctx.Done():
			close(a.events)
			return
		case cmd := <-a.commands:
			switch cmd.Kind {
			case protocol.CmdSendPrompt:
				body, ok := cmd.Payload.(protocol.SendPromptBody)
				if !ok {
					continue
				}
				// The hub owns the user_prompt event; the adapter only streams
				// the agent's reaction to it.
				a.emit(protocol.EventPayload{Kind: protocol.EventStatusChange, Payload: payload(protocol.StatusChangeBody{Status: "running"})})
				a.emit(protocol.EventPayload{Kind: protocol.EventTextDelta, Payload: payload(protocol.TextDeltaBody{Content: "Ack: " + body.Text + "\n"})})
				switch body.Text {
				case "perm":
					pendingPermission = "r-mock-1"
					a.emit(protocol.EventPayload{Kind: protocol.EventPermissionRequest, Payload: payload(protocol.PermissionRequestBody{RequestID: pendingPermission, Kind: "bash", Summary: "git push"})})
				case "question":
					pendingQuestion = "q-mock-1"
					a.emit(protocol.EventPayload{Kind: protocol.EventQuestion, Payload: payload(protocol.QuestionBody{QuestionID: pendingQuestion, Text: "Which branch?"})})
				default:
					a.emit(protocol.EventPayload{Kind: protocol.EventTurnComplete, Payload: payload(struct{}{})})
					a.emit(protocol.EventPayload{Kind: protocol.EventStatusChange, Payload: payload(protocol.StatusChangeBody{Status: "idle"})})
				}
			case protocol.CmdAnswerPermission:
				body, ok := cmd.Payload.(protocol.AnswerPermissionBody)
				if !ok || body.RequestID != pendingPermission || pendingPermission == "" {
					continue
				}
				// The hub owns the permission_resolved event; the adapter just continues.
				pendingPermission = ""
				a.emit(protocol.EventPayload{Kind: protocol.EventTurnComplete, Payload: payload(struct{}{})})
				a.emit(protocol.EventPayload{Kind: protocol.EventStatusChange, Payload: payload(protocol.StatusChangeBody{Status: "idle"})})
			case protocol.CmdAnswerQuestion:
				body, ok := cmd.Payload.(protocol.AnswerQuestionBody)
				if !ok || body.QuestionID != pendingQuestion || pendingQuestion == "" {
					continue
				}
				// The hub owns the question_resolved event; the adapter just continues.
				pendingQuestion = ""
				a.emit(protocol.EventPayload{Kind: protocol.EventTurnComplete, Payload: payload(struct{}{})})
				a.emit(protocol.EventPayload{Kind: protocol.EventStatusChange, Payload: payload(protocol.StatusChangeBody{Status: "idle"})})
			case protocol.CmdSetMode:
				// The hub owns the mode_changed event; nothing further to simulate.
			case protocol.CmdInterrupt:
				a.emit(protocol.EventPayload{Kind: protocol.EventStatusChange, Payload: payload(protocol.StatusChangeBody{Status: "idle"})})
			}
		}
	}
}

func payload(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func (a *Adapter) emit(ev protocol.EventPayload) {
	select {
	case a.events <- ev:
	case <-time.After(time.Second):
	}
}
