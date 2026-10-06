//go:build windows

package hooks

import (
	"fmt"
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	// detachedProcess gives the worker no console, so it survives the hook's console closing and shows no window.
	detachedProcess = 0x00000008
	// createNoWindow is for short helper commands (netstat); it is ignored together with detachedProcess.
	createNoWindow = 0x08000000

	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

func setSysProcAttr(cmd *exec.Cmd) {
	// Windows has no Setpgid: a new process group plus a detached process outlives the hook that started it.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}

// pidsOnPort lists the processes listening on the TCP port, found with netstat.
func pidsOnPort(port int) ([]int, error) {
	cmd := exec.Command("netstat", "-ano", "-p", "TCP") // #nosec G204 -- fixed arguments
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to find process on port: %w", err)
	}
	return parseNetstatListeners(string(output), port), nil
}

// killPID ends the process outright.
func killPID(pid int) error {
	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid)) // #nosec G115 -- pid is positive
	if err != nil {
		return err
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	return syscall.TerminateProcess(h, 1)
}

// isProcessAlive checks if a process with the given PID exists and has not exited.
func isProcessAlive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid)) // #nosec G115 -- pid is positive
	if err != nil {
		return false
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
