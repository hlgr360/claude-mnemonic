package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordedRequest is one request the fake worker received.
type recordedRequest struct {
	query  map[string]string
	body   map[string]any
	method string
	path   string
}

// fakeWorker is an HTTP stand-in for the worker. routes maps "METHOD /path" to a handler;
// anything else answers 404 so unexpected calls fail loudly.
type fakeWorker struct {
	*httptest.Server
	seen []recordedRequest
	mu   sync.Mutex
}

func newFakeWorker(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) *fakeWorker {
	t.Helper()
	fw := &fakeWorker{}
	fw.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordedRequest{method: r.Method, path: r.URL.Path, query: map[string]string{}}
		for k := range r.URL.Query() {
			rec.query[k] = r.URL.Query().Get(k)
		}
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &rec.body)
			}
			r.Body = io.NopCloser(bytes.NewReader(raw)) // handlers read the body too
		}
		fw.mu.Lock()
		fw.seen = append(fw.seen, rec)
		fw.mu.Unlock()

		if h, ok := routes[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fw.Close)
	return fw
}

func (fw *fakeWorker) requests(path string) []recordedRequest {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	var out []recordedRequest
	for _, r := range fw.seen {
		if r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func (fw *fakeWorker) total() int {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return len(fw.seen)
}

func jsonReply(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

func desktopServer(t *testing.T, fw *fakeWorker) *Server {
	t.Helper()
	s := NewServer(fw.Client(), fw.URL, "code-project_111111", "test")
	initialize(t, s, "claude-ai")
	return s
}

func initialize(t *testing.T, s *Server, client string) map[string]any {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"clientInfo": map[string]any{"name": client}})
	resp := s.handleInitialize(&Request{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: params})
	require.Nil(t, resp.Error)
	return resp.Result.(map[string]any)
}

func toolNames(t *testing.T, s *Server) []string {
	t.Helper()
	resp := s.handleToolsList(&Request{JSONRPC: "2.0", ID: 2})
	var names []string
	for _, tool := range resp.Result.(map[string]any)["tools"].([]Tool) {
		names = append(names, tool.Name)
	}
	return names
}

func call(s *Server, name string, args any) (string, error) {
	raw, _ := json.Marshal(args)
	return s.callTool(context.Background(), name, raw)
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeAuto, "auto": ModeAuto, " Desktop ": ModeDesktop, "CODE": ModeCode} {
		got, err := ParseMode(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := ParseMode("browser")
	assert.Error(t, err)
}

func TestIsDesktopClient(t *testing.T) {
	for name, want := range map[string]bool{
		"claude-ai":                   true,
		"Claude-AI":                   true,
		"local-agent-mode-mcp-probe":  true,
		"local-agent-mode-claude-mnc": true,
		"claude-code":                 false,
		"":                            false,
		"cursor":                      false,
	} {
		assert.Equal(t, want, IsDesktopClient(name), name)
	}
}

func TestInitialize_ModeDetectionAndInstructions(t *testing.T) {
	tests := []struct {
		name        string
		mode        Mode
		client      string
		wantDesktop bool
	}{
		{"chat client", ModeAuto, "claude-ai", true},
		{"cowork or code tab", ModeAuto, "local-agent-mode-claude-mnemonic", true},
		{"claude code", ModeAuto, "claude-code", false},
		{"no client info", ModeAuto, "", false},
		{"forced desktop", ModeDesktop, "claude-code", true},
		{"forced code overrides a desktop client", ModeCode, "claude-ai", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewServer(nil, "", "proj_aaaaaa", "v")
			s.SetMode(tt.mode)
			result := initialize(t, s, tt.client)

			assert.Equal(t, tt.wantDesktop, s.desktop())
			instr, has := result["instructions"]
			assert.Equal(t, tt.wantDesktop, has, "instructions are sent only in desktop mode")
			if has {
				assert.Contains(t, instr, "project_suggest")
				assert.Contains(t, instr, "never call remember")
			}
		})
	}
}

func TestInitialize_WithoutParamsKeepsCodeBehaviour(t *testing.T) {
	s := NewServer(nil, "", "proj_aaaaaa", "v")
	resp := s.handleInitialize(&Request{JSONRPC: "2.0", ID: 1, Method: "initialize"})
	assert.NotContains(t, resp.Result.(map[string]any), "instructions")
	assert.False(t, s.desktop())
	assert.Equal(t, "proj_aaaaaa", s.defaultProject())
}

func TestDefaultProject(t *testing.T) {
	t.Run("code keeps its project", func(t *testing.T) {
		s := NewServer(nil, "", "proj_aaaaaa", "v")
		initialize(t, s, "claude-code")
		assert.Equal(t, "proj_aaaaaa", s.defaultProject())
	})
	t.Run("desktop starts unbound", func(t *testing.T) {
		s := NewServer(nil, "", "derived-from-cwd_aaaaaa", "v")
		initialize(t, s, "claude-ai")
		assert.Equal(t, "", s.defaultProject(), "a Desktop server's cwd says nothing about the user's project")
	})
	t.Run("an explicit --project is kept in desktop", func(t *testing.T) {
		s := NewServer(nil, "", "chosen_aaaaaa", "v")
		s.SetProjectPinned(true)
		initialize(t, s, "claude-ai")
		assert.Equal(t, "chosen_aaaaaa", s.defaultProject())
	})
	t.Run("before initialize the project is untouched", func(t *testing.T) {
		assert.Equal(t, "p_aaaaaa", NewServer(nil, "", "p_aaaaaa", "v").defaultProject())
	})
}

func TestToolsList_DesktopToolsOnlyInDesktopMode(t *testing.T) {
	code := NewServer(nil, "", "p_aaaaaa", "v")
	initialize(t, code, "claude-code")
	codeTools := toolNames(t, code)
	for _, n := range []string{"project_suggest", "project_resolve", "project_list", "context", "remember", "project_manage", "checkpoint", "catch_up", "related", "relation_types", "dashboard"} {
		assert.NotContains(t, codeTools, n, "Code's tool list must not change")
	}

	desktop := NewServer(nil, "", "p_aaaaaa", "v")
	initialize(t, desktop, "claude-ai")
	desktopTools := toolNames(t, desktop)
	for _, n := range []string{"project_suggest", "project_resolve", "project_list", "context", "remember", "project_manage", "checkpoint", "catch_up", "related", "relation_types", "dashboard"} {
		assert.Contains(t, desktopTools, n)
	}
	assert.Len(t, desktopTools, len(codeTools)+11, "desktop adds exactly the eight project tools, the two relation tools and the dashboard link")
}

func TestToolDescriptions_CarryTheProtocol(t *testing.T) {
	// Chat does not show server instructions, so the descriptions must say it.
	byName := map[string]string{}
	for _, tool := range desktopTools() {
		byName[tool.Name] = tool.Description
	}
	assert.Contains(t, byName["project_suggest"], "ASK the user")
	assert.Contains(t, byName["project_suggest"], "WITHOUT one")
	assert.Contains(t, byName["remember"], "never in a declined")
	assert.Contains(t, byName["remember"], "never with a guessed project")
	assert.Contains(t, byName["project_resolve"], "absolute path")
	assert.Contains(t, byName["checkpoint"], "never in a declined")
	assert.Contains(t, byName["checkpoint"], "same thread name")
	assert.Contains(t, byName["catch_up"], "compacted")
	assert.Contains(t, byName["catch_up"], "Read-only")

	for _, tool := range desktopTools() {
		assert.Equal(t, "object", tool.InputSchema["type"], tool.Name)
	}
}

func TestDesktopToolsAreUnknownInCodeMode(t *testing.T) {
	s := NewServer(nil, "", "p_aaaaaa", "v")
	initialize(t, s, "claude-code")

	for _, name := range []string{"remember", "project_list", "context", "project_suggest", "project_resolve", "project_manage", "checkpoint", "catch_up"} {
		_, err := call(s, name, map[string]any{"text": "x"})
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "unknown tool", name)
	}
}

func TestSearch_RoutesByWhetherAProjectIsChosen(t *testing.T) {
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/context/search":       jsonReply(`{"observations":[]}`),
		"GET /api/search/cross-project": jsonReply(`{"observations":[{"id":1}]}`),
	})

	t.Run("desktop with no project searches everything", func(t *testing.T) {
		s := desktopServer(t, fw)
		out, err := call(s, "search", map[string]any{"query": "vector", "limit": 5, "obs_type": "decision"})
		require.NoError(t, err)
		assert.Contains(t, out, `"id":1`)
		reqs := fw.requests("/api/search/cross-project")
		require.Len(t, reqs, 1)
		assert.Equal(t, map[string]string{"query": "vector", "limit": "5", "obs_type": "decision"}, reqs[0].query)
	})
	t.Run("desktop with an explicit project uses the project search", func(t *testing.T) {
		s := desktopServer(t, fw)
		_, err := call(s, "search", map[string]any{"query": "vector", "project": "repo_aaaaaa"})
		require.NoError(t, err)
		reqs := fw.requests("/api/context/search")
		require.NotEmpty(t, reqs)
		assert.Equal(t, "repo_aaaaaa", reqs[len(reqs)-1].query["project"])
	})
	t.Run("a pinned project is used by default", func(t *testing.T) {
		s := NewServer(fw.Client(), fw.URL, "pinned_aaaaaa", "v")
		s.SetProjectPinned(true)
		initialize(t, s, "claude-ai")
		before := len(fw.requests("/api/context/search"))
		_, err := call(s, "search", map[string]any{"query": "x"})
		require.NoError(t, err)
		reqs := fw.requests("/api/context/search")
		require.Len(t, reqs, before+1)
		assert.Equal(t, "pinned_aaaaaa", reqs[before].query["project"])
	})
	t.Run("code mode is unchanged", func(t *testing.T) {
		s := NewServer(fw.Client(), fw.URL, "code_aaaaaa", "v")
		initialize(t, s, "claude-code")
		before := len(fw.requests("/api/context/search"))
		_, err := call(s, "search", map[string]any{"query": "x"})
		require.NoError(t, err)
		reqs := fw.requests("/api/context/search")
		require.Len(t, reqs, before+1)
		assert.Equal(t, "code_aaaaaa", reqs[before].query["project"])
	})
	t.Run("desktop search without a query fails before calling the worker", func(t *testing.T) {
		s := desktopServer(t, fw)
		before := fw.total()
		_, err := call(s, "search", map[string]any{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "query is required")
		assert.Equal(t, before, fw.total())
	})
}

func TestProjectList_And_Resolve_And_Suggest(t *testing.T) {
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/projects/summary": jsonReply(`[{"project":"repo_aaaaaa"}]`),
		"GET /api/projects/resolve": jsonReply(`{"id":"repo_aaaaaa","match":"path","known":true}`),
		"GET /api/projects/suggest": jsonReply(`{"suggestions":[{"project":"repo_aaaaaa"}],"confident":true}`),
	})
	s := desktopServer(t, fw)

	out, err := call(s, "project_list", map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, out, "repo_aaaaaa")

	out, err = call(s, "project_resolve", map[string]any{"path": "/Users/x/repo"})
	require.NoError(t, err)
	assert.Contains(t, out, `"match":"path"`)
	assert.Equal(t, "/Users/x/repo", fw.requests("/api/projects/resolve")[0].query["path"])

	_, err = call(s, "project_resolve", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pass path, name or id")

	out, err = call(s, "project_suggest", map[string]any{"opening_text": "fix the vector index", "limit": 3})
	require.NoError(t, err)
	assert.Contains(t, out, `"confident":true`)
	assert.Contains(t, out, "Next: ask the user", "the result repeats the protocol next to the data")
	assert.Contains(t, out, "do not call remember")
	q := fw.requests("/api/projects/suggest")[0].query
	assert.Equal(t, "fix the vector index", q["query"])
	assert.Equal(t, "3", q["limit"])
}

func TestContext_ResolvesBeforeLoading(t *testing.T) {
	resolve := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("id") == "frag_bbbbbb":
			jsonReply(`{"id":"repo_aaaaaa","match":"alias","known":true}`)(w, r)
		case r.URL.Query().Get("id") == "repo_aaaaaa":
			jsonReply(`{"id":"repo_aaaaaa","match":"exact","known":true}`)(w, r)
		case r.URL.Query().Get("path") != "":
			jsonReply(`{"id":"folder_cccccc","match":"path","known":false}`)(w, r)
		case r.URL.Query().Get("name") == "repo":
			jsonReply(`{"id":"repo_aaaaaa","match":"name","known":true}`)(w, r)
		case r.URL.Query().Get("name") == "rep":
			jsonReply(`{"match":"none","candidates":["repo_aaaaaa","report_dddddd"]}`)(w, r)
		default:
			jsonReply(`{"match":"none"}`)(w, r)
		}
	}
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/projects/resolve": resolve,
		"GET /api/context/inject":   jsonReply(`"context body"`),
	})
	s := desktopServer(t, fw)

	t.Run("an alias loads the canonical project", func(t *testing.T) {
		_, err := call(s, "context", map[string]any{"project": "frag_bbbbbb"})
		require.NoError(t, err)
		reqs := fw.requests("/api/context/inject")
		assert.Equal(t, "repo_aaaaaa", reqs[len(reqs)-1].query["project"])
	})
	t.Run("a folder path loads the project for that folder and passes it as cwd", func(t *testing.T) {
		_, err := call(s, "context", map[string]any{"path": "/Users/x/folder"})
		require.NoError(t, err)
		reqs := fw.requests("/api/context/inject")
		last := reqs[len(reqs)-1]
		assert.Equal(t, "folder_cccccc", last.query["project"])
		assert.Equal(t, "/Users/x/folder", last.query["cwd"])
	})
	t.Run("a unique name is accepted", func(t *testing.T) {
		_, err := call(s, "context", map[string]any{"project": "repo"})
		require.NoError(t, err)
		reqs := fw.requests("/api/context/inject")
		assert.Equal(t, "repo_aaaaaa", reqs[len(reqs)-1].query["project"])
	})
	t.Run("an unknown project lists candidates and does not load anything", func(t *testing.T) {
		before := len(fw.requests("/api/context/inject"))
		_, err := call(s, "context", map[string]any{"project": "rep"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repo_aaaaaa, report_dddddd")
		assert.Contains(t, err.Error(), "project_list or project_suggest")
		assert.Len(t, fw.requests("/api/context/inject"), before)
	})
	t.Run("desktop with no project is told how to choose one", func(t *testing.T) {
		_, err := call(s, "context", map[string]any{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "project_suggest")
		assert.Contains(t, err.Error(), "read-only")
	})
}

func rememberWorker(t *testing.T, reply string) *fakeWorker {
	t.Helper()
	return newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/projects/resolve": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			switch {
			case q.Get("id") == "repo_aaaaaa":
				jsonReply(`{"id":"repo_aaaaaa","match":"exact","known":true}`)(w, r)
			case strings.HasPrefix(q.Get("path"), "/"):
				jsonReply(`{"id":"folder_cccccc","match":"path","known":false}`)(w, r)
			case q.Get("name") == "repo":
				jsonReply(`{"id":"repo_aaaaaa","match":"name","known":true}`)(w, r)
			default: // like the worker, a relative path resolves to nothing
				jsonReply(`{"match":"none","candidates":["repo_aaaaaa"]}`)(w, r)
			}
		},
		"POST /api/observations/remember": func(w http.ResponseWriter, r *http.Request) {
			jsonReply(reply)(w, r)
		},
	})
}

func TestRemember_WritesToTheChosenProject(t *testing.T) {
	fw := rememberWorker(t, `{"project":"repo_aaaaaa","id":42}`)
	s := desktopServer(t, fw)

	out, err := call(s, "remember", map[string]any{
		"project": "repo_aaaaaa", "text": "We chose sqlite-vec.", "title": "Vector store",
		"type": "decision", "concepts": []string{"vector"}, "scope": "global",
	})
	require.NoError(t, err)
	assert.Equal(t, "Saved observation #42 to project repo_aaaaaa.", out)

	posts := fw.requests("/api/observations/remember")
	require.Len(t, posts, 1)
	b := posts[0].body
	assert.Equal(t, "repo_aaaaaa", b["project"])
	assert.Equal(t, "We chose sqlite-vec.", b["text"])
	assert.Equal(t, "Vector store", b["title"])
	assert.Equal(t, "decision", b["type"])
	assert.Equal(t, "global", b["scope"])
	assert.Equal(t, "claude-ai", b["source"], "the memory records which client wrote it")
	assert.Equal(t, false, b["allow_new_project"], "an id the user picked must already exist")
	assert.Equal(t, []any{"vector"}, b["concepts"])
}

func TestRemember_FolderPathMayStartANewProject(t *testing.T) {
	fw := rememberWorker(t, `{"project":"folder_cccccc","id":7}`)
	s := desktopServer(t, fw)

	_, err := call(s, "remember", map[string]any{"path": "/Users/x/folder", "text": "first note"})
	require.NoError(t, err)
	b := fw.requests("/api/observations/remember")[0].body
	assert.Equal(t, "folder_cccccc", b["project"])
	assert.Equal(t, true, b["allow_new_project"], "the id came from a real folder, so a new project is intended")
}

func TestRemember_AcceptsAUniqueName(t *testing.T) {
	fw := rememberWorker(t, `{"project":"repo_aaaaaa","id":1}`)
	s := desktopServer(t, fw)

	_, err := call(s, "remember", map[string]any{"project": "repo", "text": "x"})
	require.NoError(t, err)
	assert.Equal(t, "repo_aaaaaa", fw.requests("/api/observations/remember")[0].body["project"])
}

func TestRemember_NeverWritesWithoutAProject(t *testing.T) {
	fw := rememberWorker(t, `{"project":"x","id":1}`)
	s := desktopServer(t, fw)

	_, err := call(s, "remember", map[string]any{"text": "a thought from a declined chat"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a project is required")
	assert.Contains(t, err.Error(), "read-only")
	assert.Empty(t, fw.requests("/api/observations/remember"), "no project means no write reaches the worker")
	assert.Equal(t, 0, fw.total(), "and nothing else was called either")
}

func TestRemember_UnknownProjectIsRefusedWithCandidates(t *testing.T) {
	fw := rememberWorker(t, `{"project":"x","id":1}`)
	s := desktopServer(t, fw)

	_, err := call(s, "remember", map[string]any{"project": "made-up", "text": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown project "made-up"`)
	assert.Contains(t, err.Error(), "repo_aaaaaa")
	assert.Empty(t, fw.requests("/api/observations/remember"))
}

func TestRemember_Validation(t *testing.T) {
	fw := rememberWorker(t, `{"project":"repo_aaaaaa","id":1}`)
	s := desktopServer(t, fw)

	_, err := call(s, "remember", map[string]any{"project": "repo_aaaaaa", "text": "   "})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "text is required")

	_, err = call(s, "remember", map[string]any{"path": "relative/dir", "text": "x"})
	require.Error(t, err, "a relative path resolves to nothing")
	assert.Contains(t, err.Error(), "not an absolute folder path")

	assert.Empty(t, fw.requests("/api/observations/remember"))
}

func TestRemember_ReportsDuplicatesAndWorkerErrors(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		fw := rememberWorker(t, `{"project":"repo_aaaaaa","id":9,"duplicate":true}`)
		out, err := call(desktopServer(t, fw), "remember", map[string]any{"project": "repo_aaaaaa", "text": "x"})
		require.NoError(t, err)
		assert.Equal(t, "Already saved as observation #9 in project repo_aaaaaa.", out)
	})
	t.Run("worker refusal is surfaced", func(t *testing.T) {
		fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
			"GET /api/projects/resolve": jsonReply(`{"id":"repo_aaaaaa","match":"exact","known":true}`),
			"POST /api/observations/remember": func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "nothing to store: text is empty or entirely private", http.StatusBadRequest)
			},
		})
		_, err := call(desktopServer(t, fw), "remember", map[string]any{"project": "repo_aaaaaa", "text": "<private>x</private>"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "entirely private")
	})
}

func TestRemember_PinnedProjectIsTheDefault(t *testing.T) {
	fw := rememberWorker(t, `{"project":"pinned_aaaaaa","id":3}`)
	s := NewServer(fw.Client(), fw.URL, "pinned_aaaaaa", "v")
	s.SetProjectPinned(true)
	initialize(t, s, "claude-ai")

	_, err := call(s, "remember", map[string]any{"text": "uses the pinned project"})
	require.NoError(t, err)
	b := fw.requests("/api/observations/remember")[0].body
	assert.Equal(t, "pinned_aaaaaa", b["project"])
	assert.Equal(t, true, b["allow_new_project"])
}

func TestEnsureWorker(t *testing.T) {
	t.Run("healthy worker is not restarted and is not re-probed within the TTL", func(t *testing.T) {
		fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
			"GET /health":               jsonReply(`ok`),
			"GET /api/projects/summary": jsonReply(`[]`),
		})
		s := desktopServer(t, fw)
		started := 0
		s.SetWorkerBootstrap(func() error { started++; return nil })

		for i := 0; i < 3; i++ {
			_, err := call(s, "project_list", map[string]any{})
			require.NoError(t, err)
		}
		assert.Equal(t, 0, started)
		assert.Len(t, fw.requests("/health"), 1)
	})

	t.Run("a down worker is started once and the call then proceeds", func(t *testing.T) {
		var mu sync.Mutex
		up := false
		fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
			"GET /health": func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if !up {
					http.Error(w, "down", http.StatusServiceUnavailable)
					return
				}
				jsonReply(`ok`)(w, r)
			},
			"GET /api/projects/summary": jsonReply(`[]`),
		})
		s := desktopServer(t, fw)
		started := 0
		s.SetWorkerBootstrap(func() error {
			started++
			mu.Lock()
			up = true
			mu.Unlock()
			return nil
		})

		for i := 0; i < 2; i++ {
			_, err := call(s, "project_list", map[string]any{})
			require.NoError(t, err)
		}
		assert.Equal(t, 1, started)
	})

	t.Run("a worker that cannot be started yields a clear error", func(t *testing.T) {
		fw := newFakeWorker(t, nil) // /health -> 404
		s := desktopServer(t, fw)
		s.SetWorkerBootstrap(func() error { return errors.New("binary not found") })

		_, err := call(s, "project_list", map[string]any{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not be started")
		assert.Contains(t, err.Error(), "binary not found")
	})

	t.Run("code mode never bootstraps", func(t *testing.T) {
		fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){"GET /api/context/search": jsonReply(`{}`)})
		s := NewServer(fw.Client(), fw.URL, "code_aaaaaa", "v")
		initialize(t, s, "claude-code")
		s.SetWorkerBootstrap(func() error { t.Fatal("hooks start the worker in Code mode"); return nil })

		_, err := call(s, "search", map[string]any{"query": "x"})
		require.NoError(t, err)
		assert.Empty(t, fw.requests("/health"))
	})
}

// TestRun_EndToEndOverStdio drives the real read loop the way Claude Desktop does.
func TestRun_EndToEndOverStdio(t *testing.T) {
	fw := newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/projects/summary": jsonReply(`[{"project":"repo_aaaaaa"}]`),
	})
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	s := NewServer(fw.Client(), fw.URL, "code-project_111111", "test")
	s.stdin, s.stdout = inR, outW

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(ctx) }()

	replies := bufio.NewScanner(outR)
	replies.Buffer(make([]byte, 64*1024), 1<<20)
	send := func(line string) map[string]any {
		t.Helper()
		_, err := io.WriteString(inW, line+"\n")
		require.NoError(t, err)
		require.True(t, replies.Scan(), "no reply to %s", line)
		var m map[string]any
		require.NoError(t, json.Unmarshal(replies.Bytes(), &m))
		return m
	}

	init := send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"claude-ai"}}}`)
	assert.Contains(t, init["result"].(map[string]any)["instructions"], "project_suggest")

	list := send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var names []string
	for _, tool := range list["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	assert.Contains(t, names, "project_suggest")
	assert.Contains(t, names, "remember")

	res := send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"project_list","arguments":{}}}`)
	content := res["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, content, "repo_aaaaaa")

	refused := send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"remember","arguments":{"text":"x"}}}`)
	assert.Equal(t, true, refused["result"].(map[string]any)["isError"], "a write with no project is an error result")

	_ = inW.Close()
	select {
	case err := <-runErr:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Run did not stop after stdin closed")
	}
	cancel()
	assert.Empty(t, fw.requests("/api/observations/remember"))
}

func manageWorker(t *testing.T) *fakeWorker {
	t.Helper()
	return newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/projects/summary": jsonReply(`[
			{"project":"repo_aaaaaa","display_name":"repo","label":"repo","use":"repo"},
			{"project":"frag_bbbbbb","display_name":"frag","label":"frag","use":"frag"},
			{"project":"frag_ffffff","display_name":"frag","label":"frag","use":"frag","alias_of":"repo_aaaaaa"},
			{"project":"app_111111","display_name":"app","label":"app (49 observations, last used 2026-10-01, e.g. \"Naming rules\")","use":"app_111111"},
			{"project":"app_222222","display_name":"app","label":"app (1 observation, last used 2026-10-02)","use":"app_222222"}]`),
		"GET /api/projects/app_111111/stats":  jsonReply(`{"project":"app_111111"}`),
		"GET /api/projects/repo_aaaaaa/stats": jsonReply(`{"project":"repo_aaaaaa","observations":3}`),
		"DELETE /api/projects/repo_aaaaaa": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("confirm") == "tok123" {
				jsonReply(`{"dry_run":false,"message":"Deleted project repo_aaaaaa. A backup is at /b/x.db."}`)(w, r)
				return
			}
			jsonReply(`{"dry_run":true,"confirm":"tok123","message":"Would permanently delete project repo_aaaaaa with 3 observations."}`)(w, r)
		},
		"POST /api/projects/frag_bbbbbb/merge": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["confirm"] == "mtok" {
				jsonReply(`{"dry_run":false,"message":"Merged frag_bbbbbb into repo_aaaaaa."}`)(w, r)
				return
			}
			jsonReply(`{"dry_run":true,"confirm":"mtok","message":"Would move 2 observations."}`)(w, r)
		},
		"DELETE /api/projects/frag_bbbbbb":      jsonReply(`{"dry_run":true,"confirm":"ftok","message":"Would permanently delete project frag_bbbbbb."}`),
		"POST /api/projects/aliases":            jsonReply(`{"alias":"x_111111","canonical":"repo_aaaaaa"}`),
		"DELETE /api/projects/aliases/x_111111": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		"DELETE /api/projects/frag_ffffff": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "project id is an alias: frag_ffffff: it is an alias, not a project", http.StatusConflict)
		},
	})
}

func TestProjectManage_DescriptionRequiresUserApproval(t *testing.T) {
	var desc string
	for _, tool := range desktopTools() {
		if tool.Name == "project_manage" {
			desc = tool.Description
		}
	}
	assert.Contains(t, desc, "DESTRUCTIVE")
	assert.Contains(t, desc, "WITHOUT confirm")
	assert.Contains(t, desc, "explicit approval")
	assert.Contains(t, desc, "Never invent or reuse a token")
	assert.Contains(t, desc, "backup")
}

func TestProjectManage_Stats(t *testing.T) {
	fw := manageWorker(t)
	out, err := call(desktopServer(t, fw), "project_manage", map[string]any{"action": "stats", "project": "repo_aaaaaa"})
	require.NoError(t, err)
	assert.Contains(t, out, `"observations":3`)
}

func TestProjectManage_DeleteIsAPreviewUntilConfirmed(t *testing.T) {
	fw := manageWorker(t)
	s := desktopServer(t, fw)

	out, err := call(s, "project_manage", map[string]any{"action": "delete", "project": "repo_aaaaaa"})
	require.NoError(t, err)
	assert.Contains(t, out, "Would permanently delete project repo_aaaaaa")
	assert.Contains(t, out, "only a PREVIEW; nothing has changed")
	assert.Contains(t, out, "explicit approval")
	assert.Contains(t, out, "confirm: tok123", "the token is handed to the model for the follow-up call")
	reqs := fw.requests("/api/projects/repo_aaaaaa")
	require.Len(t, reqs, 1)
	assert.Empty(t, reqs[0].query["confirm"], "the first call never carries a confirmation")

	out, err = call(s, "project_manage", map[string]any{"action": "delete", "project": "repo_aaaaaa", "confirm": "tok123"})
	require.NoError(t, err)
	assert.Equal(t, "Deleted project repo_aaaaaa. A backup is at /b/x.db.", out)
	reqs = fw.requests("/api/projects/repo_aaaaaa")
	require.Len(t, reqs, 2)
	assert.Equal(t, "tok123", reqs[1].query["confirm"])
}

func TestProjectManage_MergePreviewThenConfirm(t *testing.T) {
	fw := manageWorker(t)
	s := desktopServer(t, fw)

	out, err := call(s, "project_manage", map[string]any{"action": "merge", "project": "frag_bbbbbb", "into": "repo_aaaaaa"})
	require.NoError(t, err)
	assert.Contains(t, out, "only a PREVIEW")
	assert.Contains(t, out, "confirm: mtok")
	first := fw.requests("/api/projects/frag_bbbbbb/merge")[0].body
	assert.Equal(t, "repo_aaaaaa", first["into"])
	assert.Equal(t, "", first["confirm"])

	out, err = call(s, "project_manage", map[string]any{"action": "merge", "project": "frag_bbbbbb", "into": "repo_aaaaaa", "confirm": "mtok"})
	require.NoError(t, err)
	assert.Equal(t, "Merged frag_bbbbbb into repo_aaaaaa.", out)
	assert.Equal(t, "mtok", fw.requests("/api/projects/frag_bbbbbb/merge")[1].body["confirm"])
}

func TestProjectManage_AliasAndUnalias(t *testing.T) {
	fw := manageWorker(t)
	s := desktopServer(t, fw)

	_, err := call(s, "project_manage", map[string]any{"action": "alias", "alias": "x_111111", "project": "repo_aaaaaa"})
	require.NoError(t, err)
	b := fw.requests("/api/projects/aliases")[0].body
	assert.Equal(t, map[string]any{"alias": "x_111111", "canonical": "repo_aaaaaa", "source": "mcp"}, b)

	out, err := call(s, "project_manage", map[string]any{"action": "unalias", "alias": "x_111111"})
	require.NoError(t, err)
	assert.Equal(t, "Removed alias x_111111.", out)
}

func TestProjectManage_ValidationNeverReachesTheWorker(t *testing.T) {
	fw := manageWorker(t)
	s := desktopServer(t, fw)

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing action", map[string]any{}, "unknown action"},
		{"unknown action", map[string]any{"action": "wipe", "project": "repo_aaaaaa"}, "unknown action"},
		{"stats needs a project", map[string]any{"action": "stats"}, "project is required"},
		{"delete needs a project", map[string]any{"action": "delete"}, "project is required"},
		{"merge needs a project", map[string]any{"action": "merge", "into": "repo_aaaaaa"}, "project is required"},
		{"merge needs a target", map[string]any{"action": "merge", "project": "frag_bbbbbb"}, "into is required"},
		{"alias needs both", map[string]any{"action": "alias", "alias": "x_111111"}, "project is required"},
		{"unalias needs an alias", map[string]any{"action": "unalias"}, "alias is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := call(s, "project_manage", tt.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
	assert.Equal(t, 0, fw.total())
}

func TestProjectManage_DoesNotResolveAliasesOrNamesForDestructiveActions(t *testing.T) {
	fw := manageWorker(t)
	s := desktopServer(t, fw)

	_, err := call(s, "project_manage", map[string]any{"action": "delete", "project": "frag_ffffff"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
	assert.Contains(t, err.Error(), "alias, not a project", "the worker's refusal reaches the model unchanged")
	assert.Empty(t, fw.requests("/api/projects/resolve"), "the id is passed through exactly; nothing is resolved behind the user's back")
}

func TestProjectManage_PathIsEscaped(t *testing.T) {
	fw := manageWorker(t)
	_, err := call(desktopServer(t, fw), "project_manage", map[string]any{"action": "stats", "project": "a/b c"})
	require.Error(t, err, "the fake has no such route")
	reqs := fw.requests("/api/projects/a/b c/stats")
	require.Len(t, reqs, 1, "the id arrives as one escaped segment, decoded by the server")
}

func TestProjectManage_AcceptsAUniqueNameAndActsOnItsId(t *testing.T) {
	fw := manageWorker(t)
	s := desktopServer(t, fw)

	for _, ref := range []string{"repo", "REPO", " repo ", "repo_aaaaaa"} {
		out, err := call(s, "project_manage", map[string]any{"action": "stats", "project": ref})
		require.NoError(t, err, ref)
		assert.Contains(t, out, `"project":"repo_aaaaaa"`, ref)
	}
	assert.Empty(t, fw.requests("/api/projects/repo/stats"), "the name is never sent to the worker as if it were an id")
	assert.Len(t, fw.requests("/api/projects/repo_aaaaaa/stats"), 4)
}

func TestProjectManage_DeleteByNameStillPreviewsFirst(t *testing.T) {
	fw := manageWorker(t)
	s := desktopServer(t, fw)

	out, err := call(s, "project_manage", map[string]any{"action": "delete", "project": "repo"})
	require.NoError(t, err)
	assert.Contains(t, out, "only a PREVIEW")
	reqs := fw.requests("/api/projects/repo_aaaaaa")
	require.Len(t, reqs, 1)
	assert.Empty(t, reqs[0].query["confirm"], "a name does not skip the preview")
}

func TestProjectManage_RefusesANameSharedByProjectsAndSaysHowTheyDiffer(t *testing.T) {
	fw := manageWorker(t)
	s := desktopServer(t, fw)

	for _, action := range []string{"stats", "delete"} {
		_, err := call(s, "project_manage", map[string]any{"action": action, "project": "app"})
		require.Error(t, err, action)
		msg := err.Error()
		assert.Contains(t, msg, `2 projects are called "app"`, action)
		assert.Contains(t, msg, "app_111111", action)
		assert.Contains(t, msg, "app_222222", action)
		assert.Contains(t, msg, `49 observations, last used 2026-10-01, e.g. "Naming rules"`, action)
		assert.Contains(t, msg, "Ask the user which one", action)
	}
	assert.Empty(t, fw.requests("/api/projects/app_111111"), "nothing was previewed, let alone deleted")
	assert.Empty(t, fw.requests("/api/projects/app_111111/stats"))
}

func TestProjectManage_NamesakeIsStillReachableById(t *testing.T) {
	fw := manageWorker(t)
	out, err := call(desktopServer(t, fw), "project_manage", map[string]any{"action": "stats", "project": "app_111111"})
	require.NoError(t, err)
	assert.Contains(t, out, "app_111111")
}

func TestProjectManage_AnAliasIsNeverMatchedByName(t *testing.T) {
	fw := manageWorker(t)
	s := desktopServer(t, fw)

	// "frag" names one real project (frag_bbbbbb) and one alias (frag_ffffff); only the real one counts.
	_, err := call(s, "project_manage", map[string]any{"action": "delete", "project": "frag"})
	require.NoError(t, err)
	assert.Len(t, fw.requests("/api/projects/frag_bbbbbb"), 1)
	assert.Empty(t, fw.requests("/api/projects/frag_ffffff"), "the alias row was not resolved by name")

	// the alias id itself still goes to the worker, which explains why it is refused
	_, err = call(s, "project_manage", map[string]any{"action": "delete", "project": "frag_ffffff"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alias, not a project")
}

func TestProjectManage_MergeResolvesBothSidesAndRefusesAnAmbiguousTarget(t *testing.T) {
	fw := manageWorker(t)
	s := desktopServer(t, fw)

	_, err := call(s, "project_manage", map[string]any{"action": "merge", "project": "frag", "into": "repo"})
	require.NoError(t, err)
	posts := fw.requests("/api/projects/frag_bbbbbb/merge")
	require.Len(t, posts, 1)
	assert.Equal(t, "repo_aaaaaa", posts[0].body["into"], "the target name became its id")

	before := len(fw.requests("/api/projects/frag_bbbbbb/merge"))
	_, err = call(s, "project_manage", map[string]any{"action": "merge", "project": "frag", "into": "app"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `2 projects are called "app"`)
	assert.Len(t, fw.requests("/api/projects/frag_bbbbbb/merge"), before, "an ambiguous target never reaches the worker")
}

func TestProjectManage_UnknownNamesPassThroughForTheWorkerToRefuse(t *testing.T) {
	fw := manageWorker(t)
	_, err := call(desktopServer(t, fw), "project_manage", map[string]any{"action": "stats", "project": "no-such-project"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404", "the worker's own answer, not a guess")
}

func namesakeWorker(t *testing.T) *fakeWorker {
	return newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/projects/resolve": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("name") == "app" {
				jsonReply(`{"match":"none","ambiguous":true,"candidates":["app_111111","app_222222"],"candidate_details":[
					{"project":"app_111111","label":"app (49 observations)","detail":"49 observations, last used 2026-10-01, e.g. \"Naming rules\""},
					{"project":"app_222222","label":"app (1 observation)","detail":"1 observation, last used 2026-10-02"}]}`)(w, r)
				return
			}
			if r.URL.Query().Get("name") == "mnemo" {
				jsonReply(`{"match":"none","candidates":["claude-mnemonic_41bfcd"],"candidate_details":[
					{"project":"claude-mnemonic_41bfcd","label":"claude-mnemonic","detail":"70 observations"}]}`)(w, r)
				return
			}
			jsonReply(`{"match":"none"}`)(w, r)
		},
		"POST /api/observations/remember": jsonReply(`{"project":"x","id":1}`),
		"GET /api/context/inject":         jsonReply(`"ctx"`),
	})
}

func TestNamesakes_RememberAndContextAskInsteadOfGuessing(t *testing.T) {
	fw := namesakeWorker(t)
	s := desktopServer(t, fw)

	for _, tc := range []struct {
		args map[string]any
		tool string
	}{
		{map[string]any{"project": "app", "text": "x"}, "remember"},
		{map[string]any{"project": "app"}, "context"},
	} {
		_, err := call(s, tc.tool, tc.args)
		require.Error(t, err, tc.tool)
		assert.Contains(t, err.Error(), `2 projects are called "app"`, tc.tool)
		assert.Contains(t, err.Error(), `(a) app_111111: 49 observations, last used 2026-10-01, e.g. "Naming rules"`, tc.tool)
		assert.Contains(t, err.Error(), "(b) app_222222: 1 observation", tc.tool)
		assert.Contains(t, err.Error(), "Ask the user which one they mean", tc.tool)
	}
	assert.Empty(t, fw.requests("/api/observations/remember"), "no write happened for an ambiguous name")
	assert.Empty(t, fw.requests("/api/context/inject"))
}

func TestNearMissNamesAreSuggestionsNotAmbiguity(t *testing.T) {
	fw := namesakeWorker(t)
	_, err := call(desktopServer(t, fw), "context", map[string]any{"project": "mnemo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown project "mnemo"`)
	assert.Contains(t, err.Error(), "did you mean one of: (a) claude-mnemonic_41bfcd: 70 observations")
	assert.NotContains(t, err.Error(), "projects are called")
}

func TestDescribeCandidatesFallsBackToBareIds(t *testing.T) {
	assert.Equal(t, "a_1, b_2", describeCandidates(nil, []string{"a_1", "b_2"}))
	assert.Equal(t, "(a) x_1: d1; (b) y_2: d2",
		describeCandidates([]candidateDetail{{Project: "x_1", Detail: "d1"}, {Project: "y_2", Detail: "d2"}}, nil))
}

func TestToolDescriptionsTellTheModelToUseLabelsAndUseValues(t *testing.T) {
	byName := map[string]string{}
	for _, tool := range desktopTools() {
		byName[tool.Name] = tool.Description
	}
	assert.Contains(t, byName["project_suggest"], "label")
	assert.Contains(t, byName["project_suggest"], "use value")
	assert.Contains(t, byName["project_list"], "label")
	assert.Contains(t, byName["project_manage"], "use` value")
	assert.Contains(t, byName["project_manage"], "Aliases and partial names are never accepted")
	assert.NotContains(t, byName["project_manage"], "names are not accepted here")
}

func toolDescriptions(t *testing.T, s *Server) map[string]string {
	t.Helper()
	resp := s.handleToolsList(&Request{JSONRPC: "2.0", ID: 2})
	out := map[string]string{}
	for _, tool := range resp.Result.(map[string]any)["tools"].([]Tool) {
		out[tool.Name] = tool.Description
	}
	return out
}

func TestMemoryWording_ClaudeCodeToolListIsUnchanged(t *testing.T) {
	code := NewServer(nil, "", "p_aaaaaa", "v")
	initialize(t, code, "claude-code")
	desktop := NewServer(nil, "", "p_aaaaaa", "v")
	initialize(t, desktop, "claude-ai")

	codeDesc, desktopDesc := toolDescriptions(t, code), toolDescriptions(t, desktop)
	for name, desc := range codeDesc {
		assert.NotContains(t, desc, "claude-mnemonic memory", "Code's %s description must not mention the Desktop wording", name)
		assert.NotContains(t, desc, "built-in memory", name)
		if name != "search" && name != "timeline" {
			assert.Equal(t, desc, desktopDesc[name], "%s is the same in both modes", name)
		}
	}
	assert.Equal(t, 4, len(codeDesc), "Code lists only the original tools")
}

func TestMemoryWording_DesktopSearchAndTimelineAreLedByWhatThisIs(t *testing.T) {
	code := NewServer(nil, "", "p_aaaaaa", "v")
	initialize(t, code, "claude-code")
	desktop := NewServer(nil, "", "p_aaaaaa", "v")
	initialize(t, desktop, "claude-ai")
	codeDesc, desktopDesc := toolDescriptions(t, code), toolDescriptions(t, desktop)

	for _, name := range []string{"search", "timeline"} {
		d := desktopDesc[name]
		assert.True(t, strings.HasPrefix(d, memoryPrefix), "%s starts with what claude-mnemonic is: %q", name, d[:60])
		assert.True(t, strings.HasSuffix(d, codeDesc[name]), "%s keeps its original description after the prefix", name)
	}
	assert.Contains(t, desktopDesc["search"], "earlier decisions, findings, fixes and project history")
	assert.Contains(t, desktopDesc["search"], "searches every project")
}

func TestMemoryWording_EntryPointNamesTheSituationsAndKeepsBothMemories(t *testing.T) {
	var d string
	for _, tool := range desktopTools() {
		if tool.Name == "project_suggest" {
			d = tool.Description
		}
	}
	for _, want := range []string{
		"persistent project memory, shared with Claude Code",
		"in addition to any built-in memory",
		"past work, earlier decisions or project history",
		"talks about memory or remembering",
		"say which source an answer came from",
		"not needed for general questions",
		"START HERE for any such memory question",
		"ASK the user",
		"never call remember",
	} {
		assert.Contains(t, d, want)
	}
}

func TestMemoryWording_OtherToolsSayWhatTheyBelongTo(t *testing.T) {
	for _, tool := range desktopTools() {
		switch tool.Name {
		case "project_resolve", "project_list", "context", "remember":
			assert.True(t, strings.HasPrefix(tool.Description, memoryPrefix), "%s: %q", tool.Name, tool.Description[:40])
		}
	}
}

func TestMemoryWording_InstructionsLeadWithItToo(t *testing.T) {
	// Claude Code's Desktop tab and Cowork show server instructions even though chat does not.
	assert.True(t, strings.HasPrefix(desktopInstructions, memoryBlurb))
	s := NewServer(nil, "", "p_aaaaaa", "v")
	result := initialize(t, s, "local-agent-mode-claude-mnemonic")
	assert.Contains(t, result["instructions"], "in addition to any built-in memory")
	assert.Contains(t, result["instructions"], "never call remember", "the protocol is still there")
}

func TestMemoryWording_DoesNotChangeWhatIsEnforced(t *testing.T) {
	fw := rememberWorker(t, `{"project":"repo_aaaaaa","id":1}`)
	s := desktopServer(t, fw)
	_, err := call(s, "remember", map[string]any{"text": "memory wording does not relax the rules"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a project is required")
	assert.Empty(t, fw.requests("/api/observations/remember"))
}

func TestMemoryWording_StaysReasonablyShort(t *testing.T) {
	// Descriptions sit in the model's context on every turn; keep the wording lean.
	assert.Less(t, len(memoryBlurb), 600)
	assert.Less(t, len(memoryPrefix), 120)
}

func threadWorker(t *testing.T, checkpointReply, catchUpReply string) *fakeWorker {
	t.Helper()
	return newFakeWorker(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/projects/resolve": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			switch {
			case q.Get("id") == "repo_aaaaaa" || q.Get("name") == "repo":
				jsonReply(`{"id":"repo_aaaaaa","match":"exact","known":true}`)(w, r)
			case strings.HasPrefix(q.Get("path"), "/"):
				jsonReply(`{"id":"folder_cccccc","match":"path","known":false}`)(w, r)
			default:
				jsonReply(`{"match":"none","candidates":["repo_aaaaaa"]}`)(w, r)
			}
		},
		"POST /api/threads/checkpoint":             jsonReply(checkpointReply),
		"GET /api/projects/repo_aaaaaa/catch-up":   jsonReply(catchUpReply),
		"GET /api/projects/folder_cccccc/catch-up": jsonReply(`{"project":"folder_cccccc","threads":[],"decisions":[]}`),
	})
}

func TestCheckpoint_SavesTheThreadNoteForTheChosenProject(t *testing.T) {
	fw := threadWorker(t, `{"project":"repo_aaaaaa","thread":"Overlay design","id":7,"created":true}`, `{}`)
	s := desktopServer(t, fw)

	out, err := call(s, "checkpoint", map[string]any{
		"project": "repo", "thread": "Overlay design", "goal": "ship it", "progress": "store done",
		"decisions": "names not ids", "next_steps": "handlers",
	})
	require.NoError(t, err)
	assert.Equal(t, `Saved a new note for thread "Overlay design" in project repo_aaaaaa.`, out)

	posts := fw.requests("/api/threads/checkpoint")
	require.Len(t, posts, 1)
	b := posts[0].body
	assert.Equal(t, "repo_aaaaaa", b["project"], "a name is resolved to the project id")
	assert.Equal(t, "Overlay design", b["thread"])
	assert.Equal(t, "ship it", b["goal"])
	assert.Equal(t, "store done", b["progress"])
	assert.Equal(t, "names not ids", b["decisions"])
	assert.Equal(t, "handlers", b["next_steps"])
	assert.Equal(t, "claude-ai", b["source"])
	assert.Equal(t, false, b["allow_new_project"], "a project chosen by name must already exist")
}

func TestCheckpoint_ReportsAnUpdateAndAllowsANewProjectOnlyFromAPath(t *testing.T) {
	fw := threadWorker(t, `{"project":"folder_cccccc","thread":"T","id":7,"created":false}`, `{}`)
	s := desktopServer(t, fw)

	out, err := call(s, "checkpoint", map[string]any{"path": "/Users/x/folder", "thread": "T", "goal": "g"})
	require.NoError(t, err)
	assert.Equal(t, `Updated the note for thread "T" in project folder_cccccc.`, out)
	assert.Equal(t, true, fw.requests("/api/threads/checkpoint")[0].body["allow_new_project"])
}

func TestCheckpoint_NeedsAThreadAndAProject(t *testing.T) {
	fw := threadWorker(t, `{}`, `{}`)
	s := desktopServer(t, fw)

	_, err := call(s, "checkpoint", map[string]any{"project": "repo_aaaaaa", "goal": "g"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "thread is required")

	_, err = call(s, "checkpoint", map[string]any{"thread": "T", "goal": "g"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a project is required", "a declined chat has no project, so nothing is written")
	assert.Empty(t, fw.requests("/api/threads/checkpoint"))
}

func TestCheckpoint_RejectsAGuessedProjectName(t *testing.T) {
	fw := threadWorker(t, `{}`, `{}`)
	s := desktopServer(t, fw)

	_, err := call(s, "checkpoint", map[string]any{"project": "relative/dir", "thread": "T", "goal": "g"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown project")
	assert.Empty(t, fw.requests("/api/threads/checkpoint"))
}

func TestCatchUp_RendersThreadsAndDecisionsAsText(t *testing.T) {
	fw := threadWorker(t, `{}`, `{"project":"repo_aaaaaa","threads":[
		{"thread":"Overlay design","goal":"ship it","progress":"store done","decisions":"names not ids","next_steps":"handlers","updated_at":"2026-10-03T16:00:00Z"},
		{"thread":"Docs","goal":"write DESKTOP.md","updated_at":"2026-10-02T09:00:00Z"}],
		"decisions":[{"title":"Use names","subtitle":"ids are cumbersome","created_at":"2026-10-01"},{"title":"Plain one","created_at":"2026-09-30"}]}`)
	s := desktopServer(t, fw)

	out, err := call(s, "catch_up", map[string]any{"project": "repo_aaaaaa", "threads": 3})
	require.NoError(t, err)
	assert.Contains(t, out, "Where the work stood in project repo_aaaaaa (most recently worked on first)")
	assert.Less(t, strings.Index(out, "Thread: Overlay design"), strings.Index(out, "Thread: Docs"), "the order of the worker is kept")
	for _, want := range []string{"  Goal: ship it", "  Progress: store done", "  Decisions: names not ids", "  Next: handlers",
		"- Use names: ids are cumbersome (2026-10-01)", "- Plain one (2026-09-30)", "keep it current with checkpoint"} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "Progress: \n", "empty fields are left out")

	reqs := fw.requests("/api/projects/repo_aaaaaa/catch-up")
	require.Len(t, reqs, 1)
	assert.Equal(t, "3", reqs[0].query["threads"])
}

func TestCatchUp_WithNothingSavedSaysSoAndPointsToContext(t *testing.T) {
	fw := threadWorker(t, `{}`, `{"project":"repo_aaaaaa","threads":[],"decisions":[]}`)
	s := desktopServer(t, fw)

	out, err := call(s, "catch_up", map[string]any{"project": "repo_aaaaaa"})
	require.NoError(t, err)
	assert.Contains(t, out, "No thread notes are saved for project repo_aaaaaa yet")
	assert.Contains(t, out, "call context")
	assert.NotContains(t, out, "keep it current")
	assert.Empty(t, fw.requests("/api/projects/repo_aaaaaa/catch-up")[0].query["threads"], "no limit is sent unless asked for")
}

func TestCatchUp_NeedsAProject(t *testing.T) {
	fw := threadWorker(t, `{}`, `{}`)
	_, err := call(desktopServer(t, fw), "catch_up", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a project is required")
}

func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func briefReply(text string) func(http.ResponseWriter, *http.Request) {
	return jsonReply(`{"project":"repo_aaaaaa","text":` + jsonString(text) + `,"source":"3 of 3 observations","as_of":"2026-10-03","as_of_epoch":1}`)
}

func contextWorker(t *testing.T, brief func(http.ResponseWriter, *http.Request)) *fakeWorker {
	t.Helper()
	routes := map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/projects/resolve": jsonReply(`{"id":"repo_aaaaaa","match":"exact","known":true}`),
		"GET /api/context/inject":   jsonReply(`"context body"`),
	}
	if brief != nil {
		routes["GET /api/projects/repo_aaaaaa/brief"] = brief
	}
	return newFakeWorker(t, routes)
}

func TestContext_PutsTheBriefAheadOfTheRawObservations(t *testing.T) {
	fw := contextWorker(t, briefReply("As of 2026-10-03, written from 3 of 3 observations.\n\n## What this is\nA plugin."))
	out, err := call(desktopServer(t, fw), "context", map[string]any{"project": "repo_aaaaaa"})
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(out, "Project brief (it can lag behind recent work):\n\nAs of 2026-10-03"), out)
	assert.Contains(t, out, "## What this is\nA plugin.")
	assert.Contains(t, out, "---\nSaved observations (raw):\n\"context body\"")
	assert.Less(t, strings.Index(out, "A plugin."), strings.Index(out, "context body"), "the brief comes first")
}

func TestContext_WithoutABriefIsExactlyWhatItWas(t *testing.T) {
	t.Run("the worker has none", func(t *testing.T) {
		fw := contextWorker(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no brief", http.StatusNotFound) })
		out, err := call(desktopServer(t, fw), "context", map[string]any{"project": "repo_aaaaaa"})
		require.NoError(t, err)
		assert.Equal(t, `"context body"`, out)
	})
	t.Run("the worker is an older one without the endpoint", func(t *testing.T) {
		out, err := call(desktopServer(t, contextWorker(t, nil)), "context", map[string]any{"project": "repo_aaaaaa"})
		require.NoError(t, err)
		assert.Equal(t, `"context body"`, out)
	})
	t.Run("the brief request fails", func(t *testing.T) {
		fw := contextWorker(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) })
		out, err := call(desktopServer(t, fw), "context", map[string]any{"project": "repo_aaaaaa"})
		require.NoError(t, err)
		assert.Equal(t, `"context body"`, out)
	})
	t.Run("the brief is empty", func(t *testing.T) {
		out, err := call(desktopServer(t, contextWorker(t, briefReply("  "))), "context", map[string]any{"project": "repo_aaaaaa"})
		require.NoError(t, err)
		assert.Equal(t, `"context body"`, out)
	})
}

func TestContext_IsStillUnavailableInCodeMode(t *testing.T) {
	s := NewServer(nil, "", "p_aaaaaa", "v")
	initialize(t, s, "claude-code")
	_, err := call(s, "context", map[string]any{"project": "repo_aaaaaa"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool", "the brief is for Desktop only")
}

func TestCatchUp_ShowsTheBriefFirst(t *testing.T) {
	fw := threadWorker(t, `{}`, `{"project":"repo_aaaaaa","brief":{"text":"As of 2026-10-03, written from 3 of 3 observations.\n\n## What this is\nA plugin.","source":"3 of 3 observations","as_of":"2026-10-03"},
		"threads":[{"thread":"Overlay design","goal":"ship it","updated_at":"2026-10-03T16:00:00Z"}],"decisions":[]}`)
	out, err := call(desktopServer(t, fw), "catch_up", map[string]any{"project": "repo_aaaaaa"})
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(out, "Project brief for repo_aaaaaa (it can lag behind recent work; the notes below are newer):\n\nAs of 2026-10-03"), out)
	assert.Less(t, strings.Index(out, "A plugin."), strings.Index(out, "Thread: Overlay design"), "brief, then the threads")
	assert.Contains(t, out, "---\nWhere the work stood in project repo_aaaaaa")
}

func TestCatchUp_AloneTheBriefIsEnoughToSayThereIsSomethingToRead(t *testing.T) {
	fw := threadWorker(t, `{}`, `{"project":"repo_aaaaaa","brief":{"text":"As of today.\n\n## What this is\nA plugin.","source":"s","as_of":"2026-10-03"},"threads":[],"decisions":[]}`)
	out, err := call(desktopServer(t, fw), "catch_up", map[string]any{"project": "repo_aaaaaa"})
	require.NoError(t, err)

	assert.Contains(t, out, "A plugin.")
	assert.Contains(t, out, "No thread notes are saved for project repo_aaaaaa yet")
	assert.NotContains(t, out, "Nothing to recover from here", "there is a brief to read")
}

func TestCatchUp_WithoutABriefIsUnchanged(t *testing.T) {
	fw := threadWorker(t, `{}`, `{"project":"repo_aaaaaa","threads":[],"decisions":[]}`)
	out, err := call(desktopServer(t, fw), "catch_up", map[string]any{"project": "repo_aaaaaa"})
	require.NoError(t, err)
	assert.NotContains(t, out, "Project brief")
	assert.Contains(t, out, "Nothing to recover from here")
}

func TestToolDescriptions_MentionTheBrief(t *testing.T) {
	byName := map[string]string{}
	for _, tool := range desktopTools() {
		byName[tool.Name] = tool.Description
	}
	assert.Contains(t, byName["context"], "brief")
	assert.Contains(t, byName["catch_up"], "brief")
}
