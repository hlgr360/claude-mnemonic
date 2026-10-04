package projects

import (
	"bufio"
	"context"
	"net/url"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"
)

// Identity is what tells a project's folder apart from another's beyond its name: the git remote the folder was cloned
// from and the root of the checkout. Credentials never reach it.
type Identity struct {
	// Remote is the normalised remote URL (host/path), "" when the folder is not a git checkout with a remote.
	Remote string
	// Root is the top of the checkout, with symlinks resolved as git reports it.
	Root string
}

// NormalizeRemote turns the forms one repository is cloned from (ssh, scp-like, https, git, with or without a user,
// password, port, ".git" or a trailing slash) into one string, "host/path", so two clones of one repository compare
// equal. Credentials are dropped, the host is lower-cased, and for the big hosting services, which ignore case in
// repository names, so is the path. A remote that is a plain folder keeps its path. "" for an empty remote.
func NormalizeRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var host, p string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		if u.Scheme == "file" {
			return cleanRemotePath(u.Path)
		}
		host, p = strings.ToLower(u.Hostname()), u.Path
	case strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "."):
		return cleanRemotePath(raw)
	default: // scp-like: [user@]host:path
		at := raw
		if i := strings.Index(at, "@"); i >= 0 && i < strings.Index(at, ":") {
			at = at[i+1:]
		}
		i := strings.Index(at, ":")
		if i <= 0 {
			return ""
		}
		host, p = strings.ToLower(at[:i]), at[i+1:]
	}

	p = cleanRemotePath(p)
	if host == "" || p == "" {
		return ""
	}
	host, p = azureDevOps(host, p)
	if caseInsensitiveHost(host) {
		p = strings.ToLower(p)
	}
	return host + "/" + p
}

// cleanRemotePath trims slashes and the ".git" suffix.
func cleanRemotePath(p string) string {
	p = strings.TrimSuffix(strings.Trim(path.Clean("/"+strings.TrimSpace(p)), "/"), ".git")
	return strings.Trim(p, "/")
}

// azureDevOps makes the ssh form (ssh.dev.azure.com:v3/org/project/repo) and the https form
// (dev.azure.com/org/project/_git/repo) of one repository the same.
func azureDevOps(host, p string) (string, string) {
	switch host {
	case "ssh.dev.azure.com", "vs-ssh.visualstudio.com":
		return "dev.azure.com", strings.TrimPrefix(p, "v3/")
	case "dev.azure.com":
		return host, strings.Replace(p, "/_git/", "/", 1)
	}
	return host, p
}

func caseInsensitiveHost(host string) bool {
	switch host {
	case "github.com", "gitlab.com", "bitbucket.org", "dev.azure.com":
		return true
	}
	return false
}

// PickRemote chooses the remote that identifies a checkout from its remotes (name -> url): origin when there is one,
// otherwise the first by name. The URL is returned normalised; "" when there is none.
func PickRemote(remotes map[string]string) string {
	if u, ok := remotes["origin"]; ok {
		if n := NormalizeRemote(u); n != "" {
			return n
		}
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if n := NormalizeRemote(remotes[name]); n != "" {
			return n
		}
	}
	return ""
}

// ParseRemoteConfig reads the output of `git config --get-regexp '^remote\..*\.url$'`: one "remote.<name>.url <url>"
// per line. Remote names may contain dots.
func ParseRemoteConfig(out string) map[string]string {
	remotes := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok || !strings.HasPrefix(key, "remote.") || !strings.HasSuffix(key, ".url") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		if name != "" {
			remotes[name] = strings.TrimSpace(value)
		}
	}
	return remotes
}

// GitIdentity reads the identity of the checkout that contains dir. It runs git read-only with a short deadline and
// returns the zero Identity when dir is not inside a git checkout (or git is missing); that is not an error.
func GitIdentity(ctx context.Context, dir string) Identity {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	run := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	root := run("rev-parse", "--show-toplevel")
	if root == "" {
		return Identity{}
	}
	return Identity{Root: root, Remote: PickRemote(ParseRemoteConfig(run("config", "--get-regexp", `^remote\..*\.url$`)))}
}
