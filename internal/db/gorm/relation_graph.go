package gorm

import (
	"context"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// liveRelationJoins keeps relations whose two observations both exist and still count: not archived and not
// superseded by a decision in the conflict review. The joins also drop relations of deleted observations.
const liveRelationJoins = `
	JOIN observations src ON src.id = r.source_id AND COALESCE(src.is_archived, 0) = 0 AND COALESCE(src.is_superseded, 0) = 0
	JOIN observations tgt ON tgt.id = r.target_id AND COALESCE(tgt.is_archived, 0) = 0 AND COALESCE(tgt.is_superseded, 0) = 0`

// UncheckedForRelations returns up to limit live observations the relation builder has not looked at yet,
// newest first.
func (s *RelationStore) UncheckedForRelations(ctx context.Context, limit int) ([]*models.Observation, error) {
	var rows []Observation
	err := s.db.WithContext(ctx).
		Where("COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0").
		Where("id NOT IN (SELECT observation_id FROM relation_checks)").
		Order("created_at_epoch DESC, id DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return toModelObservations(rows), nil
}

// CountUncheckedForRelations says how many live observations the relation builder has still to look at.
func (s *RelationStore) CountUncheckedForRelations(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&Observation{}).
		Where("COALESCE(is_archived, 0) = 0 AND COALESCE(is_superseded, 0) = 0").
		Where("id NOT IN (SELECT observation_id FROM relation_checks)").
		Count(&n).Error
	return n, err
}

// MarkRelationChecked records that the builder looked at an observation and how many relations it found.
func (s *RelationStore) MarkRelationChecked(ctx context.Context, observationID int64, relations int) error {
	return s.db.WithContext(ctx).Save(&RelationCheck{
		ObservationID: observationID, CheckedAtEpoch: time.Now().UnixMilli(), Relations: relations,
	}).Error
}

// ResetRelations deletes every relation and forgets which observations were looked at, so the builder starts
// over, for example after its thresholds were changed. It returns how many relations were deleted.
func (s *RelationStore) ResetRelations(ctx context.Context) (int64, error) {
	var deleted int64
	err := immediateTx(ctx, s.db, func(tx *gorm.DB) error {
		res := tx.Exec(`DELETE FROM observation_relations`)
		if res.Error != nil {
			return res.Error
		}
		deleted = res.RowsAffected
		return tx.Exec(`DELETE FROM relation_checks`).Error
	})
	return deleted, err
}

// GraphFilter selects the part of the graph to return. A zero value is every project, every type, any confidence.
type GraphFilter struct {
	Project       string
	Types         []models.RelationType
	MinConfidence float64
	// MaxNodes bounds the answer; the best connected observations are kept. Zero means 400.
	MaxNodes int
}

// GraphNode is an observation in the graph.
type GraphNode struct {
	Title          string   `json:"title"`
	Type           string   `json:"type"`
	Project        string   `json:"project"`
	Concepts       []string `json:"concepts,omitempty"`
	ID             int64    `json:"id"`
	Degree         int      `json:"degree"`
	CreatedAtEpoch int64    `json:"created_at_epoch"`
}

// GraphEdge is a relation between two observations in the graph.
type GraphEdge struct {
	Type       models.RelationType `json:"type"`
	Reason     string              `json:"reason,omitempty"`
	ID         int64               `json:"id"`
	Source     int64               `json:"source"`
	Target     int64               `json:"target"`
	Confidence float64             `json:"confidence"`
}

// GraphData is the answer to a graph query.
type GraphData struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	// TotalNodes and TotalEdges are what the filter matches before the node limit; Truncated says it cut.
	TotalNodes int  `json:"total_nodes"`
	TotalEdges int  `json:"total_edges"`
	Truncated  bool `json:"truncated"`
}

type edgeRow struct {
	Reason       *string
	RelationType string
	ID           int64
	SourceID     int64
	TargetID     int64
	Confidence   float64
}

func (s *RelationStore) filteredEdges(ctx context.Context, f GraphFilter) ([]edgeRow, error) {
	q := s.db.WithContext(ctx).Table("observation_relations r").
		Select("r.id, r.source_id, r.target_id, r.relation_type, r.confidence, r.reason").
		Joins(strings.TrimSpace(liveRelationJoins)).
		Where("r.confidence >= ?", f.MinConfidence)
	if f.Project != "" {
		q = q.Where("src.project = ? AND tgt.project = ?", f.Project, f.Project)
	}
	if len(f.Types) > 0 {
		types := make([]string, len(f.Types))
		for i, t := range f.Types {
			types[i] = string(t)
		}
		q = q.Where("r.relation_type IN ?", types)
	}
	var rows []edgeRow
	err := q.Order("r.confidence DESC, r.id ASC").Scan(&rows).Error
	return rows, err
}

// Graph returns the observations that have at least one relation matching the filter, and those relations.
func (s *RelationStore) Graph(ctx context.Context, f GraphFilter) (*GraphData, error) {
	maxNodes := f.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 400
	}
	rows, err := s.filteredEdges(ctx, f)
	if err != nil {
		return nil, err
	}

	degree := map[int64]int{}
	for _, e := range rows {
		degree[e.SourceID]++
		degree[e.TargetID]++
	}
	out := &GraphData{Nodes: []GraphNode{}, Edges: []GraphEdge{}, TotalNodes: len(degree), TotalEdges: len(rows)}

	keep := degree
	if len(degree) > maxNodes {
		ids := make([]int64, 0, len(degree))
		for id := range degree {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			if degree[ids[i]] != degree[ids[j]] {
				return degree[ids[i]] > degree[ids[j]]
			}
			return ids[i] < ids[j]
		})
		keep = make(map[int64]int, maxNodes)
		for _, id := range ids[:maxNodes] {
			keep[id] = degree[id]
		}
		out.Truncated = true
	}

	shownDegree := map[int64]int{}
	for _, e := range rows {
		if _, ok := keep[e.SourceID]; !ok {
			continue
		}
		if _, ok := keep[e.TargetID]; !ok {
			continue
		}
		edge := GraphEdge{ID: e.ID, Source: e.SourceID, Target: e.TargetID, Type: models.RelationType(e.RelationType), Confidence: e.Confidence}
		if e.Reason != nil {
			edge.Reason = *e.Reason
		}
		out.Edges = append(out.Edges, edge)
		shownDegree[e.SourceID]++
		shownDegree[e.TargetID]++
	}
	if len(shownDegree) == 0 {
		return out, nil
	}

	ids := make([]int64, 0, len(shownDegree))
	for id := range shownDegree {
		ids = append(ids, id)
	}
	var obs []Observation
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&obs).Error; err != nil {
		return nil, err
	}
	for _, o := range toModelObservations(obs) {
		out.Nodes = append(out.Nodes, GraphNode{
			ID: o.ID, Title: o.Title.String, Type: string(o.Type), Project: o.Project,
			Concepts: []string(o.Concepts), Degree: shownDegree[o.ID], CreatedAtEpoch: o.CreatedAtEpoch,
		})
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].ID < out.Nodes[j].ID })
	return out, nil
}

// GraphStatistics are the real numbers of the graph: relations between live observations, and the
// observations they connect.
type GraphStatistics struct {
	EdgeTypes    map[models.RelationType]int `json:"edge_types"`
	Edges        int                         `json:"edges"`
	Nodes        int                         `json:"nodes"`
	AvgDegree    float64                     `json:"avg_degree"`
	MaxDegree    int                         `json:"max_degree"`
	MinDegree    int                         `json:"min_degree"`
	MedianDegree float64                     `json:"median_degree"`
}

// GraphStats counts the graph for one project, or for all of them when project is empty.
func (s *RelationStore) GraphStats(ctx context.Context, project string) (*GraphStatistics, error) {
	rows, err := s.filteredEdges(ctx, GraphFilter{Project: project})
	if err != nil {
		return nil, err
	}
	st := &GraphStatistics{EdgeTypes: map[models.RelationType]int{}, Edges: len(rows)}
	for _, t := range models.AllRelationTypes {
		st.EdgeTypes[t] = 0
	}
	degree := map[int64]int{}
	for _, e := range rows {
		st.EdgeTypes[models.RelationType(e.RelationType)]++
		degree[e.SourceID]++
		degree[e.TargetID]++
	}
	st.Nodes = len(degree)
	if st.Nodes == 0 {
		return st, nil
	}
	ds := make([]int, 0, len(degree))
	sum := 0
	for _, d := range degree {
		ds = append(ds, d)
		sum += d
	}
	sort.Ints(ds)
	st.MinDegree, st.MaxDegree = ds[0], ds[len(ds)-1]
	st.AvgDegree = float64(sum) / float64(len(ds))
	if n := len(ds); n%2 == 1 {
		st.MedianDegree = float64(ds[n/2])
	} else {
		st.MedianDegree = float64(ds[n/2-1]+ds[n/2]) / 2
	}
	return st, nil
}
