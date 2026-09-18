//go:build !windows

package supervisor

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/Itz-snj/MSclaudeConnector/internal/logger"
)

func TestStartStopSleep(t *testing.T) {
	s := New(logger.New())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mp, err := s.Start(ctx, Config{Command: "sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if mp.Cmd == nil || mp.Cmd.Process == nil {
		t.Fatal("process did not start")
	}

	if err := s.Stop(mp); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Wait briefly for the process to actually exit.
	// Do NOT call mp.Cmd.Wait() here: the supervisor's reap goroutine owns Wait.
	pid := mp.Cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("process did not exit after stop")
}
