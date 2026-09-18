//go:build !windows

package supervisor

import (
	"os/exec"
	"syscall"
)

type platformData struct{}

func configureProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func assignAfterStart(cmd *exec.Cmd, pd *platformData) error {
	return nil
}

func killTree(cmd *exec.Cmd, pd *platformData) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Kill()
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
