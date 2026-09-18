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
// This is intentionally a working skeleton for M2/M3/M4; the exact
// stream-json schema will be validated and tightened during POC-1 (M5).
type Adapter struct {
	log        logger.Logger
	supervisor *supervisor.Supervisor
	events     chan protocol.EventPayload

	mu     sync.Mutex
	proc   *supervisor.ManagedProcess
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(log logger.Logger) *Adapter {
	return &Adapter{
		log:        log,
		supervisor: supervisor.New(log),
		events:     make(chan protocol.EventPayload, 64),
	}
}

func (a *Adapter) Start(cfg agent.SessionConfig) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return errors.New("claude adapter already started")
	}

	args := []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json"}
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

	// TODO(M5): map to exact stream-json input schema during POC-1.
	var msg map[string]interface{}
	switch cmd.Kind {
	case protocol.CmdSendPrompt:
		body, ok := cmd.Payload.(protocol.SendPromptBody)
		if !ok {
			return errors.New("invalid send_prompt payload")
		}
		msg = map[string]interface{}{
			"type":    "user_message",
			"content": body.Text,
		}
	case protocol.CmdAnswerPermission:
		body, ok := cmd.Payload.(protocol.AnswerPermissionBody)
		if !ok {
			return errors.New("invalid answer_permission payload")
		}
		msg = map[string]interface{}{
			"type":       "permission_response",
			"request_id": body.RequestID,
			"allow":      body.Allow,
		}
	case protocol.CmdInterrupt:
		msg = map[string]interface{}{"type": "interrupt"}
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
		line := scanner.Bytes()
		a.handleStreamJSON(line)
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
	// TODO(M5): replace with schema-correct parsing once POC-1 confirms it.
	var raw map[string]interface{}
	if err := json.Unmarshal(line, &raw); err != nil {
		a.emit(protocol.EventPayload{Kind: protocol.EventError, Payload: payload(map[string]string{"error": err.Error()})})
		return
	}

	t, _ := raw["type"].(string)
	switch t {
	case "text", "content_block_delta", "message_delta":
		content, _ := raw["content"].(string)
		if content == "" {
			content, _ = raw["delta"].(string)
		}
		a.emit(protocol.EventPayload{Kind: protocol.EventTextDelta, Payload: payload(protocol.TextDeltaBody{Content: content})})
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
		// Forward unknown structured lines as plain text so nothing is lost.
		a.emit(protocol.EventPayload{Kind: protocol.EventTextDelta, Payload: payload(protocol.TextDeltaBody{Content: string(line) + "\n"})})
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
	default:
		a.log.Warn("claude adapter event buffer full; dropping event", "kind", ev.Kind)
	}
}

// LookPath returns the path to the claude executable, if any.
func LookPath() (string, error) {
	return exec.LookPath("claude")
}
