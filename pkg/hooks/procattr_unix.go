//go:build !windows

package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// pidsOnPort lists the processes using the TCP port, found with lsof (macOS and Linux).
func pidsOnPort(port int) ([]int, error) {
	cmd := exec.Command("lsof", "-t", "-i", fmt.Sprintf(":%d", port)) // #nosec G204 -- port is from internal config
	output, err := cmd.Output()
	if err != nil {
		// lsof returns exit code 1 when no process is found - that's fine
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find process on port: %w", err)
	}
	return parseLsofPIDs(string(output)), nil
}

// killPID kills the process outright.
func killPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// isProcessAlive checks if a process with the given PID exists and is alive.
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if process exists without actually sending a signal.
	return proc.Signal(syscall.Signal(0)) == nil
}
