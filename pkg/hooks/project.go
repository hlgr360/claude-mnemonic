package hooks

import (
	"os"
	"path/filepath"
	"strings"
)

// CanonicalProjectPath maps a directory inside a linked git worktree to the
// equivalent directory in the main checkout, so that every worktree of a
// repository shares one project identity.
//
// Anything that is not inside a linked worktree is returned unchanged: plain
// checkouts, non-git directories, submodules and unreadable or malformed
// ".git" files. That keeps every pre-existing project ID stable.
//
// Only files are read (no git subprocess), because this runs on every hook call.
func CanonicalProjectPath(absPath string) string {
	root, gitFile, ok := findGitFile(absPath)
	if !ok {
		return absPath
	}

	commonDir, ok := worktreeCommonDir(root, gitFile)
	if !ok {
		return absPath
	}

	// A normal repository keeps its common dir at <repo>/.git; a bare repository
	// is its own common dir.
	mainRoot := commonDir
	if filepath.Base(commonDir) == ".git" {
		mainRoot = filepath.Dir(commonDir)
	}

	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return absPath
	}
	return filepath.Join(mainRoot, rel)
}

// findGitFile walks up from dir looking for the nearest ".git" entry. It
// reports ok only when that entry is a file (a linked worktree or submodule
// marker); a ".git" directory means a plain checkout and yields ok=false.
func findGitFile(dir string) (root, gitFile string, ok bool) {
	for {
		candidate := filepath.Join(dir, ".git")
		if fi, err := os.Lstat(candidate); err == nil {
			if fi.IsDir() {
				return "", "", false
			}
			return dir, candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

// worktreeCommonDir resolves the shared git directory of a linked worktree
// from its ".git" file. Submodules also have a ".git" file but no "commondir"
// entry, so they are rejected here.
func worktreeCommonDir(root, gitFile string) (string, bool) {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(data))
	gitDir, found := strings.CutPrefix(line, "gitdir:")
	if !found {
		return "", false
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}

	data, err = os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return "", false
	}
	commonDir := strings.TrimSpace(string(data))
	if commonDir == "" {
		return "", false
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	return filepath.Clean(commonDir), true
}
