package hooks

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBinaryNameFor(t *testing.T) {
	cases := []struct{ goos, name, want string }{
		{"windows", "worker", "worker.exe"},
		{"darwin", "worker", "worker"},
		{"linux", "mcp-server", "mcp-server"},
	}
	for _, c := range cases {
		if got := binaryNameFor(c.goos, c.name); got != c.want {
			t.Errorf("binaryNameFor(%q, %q) = %q, want %q", c.goos, c.name, got, c.want)
		}
	}
}

func TestParseLsofPIDs(t *testing.T) {
	got := parseLsofPIDs("4312\n  77 \n\nnot-a-pid\n0\n")
	if want := []int{4312, 77}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := parseLsofPIDs(""); len(got) != 0 {
		t.Errorf("empty output gave %v", got)
	}
}

func TestParseNetstatListeners(t *testing.T) {
	out := "Active Connections\r\n\r\n" +
		"  Proto  Local Address          Foreign Address        State           PID\r\n" +
		"  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1012\r\n" +
		"  TCP    127.0.0.1:37777        0.0.0.0:0              LISTENING       4312\r\n" +
		"  TCP    [::1]:37777            [::]:0                 LISTENING       4312\r\n" +
		"  TCP    127.0.0.1:37777        127.0.0.1:51234        ESTABLISHED     4312\r\n" +
		"  TCP    127.0.0.1:51234        127.0.0.1:37777        ESTABLISHED     9000\r\n" +
		"  TCP    127.0.0.1:137777       0.0.0.0:0              LISTENING       5555\r\n" +
		// A German Windows localizes the state column; the wildcard remote address still identifies a listener.
		"  TCP    0.0.0.0:37777          0.0.0.0:0              ABHÖREN         6001\r\n"
	got := parseNetstatListeners(out, 37777)
	if want := []int{4312, 4312, 6001}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := parseNetstatListeners(out, 1); len(got) != 0 {
		t.Errorf("port 1 gave %v", got)
	}
}

func TestHomeDirFollowsTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)        // Unix
	t.Setenv("USERPROFILE", dir) // Windows
	if got := homeDir(); got != dir {
		t.Errorf("homeDir() = %q, want %q", got, dir)
	}
	if got, want := workerCachePath(), filepath.Join(dir, ".claude-mnemonic", ".worker-cache"); got != want {
		t.Errorf("workerCachePath() = %q, want %q", got, want)
	}
}

func TestFindWorkerBinaryUsesTheStableLocationWithExeSuffix(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("CLAUDE_PLUGIN_ROOT", "")
	bin := filepath.Join(dir, ".claude-mnemonic", "bin", binaryName("worker"))
	mustWrite(t, bin)
	if got := findWorkerBinary(); got != bin {
		t.Errorf("findWorkerBinary() = %q, want %q", got, bin)
	}
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
}
