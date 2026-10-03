package gorm

import "context"

// Totals is how many observations, prompts and summaries the dashboard's lists hold.
type Totals struct {
	Observations int64 `json:"observations"`
	Prompts      int64 `json:"prompts"`
	Summaries    int64 `json:"summaries"`
}

// Totals counts exactly what the list endpoints (observations, prompts and summaries) return when they are not
// paged, for one project, or for all of them when project is empty. The dashboard lists show the newest part of
// these, so this is what to show as "how many there are": it counts the same rows, including the superseded
// observations the feed shows marked, and the checkpoint notes and briefs that the summary list holds.
func (s *Store) Totals(ctx context.Context, project string) (Totals, error) {
	var t Totals
	db := s.DB.WithContext(ctx)

	obs, prompts, sums := "SELECT COUNT(*) FROM observations", "SELECT COUNT(*) FROM user_prompts up LEFT JOIN sdk_sessions s ON up.claude_session_id = s.claude_session_id",
		"SELECT COUNT(*) FROM session_summaries"
	var args []any
	if project != "" {
		obs += " WHERE project = ?"
		prompts += " WHERE s.project = ?"
		sums += " WHERE project = ?"
		args = []any{project}
	}
	if err := db.Raw(obs, args...).Scan(&t.Observations).Error; err != nil {
		return Totals{}, err
	}
	if err := db.Raw(prompts, args...).Scan(&t.Prompts).Error; err != nil {
		return Totals{}, err
	}
	if err := db.Raw(sums, args...).Scan(&t.Summaries).Error; err != nil {
		return Totals{}, err
	}
	return t, nil
}
