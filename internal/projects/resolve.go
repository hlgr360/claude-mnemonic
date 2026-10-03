// Package projects resolves caller-supplied project references (an ID, a host
// path or a name) to canonical project IDs.
//
// It is pure: callers pass in the known project IDs and the alias map, so the
// same logic serves the worker API and is easy to test.
package projects

import (
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/hooks"
)

// Ref is a project reference as supplied by a caller. When several fields are
// set the precedence is ID, then Path, then Name.
type Ref struct {
	ID   string
	Path string
	Name string
}

// Match says how a reference was resolved.
type Match string

const (
	// MatchExact means the ID is a known project.
	MatchExact Match = "exact"
	// MatchAlias means the ID (or the ID derived from a path) is an alias of another project.
	MatchAlias Match = "alias"
	// MatchPath means the ID was derived from a host path.
	MatchPath Match = "path"
	// MatchName means a unique project has that directory name.
	MatchName Match = "name"
	// MatchNone means the reference could not be resolved; see Candidates.
	MatchNone Match = "none"
)

// Resolution is the outcome of resolving a Ref.
type Resolution struct {
	// ID is the canonical project ID. Empty when Match is MatchNone.
	ID    string `json:"id,omitempty"`
	Match Match  `json:"match"`
	// Known reports whether ID is already a project in the store. A path that
	// resolves to an unknown ID is valid: it is a project with no history yet.
	Known bool `json:"known"`
	// Candidates lists possible projects when the reference was ambiguous or unmatched.
	Candidates []string `json:"candidates,omitempty"`
}

// DisplayName returns the directory-name part of a project ID ("repo_ab12cd" -> "repo").
func DisplayName(id string) string {
	i := strings.LastIndex(id, "_")
	if i <= 0 || len(id)-i-1 != 6 {
		return id
	}
	return id[:i]
}

// Resolve maps ref to a canonical project using the known project IDs and the
// alias map (alias -> canonical).
func Resolve(ref Ref, known []string, aliases map[string]string) Resolution {
	knownSet := make(map[string]struct{}, len(known))
	for _, k := range known {
		knownSet[k] = struct{}{}
	}
	isKnown := func(id string) bool { _, ok := knownSet[id]; return ok }

	switch {
	case ref.ID != "":
		return resolveID(ref.ID, isKnown, aliases)
	case ref.Path != "":
		return resolvePath(ref.Path, isKnown, aliases)
	case ref.Name != "":
		return resolveName(ref.Name, known, isKnown, aliases)
	}
	return Resolution{Match: MatchNone}
}

func resolveID(id string, isKnown func(string) bool, aliases map[string]string) Resolution {
	if canonical, ok := aliases[id]; ok {
		return Resolution{ID: canonical, Match: MatchAlias, Known: isKnown(canonical)}
	}
	if isKnown(id) {
		return Resolution{ID: id, Match: MatchExact, Known: true}
	}
	return Resolution{Match: MatchNone}
}

func resolvePath(path string, isKnown func(string) bool, aliases map[string]string) Resolution {
	path = cleanHostPath(path)
	if !filepath.IsAbs(path) {
		// A relative path would be resolved against the server's own working
		// directory, which says nothing about the caller's project.
		return Resolution{Match: MatchNone}
	}
	id := hooks.ProjectIDWithName(path)
	if canonical, ok := aliases[id]; ok {
		return Resolution{ID: canonical, Match: MatchAlias, Known: isKnown(canonical)}
	}
	return Resolution{ID: id, Match: MatchPath, Known: isKnown(id)}
}

// cleanHostPath accepts a plain path or a file:// URI (the form MCP roots use).
func cleanHostPath(p string) string {
	p = strings.TrimSpace(p)
	if rest, ok := strings.CutPrefix(p, "file://"); ok {
		if u, err := url.PathUnescape(rest); err == nil {
			rest = u
		}
		return rest
	}
	return p
}

func resolveName(name string, known []string, isKnown func(string) bool, aliases map[string]string) Resolution {
	name = strings.TrimSpace(name)

	// A full project ID typed into the name field is still an ID.
	if r := resolveID(name, isKnown, aliases); r.Match != MatchNone {
		return r
	}

	want := strings.ToLower(name)
	exact := map[string]struct{}{}
	partial := map[string]struct{}{}
	consider := func(label, target string) {
		display := strings.ToLower(DisplayName(label))
		switch {
		case display == want:
			exact[target] = struct{}{}
		case strings.Contains(display, want):
			partial[target] = struct{}{}
		}
	}
	for _, id := range known {
		if _, isAlias := aliases[id]; isAlias {
			continue // reached through its canonical project below
		}
		consider(id, id)
	}
	for alias, canonical := range aliases {
		consider(alias, canonical)
	}

	switch len(exact) {
	case 1:
		id := only(exact)
		return Resolution{ID: id, Match: MatchName, Known: isKnown(id)}
	case 0:
		return Resolution{Match: MatchNone, Candidates: sorted(partial)}
	default:
		return Resolution{Match: MatchNone, Candidates: sorted(exact)}
	}
}

func only(set map[string]struct{}) string {
	for k := range set {
		return k
	}
	return ""
}

func sorted(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
