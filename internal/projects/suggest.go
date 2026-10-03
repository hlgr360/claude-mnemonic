package projects

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Hit is one semantic-search match attributed to a project.
type Hit struct {
	Project    string
	Title      string
	Similarity float64
}

// Activity describes a project's footprint, used for the recency signal and the hint.
type Activity struct {
	Project         string
	Sessions        int64
	Observations    int64
	LastActiveEpoch int64 // milliseconds
}

// Suggestion is one ranked candidate project.
type Suggestion struct {
	Project         string   `json:"project"`
	DisplayName     string   `json:"display_name"`
	Label           string   `json:"label,omitempty"`
	Use             string   `json:"use,omitempty"`
	Reason          string   `json:"reason"`
	TopTitles       []string `json:"top_titles,omitempty"`
	Score           float64  `json:"score"`
	Hits            int      `json:"hits"`
	Sessions        int64    `json:"sessions"`
	Observations    int64    `json:"observations"`
	LastActiveEpoch int64    `json:"last_active_epoch"`
}

// Suggestions is the ranked result of Suggest.
type Suggestions struct {
	Suggestions []Suggestion `json:"suggestions"`
	// Confident is true only when the top suggestion clearly leads the rest, so a
	// caller may propose it as a default instead of asking an open question.
	Confident bool `json:"confident"`
}

const (
	maxHitsPerProject   = 5
	nameMatchWeight     = 0.4
	maxNameMatchScore   = 0.8
	recencyWeight       = 0.15
	recencyHalfLifeDays = 14.0
	confidentRatio      = 1.8 // top must beat the runner-up by this factor...
	confidentMinScore   = 0.5 // ...and have at least this much evidence
	msPerDay            = 24 * 60 * 60 * 1000
)

// Suggest ranks projects for a free-text query by combining three signals:
// semantic hits (the strongest few per project), tokens of the query that
// appear in the project's directory name, and a small recency boost. Hits and
// activity recorded under an alias are credited to its canonical project.
//
// With no usable signal at all it falls back to the most recently active
// projects, so a caller always has something to offer.
func Suggest(query string, hits []Hit, activity []Activity, aliases map[string]string, nowMs int64, limit int) Suggestions {
	if limit <= 0 {
		limit = 4
	}
	canon := func(p string) string {
		if c, ok := aliases[p]; ok {
			return c
		}
		return p
	}

	type agg struct {
		sims   []float64
		titles []Hit
		act    Activity
	}
	byProject := map[string]*agg{}
	get := func(p string) *agg {
		a, ok := byProject[p]
		if !ok {
			a = &agg{act: Activity{Project: p}}
			byProject[p] = a
		}
		return a
	}

	for _, h := range hits {
		if h.Project == "" || h.Similarity <= 0 {
			continue
		}
		a := get(canon(h.Project))
		a.sims = append(a.sims, h.Similarity)
		a.titles = append(a.titles, h)
	}
	for _, act := range activity {
		if act.Project == "" {
			continue
		}
		a := get(canon(act.Project))
		a.act.Sessions += act.Sessions
		a.act.Observations += act.Observations
		if act.LastActiveEpoch > a.act.LastActiveEpoch {
			a.act.LastActiveEpoch = act.LastActiveEpoch
		}
	}

	qTokens := tokens(query)
	out := make([]Suggestion, 0, len(byProject))
	signal := false
	for project, a := range byProject {
		sort.Sort(sort.Reverse(sort.Float64Slice(a.sims)))
		semantic := 0.0
		for i, s := range a.sims {
			if i >= maxHitsPerProject {
				break
			}
			semantic += s
		}

		nameScore := math.Min(nameMatchWeight*float64(nameMatches(qTokens, DisplayName(project))), maxNameMatchScore)
		recency := 0.0
		if a.act.LastActiveEpoch > 0 && nowMs >= a.act.LastActiveEpoch {
			ageDays := float64(nowMs-a.act.LastActiveEpoch) / msPerDay
			recency = recencyWeight * math.Pow(0.5, ageDays/recencyHalfLifeDays)
		}

		if semantic > 0 || nameScore > 0 {
			signal = true
		}

		sort.Slice(a.titles, func(i, j int) bool { return a.titles[i].Similarity > a.titles[j].Similarity })
		var tops []string
		for _, h := range a.titles {
			if h.Title != "" && len(tops) < 2 {
				tops = append(tops, h.Title)
			}
		}

		out = append(out, Suggestion{
			Project:         project,
			DisplayName:     DisplayName(project),
			Score:           round3(semantic + nameScore + recency),
			Hits:            len(a.sims),
			TopTitles:       tops,
			Reason:          reason(len(a.sims) > 0, nameScore > 0),
			Sessions:        a.act.Sessions,
			Observations:    a.act.Observations,
			LastActiveEpoch: a.act.LastActiveEpoch,
		})
	}

	if !signal {
		for i := range out {
			out[i].Reason = "recent"
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].LastActiveEpoch != out[j].LastActiveEpoch {
			return out[i].LastActiveEpoch > out[j].LastActiveEpoch
		}
		return out[i].Project < out[j].Project
	})
	if len(out) > limit {
		out = out[:limit]
	}

	confident := false
	if signal && len(out) > 0 && out[0].Score >= confidentMinScore {
		confident = len(out) == 1 || out[0].Score >= confidentRatio*out[1].Score
	}
	return Suggestions{Suggestions: out, Confident: confident}
}

func reason(semantic, name bool) string {
	switch {
	case semantic && name:
		return "content and name match"
	case semantic:
		return "content match"
	case name:
		return "name match"
	}
	return "recent"
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// tokens lower-cases the text and splits it into words of three or more letters or digits.
func tokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(f) >= 3 {
			out[f] = struct{}{}
		}
	}
	return out
}

// nameMatches counts query tokens that equal a token of the project's directory name.
func nameMatches(query map[string]struct{}, display string) int {
	n := 0
	for t := range tokens(display) {
		if _, ok := query[t]; ok {
			n++
		}
	}
	return n
}
