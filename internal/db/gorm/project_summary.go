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

// ProjectSampleTitles returns up to perProject observation titles for each of
// the given projects, most important first, so a person can tell namesakes
// apart by what they hold. Projects with no titled observation are absent.
func (s *SessionStore) ProjectSampleTitles(ctx context.Context, projects []string, perProject int) (map[string][]string, error) {
	out := map[string][]string{}
	if len(projects) == 0 || perProject <= 0 {
		return out, nil
	}
	var rows []struct {
		Project string
		Title   string
	}
	err := s.db.WithContext(ctx).Raw(`
		SELECT project, title FROM (
			SELECT project, title,
			       ROW_NUMBER() OVER (PARTITION BY project ORDER BY COALESCE(importance_score, 1.0) DESC, created_at_epoch DESC) AS rn
			FROM observations
			WHERE project IN ? AND title IS NOT NULL AND title != ''
			  AND (is_archived = 0 OR is_archived IS NULL)
		) WHERE rn <= ?
		ORDER BY project, rn`, projects, perProject).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Project] = append(out[r.Project], r.Title)
	}
	return out, nil
}
