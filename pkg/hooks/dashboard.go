package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dashboardMarker is the file that remembers which version already told the user where the dashboard is.
const dashboardMarker = ".dashboard-announced"

// DataDir is claude-mnemonic's data directory (~/.claude-mnemonic), the same one the worker uses.
func DataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".claude-mnemonic")
}

// DashboardURL is where the web dashboard of a worker on the given port is.
func DashboardURL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}

// DashboardNotice returns the one-line message that tells the user where the dashboard is, or "" when this version
// has already told them. It is shown on the first session after an install or an update (the marker in dataDir holds
// the version that last said it), so it is never noise. It is for the user only: it is never added to the context
// the model receives. A marker that cannot be written means it may be said again, which is harmless.
func DashboardNotice(dataDir, version string, port int) string {
	marker := filepath.Join(dataDir, dashboardMarker)
	if seen, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(seen)) == version {
		return ""
	}
	if err := os.MkdirAll(dataDir, 0o755); err == nil {
		_ = os.WriteFile(marker, []byte(version+"\n"), 0o644)
	}
	return fmt.Sprintf("claude-mnemonic: your memory dashboard is at %s (or run /claude-mnemonic:dashboard)", DashboardURL(port))
}
