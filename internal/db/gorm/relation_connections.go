package gorm

import (
	"context"
	"strings"

	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

// Directions of a connection, seen from the note that was asked about.
const (
	// DirectionOlder lists relations to older notes: the asked-about note is the newer one of the relation.
	DirectionOlder = "older"
	// DirectionNewer lists relations from newer notes: the other note is the newer one.
	DirectionNewer = "newer"
)

// ConnectionFilter selects the connections of one note. A zero value is every type and direction, any confidence,
// and the default number.
type ConnectionFilter struct {
	Direction     string
	Types         []models.RelationType
	MinConfidence float64
	// Limit is how many to return (the most certain first); zero means 20, and at most 100.
	Limit int
}

// ConnectionNote is the note at the other end of a connection.
type ConnectionNote struct {
	Title          string   `json:"title"`
	Subtitle       string   `json:"subtitle,omitempty"`
	Type           string   `json:"type"`
	Project        string   `json:"project"`
	Concepts       []string `json:"concepts,omitempty"`
	ID             int64    `json:"id"`
	CreatedAtEpoch int64    `json:"created_at_epoch"`
}

// Connection is one relation of a note with the note at its other end.
type Connection struct {
	Relation *models.ObservationRelation `json:"relation"`
	// Direction says where the other note is in time: "older" (this note is the newer one) or "newer".
	Direction string         `json:"direction"`
	Note      ConnectionNote `json:"note"`
}

type connectionRow struct {
	OtherTitle    string
	OtherSubtitle string
	OtherType     string
	OtherProject  string
	OtherConcepts models.JSONStringArray `gorm:"type:text"`
	ObservationRelation
	OtherID    int64
	OtherEpoch int64
}

// Connections returns the notes related to one note, with the relation to each: its type, confidence and reason, and
// whether the other note is older or newer. Only live notes are returned (not superseded, not archived), the most
// certain relation first. total is how many match before the limit.
func (s *RelationStore) Connections(ctx context.Context, id int64, f ConnectionFilter) (connections []Connection, total int, err error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	build := func() (query string, args []any) {
		query = ` FROM observation_relations r
			JOIN observations o ON o.id = CASE WHEN r.source_id = ? THEN r.target_id ELSE r.source_id END
			  AND COALESCE(o.is_archived, 0) = 0 AND COALESCE(o.is_superseded, 0) = 0
			WHERE r.confidence >= ?`
		args = []any{id, f.MinConfidence}
		switch f.Direction {
		case DirectionOlder:
			query += ` AND r.source_id = ?`
			args = append(args, id)
		case DirectionNewer:
			query += ` AND r.target_id = ?`
			args = append(args, id)
		default:
			query += ` AND (r.source_id = ? OR r.target_id = ?)`
			args = append(args, id, id)
		}
		if len(f.Types) > 0 {
			types := make([]string, len(f.Types))
			for i, t := range f.Types {
				types[i] = string(t)
			}
			query += ` AND r.relation_type IN ?`
			args = append(args, types)
		}
		return query, args
	}

	q, args := build()
	var count int64
	if err = s.db.WithContext(ctx).Raw(`SELECT COUNT(*)`+q, args...).Scan(&count).Error; err != nil {
		return nil, 0, err
	}
	var rows []connectionRow
	err = s.db.WithContext(ctx).Raw(`SELECT r.*, COALESCE(o.title, '') AS other_title, COALESCE(o.subtitle, '') AS other_subtitle,
		o.type AS other_type, o.project AS other_project, o.concepts AS other_concepts, o.id AS other_id, o.created_at_epoch AS other_epoch`+
		q+` ORDER BY r.confidence DESC, r.id ASC LIMIT ?`, append(args, limit)...).Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	connections = make([]Connection, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		direction := DirectionNewer
		if r.SourceID == id {
			direction = DirectionOlder
		}
		connections = append(connections, Connection{
			Relation:  toModelRelation(&r.ObservationRelation),
			Direction: direction,
			Note: ConnectionNote{
				ID: r.OtherID, Title: strings.TrimSpace(r.OtherTitle), Subtitle: strings.TrimSpace(r.OtherSubtitle), Type: r.OtherType,
				Project: r.OtherProject, Concepts: []string(r.OtherConcepts), CreatedAtEpoch: r.OtherEpoch,
			},
		})
	}
	return connections, int(count), nil
}

// RelationTypeCount is how many relations of one type there are.
type RelationTypeCount struct {
	Type  models.RelationType `json:"type"`
	Count int                 `json:"count"`
}

// RelationTypeCounts counts the relations between live notes by type, for the whole graph, for one project (when
// project is not empty), or for the relations of one note (when observationID is not zero). Every type is listed, with
// zero when there are none, in the order of models.AllRelationTypes.
func (s *RelationStore) RelationTypeCounts(ctx context.Context, project string, observationID int64) ([]RelationTypeCount, error) {
	counts := map[models.RelationType]int{}
	if observationID != 0 {
		var rows []struct {
			Type  string
			Count int
		}
		err := s.db.WithContext(ctx).Raw(`SELECT r.relation_type AS type, COUNT(*) AS count
			FROM observation_relations r
			JOIN observations o ON o.id = CASE WHEN r.source_id = ? THEN r.target_id ELSE r.source_id END
			  AND COALESCE(o.is_archived, 0) = 0 AND COALESCE(o.is_superseded, 0) = 0
			WHERE r.source_id = ? OR r.target_id = ? GROUP BY r.relation_type`, observationID, observationID, observationID).Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			counts[models.RelationType(r.Type)] = r.Count
		}
	} else {
		stats, err := s.GraphStats(ctx, project)
		if err != nil {
			return nil, err
		}
		counts = stats.EdgeTypes
	}
	out := make([]RelationTypeCount, 0, len(models.AllRelationTypes))
	for _, t := range models.AllRelationTypes {
		out = append(out, RelationTypeCount{Type: t, Count: counts[t]})
	}
	return out, nil
}
