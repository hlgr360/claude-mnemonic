package worker

import "sort"

// topIDsByRelevance returns the ids of the best matches, best first, at most limit of them (limit <= 0 keeps all).
// With scores the ids are put in the order of their score, highest first; ids with the same score, or with no score,
// keep the order they came in. Without scores the order given is taken as the ranking. A vector search hands back
// more candidates than the caller wants, and the cut has to be made by relevance: cutting after a sort by date
// drops an older note that matched best.
func topIDsByRelevance(ids []int64, scores map[int64]float64, limit int) []int64 {
	out := append([]int64(nil), ids...)
	if len(scores) > 0 {
		sort.SliceStable(out, func(i, j int) bool { return scores[out[i]] > scores[out[j]] })
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
