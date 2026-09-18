package agent

import (
	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
)

// SessionConfig describes an agent session to start or resume.
type SessionConfig struct {
	AgentType       string   // "claude" | "codex"
	WorkingDir      string   // project directory on the host
	ResumeSessionID string   // vendor session id for resume; empty = new session
	Env             []string // full environment passed to child process
}

// Command is a normalized mutation sent from the hub to the agent adapter.
type Command struct {
	Kind    protocol.CommandKind
	Payload interface{}
}

// Adapter normalizes a vendor coding-agent CLI into a stream of events and
// accepts normalized commands. It is the only layer that knows vendor specifics.
type Adapter interface {
	Start(cfg SessionConfig) error
	Events() <-chan protocol.EventPayload
	Command(cmd Command) error
	Stop() error
}
