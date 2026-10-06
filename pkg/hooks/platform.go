package hooks

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// binaryNameFor returns the file name of an installed binary on the given OS: Windows executables carry ".exe".
func binaryNameFor(goos, name string) string {
	if goos == "windows" {
		return name + ".exe"
	}
	return name
}

// binaryName is binaryNameFor for the running OS.
func binaryName(name string) string {
	return binaryNameFor(runtime.GOOS, name)
}

// homeDir is the user's home directory: HOME on Unix, USERPROFILE on Windows (a Windows process has no HOME unless
// a Unix-like shell set it, and then it is an MSYS-style path (drive letter as a first directory) that Go cannot open).
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// parseLsofPIDs reads the output of `lsof -t`: one PID per line.
func parseLsofPIDs(output string) []int {
	var pids []int
	for _, line := range strings.Split(output, "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// parseNetstatListeners reads the output of `netstat -ano -p TCP` (Windows) and returns the PIDs that listen on
// the given port. A line looks like:
//
//	TCP    127.0.0.1:37777        0.0.0.0:0              LISTENING       4312
//
// The state column is localized on non-English Windows, so a line counts when its remote address is the wildcard
// (0.0.0.0:0, [::]:0 or *:*) and its local address ends in the port; an established connection has a real remote
// address and is skipped.
func parseNetstatListeners(output string, port int) []int {
	suffix := ":" + strconv.Itoa(port)
	var pids []int
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.EqualFold(f[0], "TCP") {
			continue
		}
		local, remote := f[1], f[2]
		if !strings.HasSuffix(local, suffix) {
			continue
		}
		if remote != "0.0.0.0:0" && remote != "[::]:0" && remote != "*:*" {
			continue
		}
		if pid, err := strconv.Atoi(f[len(f)-1]); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}
