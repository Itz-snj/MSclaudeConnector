//go:build windows

package supervisor

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

type platformData struct {
	job windows.Handle
}

func configureProcess(cmd *exec.Cmd) error {
	return nil
}

func assignAfterStart(cmd *exec.Cmd, pd *platformData) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(job)
		return err
	}

	const access = windows.PROCESS_CREATE_PROCESS | windows.PROCESS_TERMINATE | windows.PROCESS_SET_QUOTA
	procHandle, err := windows.OpenProcess(access, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return err
	}
	defer windows.CloseHandle(procHandle)

	if err := windows.AssignProcessToJobObject(job, procHandle); err != nil {
		windows.CloseHandle(job)
		return err
	}

	pd.job = job
	return nil
}

func killTree(cmd *exec.Cmd, pd *platformData) error {
	if pd.job != 0 {
		// Closing the job handle terminates every process in the job because
		// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE is set.
		windows.CloseHandle(pd.job)
		pd.job = 0
	}
	if cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}
