package supervisor

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/Itz-snj/MSclaudeConnector/internal/logger"
)

// Config describes a child process to supervise.
type Config struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
}

// ManagedProcess wraps an os/exec.Cmd plus platform-specific state for
// reliable tree termination.
type ManagedProcess struct {
	Cmd      *exec.Cmd
	Stdin    io.WriteCloser
	Stdout   io.ReadCloser
	Stderr   io.ReadCloser
	platform platformData
}

// Supervisor spawns child processes and guarantees their process trees are
// reaped. On Windows this uses Job Objects; on Unix it uses process groups.
type Supervisor struct {
	log logger.Logger
}

func New(log logger.Logger) *Supervisor {
	return &Supervisor{log: log}
}

func (s *Supervisor) Start(ctx context.Context, cfg Config) (*ManagedProcess, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("supervisor: command required")
	}

	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	if cfg.Env != nil {
		cmd.Env = cfg.Env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := configureProcess(cmd); err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	mp := &ManagedProcess{
		Cmd:    cmd,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	}

	if err := assignAfterStart(cmd, &mp.platform); err != nil {
		_ = killTree(cmd, &mp.platform)
		_ = cmd.Wait()
		return nil, err
	}

	s.log.Info("started managed process", "pid", cmd.Process.Pid, "cmd", cfg.Command)
	go s.reap(mp)
	return mp, nil
}

// Stop terminates the process tree synchronously.
func (s *Supervisor) Stop(mp *ManagedProcess) error {
	if mp == nil || mp.Cmd == nil || mp.Cmd.Process == nil {
		return nil
	}
	s.log.Info("stopping managed process", "pid", mp.Cmd.Process.Pid)
	return killTree(mp.Cmd, &mp.platform)
}

func (s *Supervisor) reap(mp *ManagedProcess) {
	err := mp.Cmd.Wait()
	s.log.Info("managed process exited", "pid", mp.Cmd.Process.Pid, "err", err)
}
