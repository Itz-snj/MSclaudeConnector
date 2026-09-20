package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/Itz-snj/MSclaudeConnector/internal/agent"
	"github.com/Itz-snj/MSclaudeConnector/internal/logger"
	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
	"github.com/Itz-snj/MSclaudeConnector/internal/supervisor"
)

// Adapter drives Claude Code's stream-json headless mode over stdio.
type Adapter struct {
	log        logger.Logger
	supervisor *supervisor.Supervisor
	events     chan protocol.EventPayload

	mu        sync.Mutex
	proc      *supervisor.ManagedProcess
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	sessionID string
}

func New(log logger.Logger) *Adapter {
	return &Adapter{
		log:        log,
		supervisor: supervisor.New(log),
		events:     make(chan protocol.EventPayload, 256),
	}
}

func (a *Adapter) SessionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionID
}

func (a *Adapter) Start(cfg agent.SessionConfig) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return errors.New("claude adapter already started")
	}

	// --verbose is required by Claude Code for --output-format=stream-json.
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--replay-user-messages",
	}
	if cfg.ResumeSessionID != "" {
		args = append(args, "--resume", cfg.ResumeSessionID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	procCfg := supervisor.Config{
		Command: "claude",
		Args:    args,
		Dir:     cfg.WorkingDir,
		Env:     cfg.Env,
	}
	proc, err := a.supervisor.Start(ctx, procCfg)
	if err != nil {
		cancel()
		a.cancel = nil
		return fmt.Errorf("failed to start claude: %w", err)
	}
	a.proc = proc

	a.wg.Add(2)
	go a.readStdout(proc.Stdout)
	go a.readStderr(proc.Stderr)

	return nil
}

func (a *Adapter) Events() <-chan protocol.EventPayload {
	return a.events
}

func (a *Adapter) Command(cmd agent.Command) error {
	a.mu.Lock()
	proc := a.proc
	a.mu.Unlock()
	if proc == nil {
		return errors.New("adapter not started")
	}

	var msg interface{}
	switch cmd.Kind {
	case protocol.CmdSendPrompt:
		body, ok := cmd.Payload.(protocol.SendPromptBody)
		if !ok {
			return errors.New("invalid send_prompt payload")
		}
		msg = map[string]interface{}{
			"type": "user",
			"message": map[string]interface{}{
				"role":    "user",
				"content": body.Text,
			},
		}
	case protocol.CmdAnswerPermission:
		body, ok := cmd.Payload.(protocol.AnswerPermissionBody)
		if !ok {
			return errors.New("invalid answer_permission payload")
		}
		behavior := "deny"
		if body.Allow {
			behavior = "allow"
		}
		msg = map[string]interface{}{
			"type": "control_response",
			"response": map[string]interface{}{
				"subtype":    "success",
				"request_id": body.RequestID,
				"response": map[string]interface{}{
					"behavior":     behavior,
					"allow_always": body.AllowAlways,
				},
			},
		}
	case protocol.CmdInterrupt:
		msg = map[string]interface{}{
			"type": "control_request",
			"request": map[string]interface{}{
				"subtype": "interrupt",
			},
		}
	case protocol.CmdSetMode:
		body, ok := cmd.Payload.(protocol.SetModeBody)
		if !ok {
			return errors.New("invalid set_mode payload")
		}
		// Maps to claude's --permission-mode values: default | acceptEdits |
		// bypassPermissions | plan. Unverified until POC-1 exercises it live.
		msg = map[string]interface{}{
			"type": "control_request",
			"request": map[string]interface{}{
				"subtype": "set_permission_mode",
				"mode":    body.Mode,
			},
		}
	case protocol.CmdAnswerQuestion:
		// Claude Code's stream-json protocol has no distinct "question" control
		// message today; mid-turn questions currently surface as permission
		// requests (can_use_tool). Nothing to forward until that changes.
		return fmt.Errorf("answer_question has no claude wire equivalent yet")
	default:
		return fmt.Errorf("unsupported command kind: %s", cmd.Kind)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = proc.Stdin.Write(data)
	return err
}

func (a *Adapter) Stop() error {
	a.mu.Lock()
	proc := a.proc
	cancel := a.cancel
	a.proc = nil
	a.cancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if proc != nil {
		_ = a.supervisor.Stop(proc)
	}
	a.wg.Wait()
	return nil
}

func (a *Adapter) readStdout(r io.Reader) {
	defer a.wg.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		a.handleStreamJSON(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		a.log.Error("claude stdout scanner error", "error", err)
	}
}

func (a *Adapter) readStderr(r io.Reader) {
	defer a.wg.Done()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		a.log.Warn("claude stderr", "line", scanner.Text())
	}
}

func (a *Adapter) handleStreamJSON(line []byte) {
	var raw map[string]interface{}
	if err := json.Unmarshal(line, &raw); err != nil {
		a.emit(protocol.EventPayload{Kind: protocol.EventError, Payload: payload(map[string]string{"error": err.Error()})})
		return
	}

	t, _ := raw["type"].(string)
	switch t {
	case "system":
		subtype, _ := raw["subtype"].(string)
		if subtype == "init" {
			if sid, ok := raw["session_id"].(string); ok && sid != "" {
				a.mu.Lock()
				a.sessionID = sid
				a.mu.Unlock()
			}
			a.emit(protocol.EventPayload{Kind: protocol.EventStatusChange, Payload: payload(protocol.StatusChangeBody{Status: "idle"})})
		}
	case "assistant":
		a.emitTextFromMessage(raw["message"])
	case "stream_event":
		a.handlePartialEvent(raw["event"])
	case "result":
		if usage, ok := raw["usage"].(map[string]interface{}); ok {
			inTok, _ := usage["input_tokens"].(float64)
			outTok, _ := usage["output_tokens"].(float64)
			a.emit(protocol.EventPayload{Kind: protocol.EventUsageUpdate, Payload: payload(protocol.UsageUpdateBody{InputTokens: int(inTok), OutputTokens: int(outTok)})})
		}
		a.emit(protocol.EventPayload{Kind: protocol.EventTurnComplete, Payload: payload(struct{}{})})
		a.emit(protocol.EventPayload{Kind: protocol.EventStatusChange, Payload: payload(protocol.StatusChangeBody{Status: "idle"})})
	case "control_request":
		a.handleControlRequest(raw)
	case "user":
		// replayed user messages — ignore
	case "text", "content_block_delta", "message_delta":
		content, _ := raw["content"].(string)
		if content == "" {
			content, _ = raw["delta"].(string)
		}
		if content != "" {
			a.emit(protocol.EventPayload{Kind: protocol.EventTextDelta, Payload: payload(protocol.TextDeltaBody{Content: content})})
		}
	case "tool_use":
		name, _ := raw["name"].(string)
		a.emit(protocol.EventPayload{Kind: protocol.EventToolCallStart, Payload: payload(protocol.ToolCallBody{Name: name})})
	case "tool_result":
		a.emit(protocol.EventPayload{Kind: protocol.EventToolCallEnd, Payload: payload(protocol.ToolCallBody{})})
	case "permission_request":
		rid, _ := raw["request_id"].(string)
		kind, _ := raw["kind"].(string)
		summary, _ := raw["summary"].(string)
		a.emit(protocol.EventPayload{Kind: protocol.EventPermissionRequest, Payload: payload(protocol.PermissionRequestBody{RequestID: rid, Kind: kind, Summary: summary})})
	case "turn_complete", "message_stop":
		a.emit(protocol.EventPayload{Kind: protocol.EventTurnComplete, Payload: payload(struct{}{})})
	case "status":
		status, _ := raw["status"].(string)
		a.emit(protocol.EventPayload{Kind: protocol.EventStatusChange, Payload: payload(protocol.StatusChangeBody{Status: status})})
	case "usage":
		inTok, _ := raw["input_tokens"].(float64)
		outTok, _ := raw["output_tokens"].(float64)
		a.emit(protocol.EventPayload{Kind: protocol.EventUsageUpdate, Payload: payload(protocol.UsageUpdateBody{InputTokens: int(inTok), OutputTokens: int(outTok)})})
	default:
		a.log.Info("unmapped claude stream-json type", "type", t)
	}
}

func (a *Adapter) emitTextFromMessage(msg interface{}) {
	m, ok := msg.(map[string]interface{})
	if !ok {
		return
	}
	content, ok := m["content"].([]interface{})
	if !ok {
		if s, ok := m["content"].(string); ok && s != "" {
			a.emit(protocol.EventPayload{Kind: protocol.EventTextDelta, Payload: payload(protocol.TextDeltaBody{Content: s})})
		}
		return
	}
	for _, block := range content {
		b, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		switch b["type"] {
		case "text":
			text, _ := b["text"].(string)
			if text != "" {
				a.emit(protocol.EventPayload{Kind: protocol.EventTextDelta, Payload: payload(protocol.TextDeltaBody{Content: text})})
			}
		case "tool_use":
			name, _ := b["name"].(string)
			input, _ := json.Marshal(b["input"])
			a.emit(protocol.EventPayload{Kind: protocol.EventToolCallStart, Payload: payload(protocol.ToolCallBody{Name: name, Input: string(input)})})
		}
	}
}

func (a *Adapter) handlePartialEvent(ev interface{}) {
	m, ok := ev.(map[string]interface{})
	if !ok {
		return
	}
	t, _ := m["type"].(string)
	if t == "content_block_delta" {
		delta, _ := m["delta"].(map[string]interface{})
		text, _ := delta["text"].(string)
		if text != "" {
			a.emit(protocol.EventPayload{Kind: protocol.EventTextDelta, Payload: payload(protocol.TextDeltaBody{Content: text})})
		}
	}
}

func (a *Adapter) handleControlRequest(raw map[string]interface{}) {
	rid, _ := raw["request_id"].(string)
	req, _ := raw["request"].(map[string]interface{})
	if req == nil {
		return
	}
	subtype, _ := req["subtype"].(string)
	if subtype != "can_use_tool" {
		return
	}
	name, _ := req["tool_name"].(string)
	if name == "" {
		name, _ = req["name"].(string)
	}
	summary, _ := req["tool_use_id"].(string)
	if input, ok := req["input"]; ok {
		b, _ := json.Marshal(input)
		summary = string(b)
	}
	if rid == "" {
		rid, _ = req["request_id"].(string)
	}
	a.emit(protocol.EventPayload{Kind: protocol.EventPermissionRequest, Payload: payload(protocol.PermissionRequestBody{
		RequestID: rid,
		Kind:      name,
		Summary:   summary,
	})})
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
	default:
		a.log.Warn("claude adapter event buffer full; dropping event", "kind", ev.Kind)
	}
}

func LookPath() (string, error) {
	return exec.LookPath("claude")
}
