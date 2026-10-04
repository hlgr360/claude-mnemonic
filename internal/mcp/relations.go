package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// relatedArgs are the arguments of the related tool and of the observation tool's related action.
type relatedArgs struct {
	Query         string   `json:"query"`
	Project       string   `json:"project"`
	Direction     string   `json:"direction"`
	Types         []string `json:"types"`
	MinConfidence float64  `json:"min_confidence"`
	Limit         int      `json:"limit"`
	ID            int64    `json:"id"`
}

// connectionsReply mirrors the worker's GET /api/observations/{id}/connections.
type connectionsReply struct {
	Observation struct {
		Title   string `json:"title"`
		Type    string `json:"type"`
		Project string `json:"project"`
		ID      int64  `json:"id"`
	} `json:"observation"`
	Connections []struct {
		Relation struct {
			RelationType    string  `json:"relation_type"`
			Reason          string  `json:"reason"`
			DetectionSource string  `json:"detection_source"`
			Confidence      float64 `json:"confidence"`
			SourceID        int64   `json:"source_id"`
		} `json:"relation"`
		Direction string `json:"direction"`
		Note      struct {
			Title          string `json:"title"`
			Subtitle       string `json:"subtitle"`
			Type           string `json:"type"`
			Project        string `json:"project"`
			ID             int64  `json:"id"`
			CreatedAtEpoch int64  `json:"created_at_epoch"`
		} `json:"note"`
	} `json:"connections"`
	Total int `json:"total"`
}

// relationPhrase says what the relation is, from the point of view of the note that was asked about. outgoing is true
// when that note is the source of the relation: "this note fixes it".
func relationPhrase(relationType string, outgoing bool) string {
	phrases := map[string][2]string{ // outgoing, incoming
		"causes":       {"causes it", "is caused by it"},
		"fixes":        {"fixes it", "is fixed by it"},
		"supersedes":   {"supersedes it", "is superseded by it"},
		"depends_on":   {"builds on it", "is built on by it"},
		"relates_to":   {"relates to it", "relates to it"},
		"evolves_from": {"evolved from it", "evolved into it"},
	}
	p, ok := phrases[relationType]
	if !ok {
		return relationType
	}
	if outgoing {
		return "this note " + p[0]
	}
	return "this note " + p[1]
}

func quoteTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "(untitled)"
	}
	return fmt.Sprintf("%q", title)
}

// renderConnections turns the worker's answer into text a model can read and follow: the note asked about, then one
// line per connection with the other note's id, what the relation is, how sure the graph is and why.
func renderConnections(raw string, filtered bool) (string, error) {
	var r connectionsReply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return "", fmt.Errorf("related: unexpected answer from the worker: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Note #%d %s (%s, %s)", r.Observation.ID, quoteTitle(r.Observation.Title), r.Observation.Type, r.Observation.Project)
	if len(r.Connections) == 0 {
		if filtered {
			b.WriteString(" has no connections that match. Try without the filters, or call relation_types to see what exists.")
		} else {
			b.WriteString(" has no connections yet. The graph links notes that read alike; it is built in the background, so a new note may take a few minutes.")
		}
		return b.String(), nil
	}
	if r.Total > len(r.Connections) {
		fmt.Fprintf(&b, " has %d connections; the %d most certain:\n", r.Total, len(r.Connections))
	} else {
		fmt.Fprintf(&b, " has %d connection%s:\n", r.Total, map[bool]string{true: "", false: "s"}[r.Total == 1])
	}
	for _, c := range r.Connections {
		outgoing := c.Direction == "older" // the note asked about is the source: it points back to an older note
		if c.Relation.SourceID != 0 {
			outgoing = c.Relation.SourceID == r.Observation.ID
		}
		fmt.Fprintf(&b, "- #%d %s (%s, %s", c.Note.ID, quoteTitle(c.Note.Title), c.Note.Type, c.Note.Project)
		if c.Note.CreatedAtEpoch > 0 {
			fmt.Fprintf(&b, ", %s", time.UnixMilli(c.Note.CreatedAtEpoch).UTC().Format("2006-01-02"))
		}
		fmt.Fprintf(&b, ", %s): %s [%s, %.2f]", c.Direction, relationPhrase(c.Relation.RelationType, outgoing), c.Relation.RelationType, c.Relation.Confidence)
		if reason := strings.TrimSpace(c.Relation.Reason); reason != "" {
			fmt.Fprintf(&b, " — %s", reason)
		}
		b.WriteString("\n")
	}
	if r.Total > len(r.Connections) {
		b.WriteString("Narrow with types, direction or min_confidence, or raise limit (at most 100).\n")
	}
	b.WriteString("Pass any of these ids to related to follow the chain, or read a note in full with observation (action get).")
	return b.String(), nil
}

// relationTypesReply mirrors the worker's GET /api/relations/types.
type relationTypesReply struct {
	Types []struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Count       int    `json:"count"`
		Automatic   bool   `json:"automatic"`
	} `json:"types"`
	Total int `json:"total"`
}

func renderRelationTypes(raw, scope string) (string, error) {
	var r relationTypesReply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return "", fmt.Errorf("relation_types: unexpected answer from the worker: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Relations %s: %d in all. The types, with how many there are of each:\n", scope, r.Total)
	for _, t := range r.Types {
		auto := "set by a person or a tool, never by the graph"
		if t.Automatic {
			auto = "created by the graph"
		}
		fmt.Fprintf(&b, "- %s (%d, %s): %s\n", t.Type, t.Count, auto, t.Description)
	}
	b.WriteString("Use these names in the types argument of related.")
	return b.String(), nil
}

// findNoteByQuery returns the id of the note that best matches the text, within one project.
func (s *Server) findNoteByQuery(ctx context.Context, project, query string) (int64, error) {
	raw, err := s.proxyGetRaw(ctx, "/api/context/search", map[string]string{"project": project, "query": query, "limit": "1"})
	if err != nil {
		return 0, err
	}
	var found struct {
		Observations []struct {
			ID int64 `json:"id"`
		} `json:"observations"`
	}
	if err := json.Unmarshal([]byte(raw), &found); err != nil {
		return 0, fmt.Errorf("related: unexpected search answer: %w", err)
	}
	if len(found.Observations) == 0 {
		return 0, fmt.Errorf("related: no note in this project matches %q; pass the id of a note from a search result instead", query)
	}
	return found.Observations[0].ID, nil
}

// toolRelated lists the notes connected to one note, with how they are connected. The note is given by its id (from a
// search result, or from an earlier answer of this tool) or found by a query within a project.
func (s *Server) toolRelated(ctx context.Context, args json.RawMessage) (string, error) {
	var a relatedArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("related: invalid arguments: %w", err)
	}
	id := a.ID
	if id < 0 {
		return "", fmt.Errorf("related: id must be positive")
	}
	if id == 0 {
		if strings.TrimSpace(a.Query) == "" {
			return "", fmt.Errorf("related: pass the id of a note (it is in search results and in earlier answers of this tool), or a query that finds the note")
		}
		project, _, err := s.projectFromArgs(ctx, "related", a.Project, "")
		if err != nil {
			return "", err
		}
		if id, err = s.findNoteByQuery(ctx, project, a.Query); err != nil {
			return "", err
		}
	}

	qp := map[string]string{}
	if a.Direction != "" {
		qp["direction"] = a.Direction
	}
	if len(a.Types) > 0 {
		qp["types"] = strings.Join(a.Types, ",")
	}
	if a.MinConfidence > 0 {
		qp["min_confidence"] = strconv.FormatFloat(a.MinConfidence, 'f', -1, 64)
	}
	if a.Limit > 0 {
		qp["limit"] = strconv.Itoa(a.Limit)
	}
	raw, err := s.proxyGetRaw(ctx, fmt.Sprintf("/api/observations/%d/connections", id), qp)
	if err != nil {
		return "", err
	}
	return renderConnections(raw, qp["direction"] != "" || qp["types"] != "" || qp["min_confidence"] != "")
}

// toolRelationTypes lists the relation types with what they mean and how many relations there are of each: in the
// whole graph, in one project, or around one note.
func (s *Server) toolRelationTypes(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Project string `json:"project"`
		ID      int64  `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("relation_types: invalid arguments: %w", err)
	}
	qp := map[string]string{}
	scope := "in all projects"
	switch {
	case a.ID > 0:
		qp["observation_id"] = strconv.FormatInt(a.ID, 10)
		scope = fmt.Sprintf("around note #%d", a.ID)
	case strings.TrimSpace(a.Project) != "":
		project, _, err := s.projectFromArgs(ctx, "relation_types", a.Project, "")
		if err != nil {
			return "", err
		}
		qp["project"] = project
		scope = "in project " + a.Project
	}
	raw, err := s.proxyGetRaw(ctx, "/api/relations/types", qp)
	if err != nil {
		return "", err
	}
	return renderRelationTypes(raw, scope)
}
