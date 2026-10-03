package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mode selects how the server picks a project.
//
// Claude Code starts the server inside the project, so the project is known up
// front. Claude Desktop (chat, Cowork, Code tab) starts one shared server with
// no project at all, so there the model has to name the project on every call.
type Mode string

const (
	// ModeAuto decides from the client name sent in initialize.
	ModeAuto Mode = "auto"
	// ModeCode keeps the historical behaviour: one project, fixed at startup.
	ModeCode Mode = "code"
	// ModeDesktop starts without a project and exposes the project tools.
	ModeDesktop Mode = "desktop"
)

// ParseMode validates a --mode value. The empty string means ModeAuto.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "", ModeAuto:
		return ModeAuto, nil
	case ModeCode, ModeDesktop:
		return m, nil
	}
	return "", fmt.Errorf("invalid mode %q: want auto, code or desktop", s)
}

// IsDesktopClient reports whether an MCP clientInfo.name belongs to Claude
// Desktop: "claude-ai" for chat, "local-agent-mode-*" for Cowork and the Code tab.
func IsDesktopClient(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "claude-ai" || strings.HasPrefix(n, "local-agent-mode")
}

// desktopInstructions is returned in the initialize result. Some clients show
// it to the model, others (Desktop chat) do not, so the same protocol is also
// written into the tool descriptions.
const desktopInstructions = `claude-mnemonic keeps memory per project. This client has no working directory, so choose a project explicitly:
- If a project folder is open, call project_resolve with its absolute path, then context with the returned id.
- Otherwise call project_suggest with the user's first message, offer the user the candidates (plus "none"), and wait for their choice.
- If the user declines, stay read-only: use search without a project, and never call remember.
- Pass the chosen project id to remember and context on every call.`

// workerBootstrap starts the worker when it is not running.
type workerBootstrap struct {
	start     func() error
	lastCheck time.Time
	mu        sync.Mutex
}

const workerCheckTTL = 30 * time.Second

// setClientName records the clientInfo.name sent in initialize.
func (s *Server) setClientName(name string) {
	s.stateMu.Lock()
	s.clientName = name
	s.stateMu.Unlock()
}

// getClientName returns the clientInfo.name sent in initialize ("" before it).
func (s *Server) getClientName() string {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.clientName
}

// defaultProject is the project used when a call names none. Claude Code has
// one, fixed at startup; Desktop mode has none (reads then span all projects)
// unless the user pinned one with --project. It never changes after startup, so
// it needs no lock; only the client name does.
func (s *Server) defaultProject() string {
	if s.desktop() && !s.projectPinned {
		return ""
	}
	return s.project
}

// SetMode overrides client-based mode detection. Call it before Run.
func (s *Server) SetMode(m Mode) { s.mode = m }

// SetProjectPinned marks the project as chosen by the user (the --project flag),
// so Desktop mode keeps it as the default instead of starting unbound. Call it before Run.
func (s *Server) SetProjectPinned(pinned bool) { s.projectPinned = pinned }

// SetWorkerBootstrap registers a function that starts the worker. Desktop has
// no hooks to do it, so the server does it on the first tool call.
func (s *Server) SetWorkerBootstrap(start func() error) {
	s.bootstrap = &workerBootstrap{start: start}
}

// desktop reports whether the server is in Desktop mode.
func (s *Server) desktop() bool {
	switch s.mode {
	case ModeDesktop:
		return true
	case ModeCode:
		return false
	}
	return IsDesktopClient(s.getClientName())
}

// ensureWorker makes sure the worker answers before a Desktop tool call. It is
// a no-op in Code mode (hooks already start the worker) and when no bootstrap
// is registered. A healthy answer is remembered briefly to avoid a probe per call.
func (s *Server) ensureWorker(ctx context.Context) error {
	b := s.bootstrap
	if b == nil || !s.desktop() {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Since(b.lastCheck) < workerCheckTTL {
		return nil
	}
	if _, err := s.proxyGetRaw(ctx, "/health", nil); err == nil {
		b.lastCheck = time.Now()
		return nil
	}
	if err := b.start(); err != nil {
		return fmt.Errorf("worker is not running and could not be started: %w", err)
	}
	b.lastCheck = time.Now()
	return nil
}

// desktopTools are only listed in Desktop mode, so Code's tool list is unchanged.
func desktopTools() []Tool {
	return []Tool{
		{
			Name: "project_suggest",
			Description: "START HERE in a conversation that has no project folder. Pass the user's first message; returns candidate projects ranked by content, name and recency. " +
				"Then ASK the user which project to use or whether to continue WITHOUT one (read-only). Do not pick for them; if confident is true you may propose the top one for confirmation. " +
				"If they decline, never call remember.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"opening_text"},
				"properties": map[string]any{
					"opening_text": map[string]any{"type": "string", "description": "The user's first message or a short summary of what they want to work on"},
					"limit":        map[string]any{"type": "number", "default": 4, "minimum": 1, "maximum": 10},
				},
			},
		},
		{
			Name: "project_resolve",
			Description: "Map a project reference to its canonical project id. When a project folder is open, pass its absolute path (from your session context): this reproduces the id Claude Code uses for the same folder. " +
				"Also accepts a name or an id; ambiguous names return candidates instead of guessing.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Absolute folder path on the user's computer (a file:// URI also works)"},
					"name": map[string]any{"type": "string", "description": "Project directory name, e.g. claude-mnemonic"},
					"id":   map[string]any{"type": "string", "description": "A project id or alias"},
				},
			},
		},
		{
			Name:        "project_list",
			Description: "List all projects with session and observation counts and last activity, most recent first.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name: "context",
			Description: "Load the saved context for a chosen project (what Claude Code receives at session start): recent observations and decisions. " +
				"Call it once after the user picks a project. Not for declined chats.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "Project id from project_suggest, project_list or project_resolve"},
					"path":    map[string]any{"type": "string", "description": "Alternatively the absolute folder path"},
				},
			},
		},
		{
			Name: "project_manage",
			Description: "Inspect, alias, merge or delete projects. stats is read-only. delete and merge are DESTRUCTIVE: first call WITHOUT confirm to get a preview (nothing changes), " +
				"show the user exactly what would be removed or moved and get their explicit approval, then repeat the same call with the confirm token from the preview. " +
				"Never invent or reuse a token, and never confirm without asking the user. A backup of the database is taken automatically before any change. " +
				"Use exact project ids from project_list; names are not accepted here.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action":  map[string]any{"type": "string", "enum": []string{"stats", "delete", "merge", "alias", "unalias"}, "description": "stats: counts for a project. delete: remove a project and all its data. merge: move a project's data into another and keep its id as an alias. alias: declare an id to be another project. unalias: remove an alias."},
					"project": map[string]any{"type": "string", "description": "Exact project id (stats, delete, merge); the surviving project for alias"},
					"into":    map[string]any{"type": "string", "description": "merge: the project to move the data into (an id or alias that already exists)"},
					"alias":   map[string]any{"type": "string", "description": "alias / unalias: the alias id"},
					"confirm": map[string]any{"type": "string", "description": "delete / merge: the token returned by the preview, sent only after the user approved"},
				},
			},
		},
		{
			Name: "remember",
			Description: "Save durable knowledge (a decision, a finding, a fix) to a project's memory. Only after the user has chosen a project: never in a declined, read-only chat, and never with a guessed project. " +
				"Pass the project id (or the folder path to start a new project). Text inside <private> tags is not stored.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"text"},
				"properties": map[string]any{
					"text":     map[string]any{"type": "string", "description": "What to remember, self-contained"},
					"title":    map[string]any{"type": "string", "description": "Short title (derived from the text if omitted)"},
					"project":  map[string]any{"type": "string", "description": "Project id or unique name, from project_suggest, project_list or project_resolve"},
					"path":     map[string]any{"type": "string", "description": "Absolute folder path; use instead of project to write to (or start) the project for that folder"},
					"type":     map[string]any{"type": "string", "enum": []string{"decision", "bugfix", "feature", "refactor", "discovery", "change"}, "default": "discovery"},
					"concepts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Short topic tags"},
					"scope":    map[string]any{"type": "string", "enum": []string{"project", "global"}, "default": "project"},
				},
			},
		},
	}
}

// isDesktopTool reports whether name is one of the tools added by Desktop mode.
func isDesktopTool(name string) bool {
	switch name {
	case "project_suggest", "project_resolve", "project_list", "context", "remember", "project_manage":
		return true
	}
	return false
}

// callDesktopTool runs a Desktop-mode tool.
func (s *Server) callDesktopTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "project_list":
		return s.proxyGetRaw(ctx, "/api/projects/summary", nil)
	case "project_resolve":
		return s.toolProjectResolve(ctx, args)
	case "project_suggest":
		return s.toolProjectSuggest(ctx, args)
	case "context":
		return s.toolContext(ctx, args)
	case "remember":
		return s.toolRemember(ctx, args)
	case "project_manage":
		return s.toolProjectManage(ctx, args)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// resolution mirrors the worker's /api/projects/resolve answer.
type resolution struct {
	ID         string   `json:"id"`
	Match      string   `json:"match"`
	Candidates []string `json:"candidates"`
	Known      bool     `json:"known"`
}

func (s *Server) resolveRef(ctx context.Context, key, value string) (resolution, error) {
	raw, err := s.proxyGetRaw(ctx, "/api/projects/resolve", map[string]string{key: value})
	if err != nil {
		return resolution{}, err
	}
	var r resolution
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return resolution{}, fmt.Errorf("decode resolve response: %w", err)
	}
	return r, nil
}

func (s *Server) toolProjectResolve(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path, Name, ID string
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("project_resolve: invalid arguments: %w", err)
	}
	if a.Path == "" && a.Name == "" && a.ID == "" {
		return "", fmt.Errorf("project_resolve: pass path, name or id")
	}
	return s.proxyGetRaw(ctx, "/api/projects/resolve", map[string]string{"path": a.Path, "name": a.Name, "id": a.ID})
}

func (s *Server) toolProjectSuggest(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		OpeningText string `json:"opening_text"`
		Limit       int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("project_suggest: invalid arguments: %w", err)
	}
	params := map[string]string{"query": a.OpeningText}
	if a.Limit > 0 {
		params["limit"] = strconv.Itoa(a.Limit)
	}
	out, err := s.proxyGetRaw(ctx, "/api/projects/suggest", params)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out) + "\n\nNext: ask the user which project to use, or whether to continue without one (read-only). " +
		"Then call context with the chosen project id. If they decline, do not call remember.", nil
}

// projectFromArgs resolves the project a Desktop tool should act on. A path
// resolves to the id Claude Code would use, a project value may be an id, an
// alias or a unique name. allowNew is true only when the id came from a path.
func (s *Server) projectFromArgs(ctx context.Context, tool, project, path string) (id string, allowNew bool, err error) {
	switch {
	case strings.TrimSpace(path) != "":
		r, rerr := s.resolveRef(ctx, "path", path)
		if rerr != nil {
			return "", false, rerr
		}
		if r.ID == "" {
			return "", false, fmt.Errorf("%s: %q is not an absolute folder path", tool, path)
		}
		return r.ID, true, nil

	case strings.TrimSpace(project) != "":
		r, rerr := s.resolveRef(ctx, "id", project)
		if rerr != nil {
			return "", false, rerr
		}
		if r.ID == "" {
			// Not an id or alias: accept a unique project name.
			if r, rerr = s.resolveRef(ctx, "name", project); rerr != nil {
				return "", false, rerr
			}
		}
		if r.ID == "" {
			msg := fmt.Sprintf("%s: unknown project %q", tool, project)
			if len(r.Candidates) > 0 {
				msg += "; did you mean one of: " + strings.Join(r.Candidates, ", ")
			}
			return "", false, fmt.Errorf("%s; use project_list or project_suggest to find the right id", msg)
		}
		return r.ID, false, nil

	case !s.desktop() || s.projectPinned:
		// Claude Code (or an explicitly pinned project): the server's own project is the default.
		if s.project != "" {
			return s.project, true, nil
		}
	}
	return "", false, fmt.Errorf("%s: a project is required. Call project_suggest (or project_resolve with the folder path), "+
		"let the user choose a project, then pass its id. If they declined, this chat stays read-only", tool)
}

func (s *Server) toolContext(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Project string `json:"project"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("context: invalid arguments: %w", err)
	}
	id, _, err := s.projectFromArgs(ctx, "context", a.Project, a.Path)
	if err != nil {
		return "", err
	}
	params := map[string]string{"project": id}
	if a.Path != "" {
		params["cwd"] = a.Path
	}
	return s.proxyGetRaw(ctx, "/api/context/inject", params)
}

func (s *Server) toolRemember(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text     string   `json:"text"`
		Title    string   `json:"title"`
		Project  string   `json:"project"`
		Path     string   `json:"path"`
		Type     string   `json:"type"`
		Scope    string   `json:"scope"`
		Concepts []string `json:"concepts"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("remember: invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Text) == "" {
		return "", fmt.Errorf("remember: text is required")
	}
	id, allowNew, err := s.projectFromArgs(ctx, "remember", a.Project, a.Path)
	if err != nil {
		return "", err
	}

	raw, err := s.proxyPostRaw(ctx, "/api/observations/remember", map[string]any{
		"project":           id,
		"title":             a.Title,
		"text":              a.Text,
		"type":              a.Type,
		"scope":             a.Scope,
		"concepts":          a.Concepts,
		"source":            s.getClientName(),
		"allow_new_project": allowNew,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		Project   string `json:"project"`
		ID        int64  `json:"id"`
		Duplicate bool   `json:"duplicate"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return "", fmt.Errorf("decode remember response: %w", err)
	}
	if resp.Duplicate {
		return fmt.Sprintf("Already saved as observation #%d in project %s.", resp.ID, resp.Project), nil
	}
	return fmt.Sprintf("Saved observation #%d to project %s.", resp.ID, resp.Project), nil
}

// searchAcrossProjects serves search when no project is chosen.
func (s *Server) searchAcrossProjects(ctx context.Context, args searchArgs) (string, error) {
	params := map[string]string{"query": args.Query}
	if args.Limit > 0 {
		params["limit"] = strconv.Itoa(args.Limit)
	}
	if args.ObsType != "" {
		params["obs_type"] = args.ObsType
	}
	return s.proxyGetRaw(ctx, "/api/search/cross-project", params)
}

// proxyDeleteRaw sends a DELETE to the worker.
func (s *Server) proxyDeleteRaw(ctx context.Context, path string, params map[string]string) (string, error) {
	if s.client == nil {
		return "", fmt.Errorf("worker unavailable at %s: http client not configured", s.workerURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.workerURL+path, nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	req.URL.RawQuery = q.Encode()

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("worker unavailable at %s: %w", s.workerURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read worker response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("worker returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

// previewNote is appended to every destructive preview so the instruction sits
// next to the token, where the model is looking when it decides what to do next.
const previewNote = "\n\nThis is only a PREVIEW; nothing has changed. Show the user what would happen and ask for their explicit approval. " +
	"Only if they approve, call project_manage again with the same arguments plus this confirm token. Do not proceed on your own."

func (s *Server) toolProjectManage(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Action  string `json:"action"`
		Project string `json:"project"`
		Into    string `json:"into"`
		Alias   string `json:"alias"`
		Confirm string `json:"confirm"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("project_manage: invalid arguments: %w", err)
	}
	need := func(field, value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("project_manage %s: %s is required", a.Action, field)
		}
		return nil
	}

	switch a.Action {
	case "stats":
		if err := need("project", a.Project); err != nil {
			return "", err
		}
		return s.proxyGetRaw(ctx, "/api/projects/"+url.PathEscape(a.Project)+"/stats", nil)

	case "delete":
		if err := need("project", a.Project); err != nil {
			return "", err
		}
		raw, err := s.proxyDeleteRaw(ctx, "/api/projects/"+url.PathEscape(a.Project), map[string]string{"confirm": a.Confirm})
		if err != nil {
			return "", err
		}
		return describeAdminResult(raw)

	case "merge":
		if err := need("project", a.Project); err != nil {
			return "", err
		}
		if err := need("into", a.Into); err != nil {
			return "", err
		}
		raw, err := s.proxyPostRaw(ctx, "/api/projects/"+url.PathEscape(a.Project)+"/merge", map[string]string{"into": a.Into, "confirm": a.Confirm})
		if err != nil {
			return "", err
		}
		return describeAdminResult(raw)

	case "alias":
		if err := need("alias", a.Alias); err != nil {
			return "", err
		}
		if err := need("project", a.Project); err != nil {
			return "", err
		}
		return s.proxyPostRaw(ctx, "/api/projects/aliases", map[string]string{"alias": a.Alias, "canonical": a.Project, "source": "mcp"})

	case "unalias":
		if err := need("alias", a.Alias); err != nil {
			return "", err
		}
		if _, err := s.proxyDeleteRaw(ctx, "/api/projects/aliases/"+url.PathEscape(a.Alias), nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("Removed alias %s.", a.Alias), nil
	}
	return "", fmt.Errorf("project_manage: unknown action %q (use stats, delete, merge, alias or unalias)", a.Action)
}

// describeAdminResult turns the worker's delete/merge answer into text for the
// model: the message, plus the approval instruction when it was only a preview.
func describeAdminResult(raw string) (string, error) {
	var r struct {
		Message string `json:"message"`
		Confirm string `json:"confirm"`
		DryRun  bool   `json:"dry_run"`
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return "", fmt.Errorf("decode worker response: %w", err)
	}
	if r.DryRun {
		return r.Message + previewNote + "\nconfirm: " + r.Confirm, nil
	}
	return r.Message, nil
}
