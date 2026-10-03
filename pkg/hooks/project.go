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
	return filepath.Join(inCallerPathForm(root, mainRoot), rel)
}

// inCallerPathForm rewrites mainRoot, which git records with symlinks resolved,
// into the form the caller used to reach the worktree root. When both live
// under the same symlinked directory (macOS /var -> /private/var, a symlinked
// ~/code), the project is otherwise hashed from two spellings of one path and
// splits in two.
//
// It compares the root as given with its resolved form, finds the trailing
// path elements they share, and swaps the differing leading part. If anything
// does not line up it returns mainRoot unchanged.
func inCallerPathForm(root, mainRoot string) string {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil || realRoot == root {
		return mainRoot
	}

	given := splitPath(root)
	real := splitPath(realRoot)
	shared := 0
	for shared < len(given) && shared < len(real) &&
		given[len(given)-1-shared] == real[len(real)-1-shared] {
		shared++
	}
	givenPrefix := joinPath(given[:len(given)-shared])
	realPrefix := joinPath(real[:len(real)-shared])

	rest, ok := cutPathPrefix(mainRoot, realPrefix)
	if !ok {
		return mainRoot
	}
	return filepath.Join(givenPrefix, rest)
}

func splitPath(p string) []string {
	var parts []string
	for _, e := range strings.Split(filepath.ToSlash(filepath.Clean(p)), "/") {
		if e != "" {
			parts = append(parts, e)
		}
	}
	return parts
}

func joinPath(parts []string) string {
	return string(filepath.Separator) + filepath.Join(parts...)
}

// cutPathPrefix reports whether p lies under prefix, comparing whole path
// elements, and returns the remainder.
func cutPathPrefix(p, prefix string) (string, bool) {
	p, prefix = filepath.Clean(p), filepath.Clean(prefix)
	if prefix == string(filepath.Separator) {
		return strings.TrimPrefix(p, string(filepath.Separator)), true
	}
	if p == prefix {
		return "", true
	}
	if rest, ok := strings.CutPrefix(p, prefix+string(filepath.Separator)); ok {
		return rest, true
	}
	return "", false
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
