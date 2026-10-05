package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/lukaszraczylo/claude-mnemonic/internal/db/gorm"
	"github.com/lukaszraczylo/claude-mnemonic/internal/privacy"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	checkpointMaxBody    = 128 << 10
	checkpointFieldRunes = 4000
	threadSlugRunes      = 60
	catchUpFieldRunes    = 1500
	catchUpDefaultThread = 5
	catchUpMaxThreads    = 20
	catchUpDefaultDecis  = 8
	catchUpMaxDecisions  = 30
)

// CheckpointRequest is the body of POST /api/threads/checkpoint: the current
// state of one line of work in a chat, written on purpose by a client that has
// no hooks (Claude Desktop) so a later chat can pick it up.
type CheckpointRequest struct {
	Project   string `json:"project"`
	Thread    string `json:"thread"`
	Goal      string `json:"goal"`
	Progress  string `json:"progress"`
	Decisions string `json:"decisions"`
	NextSteps string `json:"next_steps"`
	// Source names the client that wrote the note, for example "claude-ai".
	Source string `json:"source"`
	// AllowNewProject lets the write create a project that has no history yet.
	AllowNewProject bool `json:"allow_new_project"`
}

// CheckpointResponse reports which note was written.
type CheckpointResponse struct {
	Project string `json:"project"`
	Thread  string `json:"thread"`
	ID      int64  `json:"id"`
	Created bool   `json:"created"`
}

// CatchUpThread is one thread note as returned to a new chat.
type CatchUpThread struct {
	Thread       string `json:"thread"`
	Goal         string `json:"goal,omitempty"`
	Progress     string `json:"progress,omitempty"`
	Decisions    string `json:"decisions,omitempty"`
	NextSteps    string `json:"next_steps,omitempty"`
	UpdatedAt    string `json:"updated_at"`
	UpdatedEpoch int64  `json:"updated_epoch"`
}

// CatchUpDecision is one recent decision of the project.
type CatchUpDecision struct {
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle,omitempty"`
	CreatedAt string `json:"created_at"`
}

// CatchUpBrief is the project's brief as a new chat receives it.
type CatchUpBrief struct {
	Text   string `json:"text"`
	Source string `json:"source"`
	AsOf   string `json:"as_of"`
}

// CatchUpResponse is the digest a new chat reads to recover where the work stood.
type CatchUpResponse struct {
	Brief     *CatchUpBrief     `json:"brief,omitempty"`
	Project   string            `json:"project"`
	Threads   []CatchUpThread   `json:"threads"`
	Decisions []CatchUpDecision `json:"decisions"`
}

// handleCheckpoint writes or refreshes the living note of one thread.
//
// A thread is identified by its project and a name; checkpointing the same
// name again replaces the note, so a long chat leaves one current note per
// line of work instead of a pile of near-copies.
func (s *Service) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var req CheckpointRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, checkpointMaxBody)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	project := strings.TrimSpace(req.Project)
	if project == "" {
		http.Error(w, "project is required: a checkpoint without a project would be stored nowhere", http.StatusBadRequest)
		return
	}
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	thread := strings.TrimSpace(privacy.Clean(req.Thread))
	slug := threadSlug(thread)
	if slug == "" {
		http.Error(w, "thread is required: give the line of work a short name", http.StatusBadRequest)
		return
	}

	fields := map[string]*string{"goal": &req.Goal, "progress": &req.Progress, "decisions": &req.Decisions, "next_steps": &req.NextSteps}
	for name, field := range fields {
		if len([]rune(*field)) > checkpointFieldRunes {
			http.Error(w, fmt.Sprintf("%s is too long (limit %d characters): keep a checkpoint short", name, checkpointFieldRunes), http.StatusBadRequest)
			return
		}
		*field = strings.TrimSpace(privacy.Clean(*field))
	}
	if req.Goal == "" && req.Progress == "" {
		http.Error(w, "nothing to store: give at least a goal or the progress so far (private parts are removed)", http.StatusBadRequest)
		return
	}

	canonical, _, err := gorm.NewProjectAliasStore(s.store).ResolveAlias(ctx, project)
	if err != nil {
		http.Error(w, "failed to resolve project", http.StatusInternalServerError)
		return
	}
	if !req.AllowNewProject {
		known, kerr := s.projectExists(ctx, canonical)
		if kerr != nil {
			http.Error(w, "failed to check project", http.StatusInternalServerError)
			return
		}
		if !known {
			http.Error(w, fmt.Sprintf("unknown project %q: pick an existing project, or pass the folder's full path on the user's computer (not a sandbox path) to start a new one", canonical),
				http.StatusUnprocessableEntity)
			return
		}
	}

	parsed := &models.ParsedSummary{
		Request:   thread,
		Notes:     req.Goal,
		Completed: req.Progress,
		Learned:   req.Decisions,
		NextSteps: req.NextSteps,
	}
	id, created, err := s.summaryStore.UpsertThreadSummary(ctx, gorm.ThreadSessionPrefix+canonical+"-"+slug, canonical, parsed)
	if err != nil {
		log.Error().Err(err).Str("project", canonical).Msg("checkpoint: failed to store thread note")
		http.Error(w, "failed to store checkpoint", http.StatusInternalServerError)
		return
	}

	s.afterSummaryWrite(id, canonical, created, "checkpoint")

	noStore(w)
	writeJSON(w, CheckpointResponse{Project: canonical, Thread: thread, ID: id, Created: created})
}

// afterSummaryWrite refreshes a written summary row's search documents (old ones are removed first,
// because the text changed) and tells the dashboard. action names what was written: "checkpoint", "brief".
func (s *Service) afterSummaryWrite(id int64, project string, created bool, action string) {
	if s.vectorSync != nil {
		s.asyncVectorSync(func() {
			ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			defer cancel()
			summaries, err := s.summaryStore.GetSummariesByIDs(ctx, []int64{id}, "date_desc", 1)
			if err != nil || len(summaries) == 0 {
				return
			}
			if !created {
				if derr := s.vectorSync.DeleteSummaries(ctx, []int64{id}); derr != nil && s.ctx.Err() == nil {
					log.Debug().Err(derr).Int64("id", id).Msg(action + ": failed to drop old vectors")
				}
			}
			if serr := s.vectorSync.SyncSummary(ctx, summaries[0]); serr != nil && s.ctx.Err() == nil {
				log.Debug().Err(serr).Int64("id", id).Msg(action + ": failed to sync")
			}
		})
	}
	if s.sseBroadcaster != nil {
		s.sseBroadcaster.Broadcast(map[string]any{
			"type":    "summary",
			"action":  action,
			"project": project,
			"count":   1,
		})
	}
}

// handleCatchUp returns what a new chat needs to resume a project: the thread
// notes (most recently worked on first) and the project's latest decisions.
func (s *Service) handleCatchUp(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	project := strings.TrimSpace(chi.URLParam(r, "id"))
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	canonical, _, err := gorm.NewProjectAliasStore(s.store).ResolveAlias(ctx, project)
	if err != nil {
		http.Error(w, "failed to resolve project", http.StatusInternalServerError)
		return
	}
	known, err := s.projectExists(ctx, canonical)
	if err != nil {
		http.Error(w, "failed to check project", http.StatusInternalServerError)
		return
	}
	if !known {
		http.Error(w, fmt.Sprintf("unknown project %q", canonical), http.StatusUnprocessableEntity)
		return
	}

	threadLimit := boundedInt(r.URL.Query().Get("threads"), catchUpDefaultThread, catchUpMaxThreads)
	decisionLimit := boundedInt(r.URL.Query().Get("decisions"), catchUpDefaultDecis, catchUpMaxDecisions)

	notes, err := s.summaryStore.GetThreadSummaries(ctx, canonical, threadLimit)
	if err != nil {
		http.Error(w, "failed to load thread notes", http.StatusInternalServerError)
		return
	}
	decisions, err := s.summaryStore.RecentDecisions(ctx, canonical, decisionLimit)
	if err != nil {
		http.Error(w, "failed to load decisions", http.StatusInternalServerError)
		return
	}

	resp := CatchUpResponse{Project: canonical, Threads: make([]CatchUpThread, 0, len(notes)), Decisions: make([]CatchUpDecision, 0, len(decisions))}
	for _, n := range notes {
		resp.Threads = append(resp.Threads, CatchUpThread{
			Thread:       n.Request.String,
			Goal:         clip(n.Notes.String, catchUpFieldRunes),
			Progress:     clip(n.Completed.String, catchUpFieldRunes),
			Decisions:    clip(n.Learned.String, catchUpFieldRunes),
			NextSteps:    clip(n.NextSteps.String, catchUpFieldRunes),
			UpdatedAt:    n.CreatedAt,
			UpdatedEpoch: n.CreatedAtEpoch,
		})
	}
	for _, d := range decisions {
		resp.Decisions = append(resp.Decisions, CatchUpDecision{Title: d.Title, Subtitle: d.Subtitle, CreatedAt: d.CreatedAt})
	}

	if brief, berr := s.summaryStore.GetBrief(ctx, canonical); berr == nil && brief != nil {
		b := briefResponse(brief)
		resp.Brief = &CatchUpBrief{Text: b.Text, Source: b.Source, AsOf: b.AsOf}
	}

	noStore(w)
	writeJSON(w, resp)
}

// threadSlug turns a thread name into the stable part of its note's key:
// lower case letters and digits, everything else collapsed to single dashes.
func threadSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := []rune(strings.TrimRight(b.String(), "-"))
	if len(slug) > threadSlugRunes {
		slug = slug[:threadSlugRunes]
	}
	return strings.TrimRight(string(slug), "-")
}

func boundedInt(raw string, def, limit int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > limit {
		return limit
	}
	return n
}

func clip(s string, runes int) string {
	r := []rune(s)
	if len(r) <= runes {
		return s
	}
	return strings.TrimSpace(string(r[:runes])) + "…"
}
