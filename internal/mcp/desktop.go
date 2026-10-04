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

// memoryBlurb says what claude-mnemonic is in the words people use, so a chat that has its own
// built-in memory also reaches for this one when the person talks about memory or past work.
// Chat shows only tool descriptions (not server instructions), so this text leads the key ones.
const memoryBlurb = "claude-mnemonic is the user's persistent project memory, shared with Claude Code: decisions, findings and fixes from earlier sessions. " +
	"Use it, in addition to any built-in memory, whenever the user asks about their past work, earlier decisions or project history, " +
	"or talks about memory or remembering things for their projects, and say which source an answer came from. " +
	"It is not needed for general questions that do not refer to the user's own earlier work."

// memoryPrefix starts the descriptions of the other tools, which only need to say what they belong to.
const memoryPrefix = "claude-mnemonic memory (the user's persistent project memory, shared with Claude Code): "

// withMemoryDescriptions returns the base tools with the descriptions of the ones a person's
// memory questions should reach (search, timeline) led by what claude-mnemonic is. The original text
// is kept after it. Desktop mode only: Claude Code's own tool list is never changed.
func withMemoryDescriptions(tools []Tool) []Tool {
	out := make([]Tool, len(tools))
	copy(out, tools)
	for i := range out {
		switch out[i].Name {
		case "search":
			out[i].Description = memoryPrefix + "search it for earlier decisions, findings, fixes and project history; " +
				"with no project chosen it searches every project. " + out[i].Description
		case "timeline":
			out[i].Description = memoryPrefix + out[i].Description
		}
	}
	return out
}

// desktopInstructions is returned in the initialize result. Some clients show
// it to the model, others (Desktop chat) do not, so the same protocol is also
// written into the tool descriptions.
const desktopInstructions = memoryBlurb + `

claude-mnemonic keeps memory per project. This client has no working directory, so choose a project explicitly:
- If a project folder is open, call project_resolve with its absolute path, then context with the returned id.
- Otherwise call project_suggest with the user's first message, offer the user the candidates (plus "none"), and wait for their choice.
- If the user declines, stay read-only: use search and catch_up without saving, and never call remember or checkpoint.
- Pass the chosen project id to remember, checkpoint, catch_up and context on every call.
- When the user asks to see, open or manage their memory in a browser, call dashboard and give them the link.
- Once a project is chosen, keep one checkpoint per thread of work current. If the conversation was compacted and you lost the thread, call catch_up.`

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
			Description: memoryBlurb + " START HERE for any such memory question in a conversation that has no project folder. Pass the user's first message; returns candidate projects ranked by content, name and recency. " +
				"Then ASK the user which project to use or whether to continue WITHOUT one (read-only). Do not pick for them; if confident is true you may propose the top one for confirmation. " +
				"Show projects to the user by their label and pass the matching use value in later calls (the project's name, or its id when two projects share a name). " +
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
			Description: memoryPrefix + "map a project reference to its canonical project id. When a project folder is open, pass its absolute path (from your session context): this reproduces the id Claude Code uses for the same folder. " +
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
			Description: memoryPrefix + "list all projects with session and observation counts and last activity, most recent first. Show each project by its label; pass its use value (name, or id when two projects share a name) to other tools.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name: "context",
			Description: memoryPrefix + "load the saved context for a chosen project (what Claude Code receives at session start): recent observations and decisions, " +
				"preceded by the project's short dated brief when one has been written. Call it once after the user picks a project. Not for declined chats.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "The project's use value from project_suggest or project_list: its name, or its id when two projects share a name"},
					"path":    map[string]any{"type": "string", "description": "Alternatively the absolute folder path"},
				},
			},
		},
		{
			Name: "related",
			Description: memoryPrefix + "how a saved note is connected to others: what it fixes, builds on or evolved from, and what came after it, each with the id, how sure the graph is and why. " +
				"Use it to follow the history behind a decision or a fix (\"what led to this?\", \"was this ever fixed?\", \"what else is about this?\"). " +
				"Give the id of a note (search results and earlier answers of this tool show ids), or a query and the project to find the note in. Read-only.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":             map[string]any{"type": "number", "description": "The note's id, from a search result or from an earlier answer of this tool"},
					"query":          map[string]any{"type": "string", "description": "Instead of an id: words that find the note, within project"},
					"project":        map[string]any{"type": "string", "description": "With query: the project's use value from project_suggest or project_list"},
					"types":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"relates_to", "fixes", "depends_on", "evolves_from", "supersedes", "causes"}}, "description": "Only these kinds of relation (relation_types lists them)"},
					"direction":      map[string]any{"type": "string", "enum": []string{"older", "newer"}, "description": "older: what this note came from; newer: what came after it"},
					"min_confidence": map[string]any{"type": "number", "minimum": 0.0, "maximum": 1.0, "description": "Only relations the graph is at least this sure of"},
					"limit":          map[string]any{"type": "number", "default": 20, "minimum": 1, "maximum": 100},
				},
			},
		},
		{
			Name: "dashboard",
			Description: memoryPrefix + "the address of the web dashboard, where the user can browse and manage their saved notes, session summaries, the knowledge graph, conflicts to review, scopes and projects. " +
				"Call it when the user asks to see, open or manage their memory in a browser, and give them the link: it opens on their computer. Read-only.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "relation_types",
			Description: memoryPrefix + "the kinds of connection between notes (relates_to, fixes, depends_on, evolves_from, supersedes, causes), what each means, and how many there are in a project, around one note, or everywhere. Read-only.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "Count within this project (its use value from project_suggest or project_list)"},
					"id":      map[string]any{"type": "number", "description": "Count the relations around this note"},
				},
			},
		},
		{
			Name: "project_manage",
			Description: "Inspect, alias, merge or delete projects. stats is read-only. delete and merge are DESTRUCTIVE: first call WITHOUT confirm to get a preview (nothing changes), " +
				"show the user exactly what would be removed or moved and get their explicit approval, then repeat the same call with the confirm token from the preview. " +
				"Never invent or reuse a token, and never confirm without asking the user. A backup of the database is taken automatically before any change. " +
				"Pass a project's `use` value from project_list: its name when that is unique, its id when two projects share a name. Names must match exactly; a name shared by several projects is refused, " +
				"so ask the user which one they mean. Aliases and partial names are never accepted for these actions.",
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
			Description: memoryPrefix + "save durable knowledge (a decision, a finding, a fix), also when the user says to remember something about their project. Only after the user has chosen a project: never in a declined, read-only chat, and never with a guessed project. " +
				"Pass the project id (or the folder path to start a new project). Text inside <private> tags is not stored.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"text"},
				"properties": map[string]any{
					"text":     map[string]any{"type": "string", "description": "What to remember, self-contained"},
					"title":    map[string]any{"type": "string", "description": "Short title (derived from the text if omitted)"},
					"project":  map[string]any{"type": "string", "description": "The project's use value from project_suggest or project_list: its name, or its id when two projects share a name"},
					"path":     map[string]any{"type": "string", "description": "Absolute folder path; use instead of project to write to (or start) the project for that folder"},
					"type":     map[string]any{"type": "string", "enum": []string{"decision", "bugfix", "feature", "refactor", "discovery", "change"}, "default": "discovery"},
					"concepts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Short topic tags"},
					"scope":    map[string]any{"type": "string", "enum": []string{"project", "global"}, "default": "project"},
				},
			},
		},
		{
			Name: "checkpoint",
			Description: memoryPrefix + "save the current state of the line of work you are on, so it can be picked up after the conversation is compacted or in a new chat. " +
				"One note per thread: calling it again with the same thread name replaces the note, so keep it current. Call it after meaningful progress or a decision, and before a long conversation gets summarised. " +
				"Only after the user has chosen a project: never in a declined, read-only chat, and never with a guessed project. Text inside <private> tags is not stored.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"thread"},
				"properties": map[string]any{
					"thread":     map[string]any{"type": "string", "description": "Short name of the line of work, e.g. \"Overlay design\". The same name updates the same note"},
					"goal":       map[string]any{"type": "string", "description": "What this thread is trying to achieve"},
					"progress":   map[string]any{"type": "string", "description": "Where it stands now: what is done, what was tried"},
					"decisions":  map[string]any{"type": "string", "description": "What was decided and why"},
					"next_steps": map[string]any{"type": "string", "description": "What is still open or comes next"},
					"project":    map[string]any{"type": "string", "description": "The project's use value from project_suggest or project_list: its name, or its id when two projects share a name"},
					"path":       map[string]any{"type": "string", "description": "Absolute folder path; use instead of project to write to (or start) the project for that folder"},
				},
			},
		},
		{
			Name: "catch_up",
			Description: memoryPrefix + "recover where the work stood: the project's short dated brief when there is one, the user's open threads (goal, progress, decisions, next steps), most recently worked on first, plus the project's latest decisions. " +
				"Call it after the user picks a project when they are continuing earlier work, and whenever you notice you have lost the thread of what you were doing (for example after the conversation was compacted). " +
				"Read-only, so it is also fine in a chat where the user declined to save anything.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string", "description": "The project's use value from project_suggest or project_list: its name, or its id when two projects share a name"},
					"path":    map[string]any{"type": "string", "description": "Alternatively the absolute folder path"},
					"threads": map[string]any{"type": "number", "default": 5, "minimum": 1, "maximum": 20, "description": "How many threads to return"},
				},
			},
		},
	}
}

// isDesktopTool reports whether name is one of the tools added by Desktop mode.
func isDesktopTool(name string) bool {
	switch name {
	case "project_suggest", "project_resolve", "project_list", "context", "remember", "project_manage", "checkpoint", "catch_up", "related", "relation_types", "dashboard":
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
	case "checkpoint":
		return s.toolCheckpoint(ctx, args)
	case "catch_up":
		return s.toolCatchUp(ctx, args)
	case "dashboard":
		return s.toolDashboard(), nil
	case "related":
		return s.toolRelated(ctx, args)
	case "relation_types":
		return s.toolRelationTypes(ctx, args)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// toolDashboard says where the web dashboard is: the worker's own address, on the port this server was started with.
// The tool call already made sure the worker is running. Desktop cannot open a local page itself; the user clicks the
// link and it opens on their computer.
func (s *Server) toolDashboard() string {
	url := strings.TrimRight(s.workerURL, "/")
	return fmt.Sprintf("The memory dashboard is at %s . Give the user this link: it opens in their browser on this computer. "+
		"It shows their saved notes, session summaries, the knowledge graph, conflicts to review, scopes, and project management "+
		"(merge, delete, possible duplicates). It is served by the local worker, so it only works on this computer.", url)
}

// resolution mirrors the worker's /api/projects/resolve answer.
type resolution struct {
	ID               string            `json:"id"`
	Match            string            `json:"match"`
	Candidates       []string          `json:"candidates"`
	CandidateDetails []candidateDetail `json:"candidate_details"`
	Known            bool              `json:"known"`
	Ambiguous        bool              `json:"ambiguous"`
}

// candidateDetail is one project among several that share a name, with what tells them apart.
type candidateDetail struct {
	Project string `json:"project"`
	Label   string `json:"label"`
	Detail  string `json:"detail"`
}

// describeCandidates lists projects with their ids and what distinguishes them.
func describeCandidates(details []candidateDetail, fallbackIDs []string) string {
	if len(details) == 0 {
		return strings.Join(fallbackIDs, ", ")
	}
	parts := make([]string, 0, len(details))
	for i, d := range details {
		parts = append(parts, fmt.Sprintf("(%c) %s: %s", 'a'+i, d.Project, d.Detail))
	}
	return strings.Join(parts, "; ")
}

// unresolvedMessage explains why a project reference matched nothing usable. Several projects
// with exactly the requested name are namesakes the person must choose between; anything else
// is a near miss or an unknown name.
func unresolvedMessage(tool, ref string, r resolution) string {
	if r.Ambiguous {
		return fmt.Sprintf("%s: %d projects are called %q: %s. Ask the user which one they mean, then pass that project's id.",
			tool, len(r.Candidates), ref, describeCandidates(r.CandidateDetails, r.Candidates))
	}
	msg := fmt.Sprintf("%s: unknown project %q", tool, ref)
	if len(r.Candidates) > 0 {
		msg += "; did you mean one of: " + describeCandidates(r.CandidateDetails, r.Candidates)
	}
	return msg + "; use project_list or project_suggest to see the projects"
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
		"Refer to each project by its label, and pass its use value in later calls. " +
		"Then call context with the chosen project. If they decline, do not call remember.", nil
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
			return "", false, fmt.Errorf("%s", unresolvedMessage(tool, project, r))
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
	out, err := s.proxyGetRaw(ctx, "/api/context/inject", params)
	if err != nil {
		return "", err
	}
	if brief := s.projectBrief(ctx, id); brief != "" {
		return "Project brief (it can lag behind recent work):\n\n" + brief + "\n\n---\nSaved observations (raw):\n" + out, nil
	}
	return out, nil
}

// projectBrief returns the project's brief text, or "" when it has none or the worker cannot say: the
// brief is an extra, so its absence never changes or breaks what the tool returns without it.
func (s *Server) projectBrief(ctx context.Context, project string) string {
	raw, err := s.proxyGetRaw(ctx, "/api/projects/"+url.PathEscape(project)+"/brief", nil)
	if err != nil {
		return ""
	}
	var b struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(raw), &b) != nil {
		return ""
	}
	return strings.TrimSpace(b.Text)
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

func (s *Server) toolCheckpoint(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Thread    string `json:"thread"`
		Goal      string `json:"goal"`
		Progress  string `json:"progress"`
		Decisions string `json:"decisions"`
		NextSteps string `json:"next_steps"`
		Project   string `json:"project"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("checkpoint: invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Thread) == "" {
		return "", fmt.Errorf("checkpoint: thread is required: give the line of work a short name")
	}
	id, allowNew, err := s.projectFromArgs(ctx, "checkpoint", a.Project, a.Path)
	if err != nil {
		return "", err
	}

	raw, err := s.proxyPostRaw(ctx, "/api/threads/checkpoint", map[string]any{
		"project":           id,
		"thread":            a.Thread,
		"goal":              a.Goal,
		"progress":          a.Progress,
		"decisions":         a.Decisions,
		"next_steps":        a.NextSteps,
		"source":            s.getClientName(),
		"allow_new_project": allowNew,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		Project string `json:"project"`
		Thread  string `json:"thread"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return "", fmt.Errorf("decode checkpoint response: %w", err)
	}
	if resp.Created {
		return fmt.Sprintf("Saved a new note for thread %q in project %s.", resp.Thread, resp.Project), nil
	}
	return fmt.Sprintf("Updated the note for thread %q in project %s.", resp.Thread, resp.Project), nil
}

// catchUpDigest mirrors the worker's catch-up answer.
type catchUpDigest struct {
	Brief *struct {
		Text   string `json:"text"`
		Source string `json:"source"`
		AsOf   string `json:"as_of"`
	} `json:"brief"`
	Project string `json:"project"`
	Threads []struct {
		Thread    string `json:"thread"`
		Goal      string `json:"goal"`
		Progress  string `json:"progress"`
		Decisions string `json:"decisions"`
		NextSteps string `json:"next_steps"`
		UpdatedAt string `json:"updated_at"`
	} `json:"threads"`
	Decisions []struct {
		Title     string `json:"title"`
		Subtitle  string `json:"subtitle"`
		CreatedAt string `json:"created_at"`
	} `json:"decisions"`
}

func (s *Server) toolCatchUp(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Project string `json:"project"`
		Path    string `json:"path"`
		Threads int    `json:"threads"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("catch_up: invalid arguments: %w", err)
	}
	id, _, err := s.projectFromArgs(ctx, "catch_up", a.Project, a.Path)
	if err != nil {
		return "", err
	}
	params := map[string]string{}
	if a.Threads > 0 {
		params["threads"] = strconv.Itoa(a.Threads)
	}
	raw, err := s.proxyGetRaw(ctx, "/api/projects/"+url.PathEscape(id)+"/catch-up", params)
	if err != nil {
		return "", err
	}
	var d catchUpDigest
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return "", fmt.Errorf("decode catch_up response: %w", err)
	}
	return renderCatchUp(d), nil
}

// renderCatchUp writes the digest as text a model can act on straight away.
func renderCatchUp(d catchUpDigest) string {
	var b strings.Builder
	if d.Brief != nil && strings.TrimSpace(d.Brief.Text) != "" {
		fmt.Fprintf(&b, "Project brief for %s (it can lag behind recent work; the notes below are newer):\n\n%s\n\n---\n", d.Project, d.Brief.Text)
	}
	if len(d.Threads) == 0 {
		fmt.Fprintf(&b, "No thread notes are saved for project %s yet.\n", d.Project)
	} else {
		fmt.Fprintf(&b, "Where the work stood in project %s (most recently worked on first):\n", d.Project)
	}
	for _, t := range d.Threads {
		fmt.Fprintf(&b, "\nThread: %s (updated %s)\n", t.Thread, t.UpdatedAt)
		for _, f := range []struct{ label, text string }{
			{"Goal", t.Goal}, {"Progress", t.Progress}, {"Decisions", t.Decisions}, {"Next", t.NextSteps},
		} {
			if f.text != "" {
				fmt.Fprintf(&b, "  %s: %s\n", f.label, f.text)
			}
		}
	}
	if len(d.Decisions) > 0 {
		b.WriteString("\nRecent decisions in this project:\n")
		for _, x := range d.Decisions {
			if x.Subtitle != "" {
				fmt.Fprintf(&b, "- %s: %s (%s)\n", x.Title, x.Subtitle, x.CreatedAt)
			} else {
				fmt.Fprintf(&b, "- %s (%s)\n", x.Title, x.CreatedAt)
			}
		}
	}
	if len(d.Threads) == 0 && len(d.Decisions) == 0 && d.Brief == nil {
		b.WriteString("Nothing to recover from here: call context for the project's saved observations.\n")
	} else {
		b.WriteString("\nUse this to continue where things left off. Say so if it looks out of date, and keep it current with checkpoint when the user chose to save.\n")
	}
	return b.String()
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

	var err error
	switch a.Action {
	case "stats":
		if err := need("project", a.Project); err != nil {
			return "", err
		}
		project, err := s.manageRef(ctx, "stats", a.Project)
		if err != nil {
			return "", err
		}
		return s.proxyGetRaw(ctx, "/api/projects/"+url.PathEscape(project)+"/stats", nil)

	case "delete":
		if err := need("project", a.Project); err != nil {
			return "", err
		}
		project, err := s.manageRef(ctx, "delete", a.Project)
		if err != nil {
			return "", err
		}
		raw, err := s.proxyDeleteRaw(ctx, "/api/projects/"+url.PathEscape(project), map[string]string{"confirm": a.Confirm})
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
		project, err := s.manageRef(ctx, "merge", a.Project)
		if err != nil {
			return "", err
		}
		into, err := s.manageRef(ctx, "merge", a.Into)
		if err != nil {
			return "", err
		}
		raw, err := s.proxyPostRaw(ctx, "/api/projects/"+url.PathEscape(project)+"/merge", map[string]string{"into": into, "confirm": a.Confirm})
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
		a.Project, err = s.manageRef(ctx, "alias", a.Project)
		if err != nil {
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

// manageRef turns what the model passed for a project into the id to act on.
//
// Destructive actions must be unambiguous, so this is strict: an exact id of a real project is used
// as is; an exact name (case-insensitive) is accepted only if it identifies exactly one real project;
// a name shared by several is refused with what tells them apart. Aliases and partial names are never
// resolved. Anything else is passed through untouched, so the worker's own refusals (an alias, a
// missing project) still apply.
func (s *Server) manageRef(ctx context.Context, action, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	raw, err := s.proxyGetRaw(ctx, "/api/projects/summary", nil)
	if err != nil {
		return "", err
	}
	var rows []struct {
		Project     string `json:"project"`
		DisplayName string `json:"display_name"`
		AliasOf     string `json:"alias_of"`
		Label       string `json:"label"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return "", fmt.Errorf("decode project list: %w", err)
	}

	var named []candidateDetail
	for _, r := range rows {
		if r.AliasOf != "" {
			continue // an alias is not a project: never matched by name here
		}
		if r.Project == ref {
			return ref, nil
		}
		if strings.EqualFold(r.DisplayName, ref) {
			named = append(named, candidateDetail{Project: r.Project, Label: r.Label, Detail: strings.TrimSuffix(strings.TrimPrefix(r.Label, r.DisplayName+" ("), ")")})
		}
	}
	switch len(named) {
	case 0:
		return ref, nil
	case 1:
		return named[0].Project, nil
	}
	ids := make([]string, len(named))
	for i, n := range named {
		ids[i] = n.Project
	}
	return "", fmt.Errorf("%s", unresolvedMessage("project_manage "+action, ref,
		resolution{Ambiguous: true, Candidates: ids, CandidateDetails: named}))
}
