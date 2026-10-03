// Package gorm provides GORM-based database operations for claude-mnemonic.
package gorm

import "context"

// ProjectSummary describes one project's footprint in the store.
type ProjectSummary struct {
	Project         string `json:"project"`
	Sessions        int64  `json:"sessions"`
	Observations    int64  `json:"observations"`
	LastActiveEpoch int64  `json:"last_active_epoch"`
}

// ProjectSummaries returns every project that has sessions or active
// observations, most recently active first. Unlike GetAllProjects it also
// lists projects that only have observations, so a project cannot disappear
// from the list just because its session rows were cleaned up.
func (s *SessionStore) ProjectSummaries(ctx context.Context) ([]ProjectSummary, error) {
	var out []ProjectSummary
	err := s.db.WithContext(ctx).Raw(`
		SELECT project,
		       SUM(sessions)     AS sessions,
		       SUM(observations) AS observations,
		       MAX(last_epoch)   AS last_active_epoch
		FROM (
			SELECT project, COUNT(*) AS sessions, 0 AS observations, MAX(started_at_epoch) AS last_epoch
			FROM sdk_sessions
			WHERE project IS NOT NULL AND project != ''
			GROUP BY project
			UNION ALL
			SELECT project, 0, COUNT(*), MAX(created_at_epoch)
			FROM observations
			WHERE project IS NOT NULL AND project != '' AND (is_archived = 0 OR is_archived IS NULL)
			GROUP BY project
		)
		GROUP BY project
		ORDER BY last_active_epoch DESC, project ASC`).Scan(&out).Error
	return out, err
}
