package worker

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// handleCounts returns how many observations, prompts and summaries there are, for one project or all of
// them: GET /api/counts?project=. The dashboard shows the newest part of each list; this is the number to
// show next to it.
func (s *Service) handleCounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if err := ValidateProjectName(project); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	totals, err := s.store.Totals(ctx, project)
	if err != nil {
		http.Error(w, "failed to count", http.StatusInternalServerError)
		return
	}
	noStore(w)
	writeJSON(w, totals)
}
