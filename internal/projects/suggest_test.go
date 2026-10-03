package projects

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const day = int64(24 * 60 * 60 * 1000)

var now = 100 * day

func ids(s Suggestions) []string {
	out := make([]string, len(s.Suggestions))
	for i, x := range s.Suggestions {
		out[i] = x.Project
	}
	return out
}

func TestSuggest_SemanticHitsRankProjects(t *testing.T) {
	hits := []Hit{
		{"awx_aaaaaa", "AWX credential rotation", 0.8},
		{"awx_aaaaaa", "AWX key vault", 0.7},
		{"mnemonic_bbbbbb", "vector search", 0.5},
	}
	got := Suggest("rotate awx credentials", hits, nil, nil, now, 4)

	assert.Equal(t, []string{"awx_aaaaaa", "mnemonic_bbbbbb"}, ids(got))
	top := got.Suggestions[0]
	assert.Equal(t, 2, top.Hits)
	assert.Equal(t, []string{"AWX credential rotation", "AWX key vault"}, top.TopTitles)
	assert.InDelta(t, 1.9, top.Score, 0.01, "0.8 + 0.7 semantic, +0.4 because the query names the project (\"awx\"), no recency data")
	assert.Equal(t, "content and name match", top.Reason)
	assert.Equal(t, "content match", got.Suggestions[1].Reason)
	assert.True(t, got.Confident, "1.9 vs 0.5 is a clear lead")
}

func TestSuggest_OnlyTheStrongestHitsPerProjectCount(t *testing.T) {
	var hits []Hit
	for i := 0; i < 20; i++ {
		hits = append(hits, Hit{"many_aaaaaa", "", 0.2})
	}
	hits = append(hits, Hit{"few_bbbbbb", "", 0.9}, Hit{"few_bbbbbb", "", 0.9})

	got := Suggest("anything", hits, nil, nil, now, 4)
	// many: 5 * 0.2 = 1.0 (capped at 5 hits), few: 1.8
	assert.Equal(t, []string{"few_bbbbbb", "many_aaaaaa"}, ids(got), "volume of weak hits must not beat a few strong ones")
	assert.InDelta(t, 1.0, got.Suggestions[1].Score, 0.01)
	assert.Equal(t, 20, got.Suggestions[1].Hits, "Hits still reports the raw count")
}

func TestSuggest_NameMatchBoostsAProjectWithoutHits(t *testing.T) {
	activity := []Activity{{Project: "knowledge_base_cccccc", LastActiveEpoch: now - 60*day}, {Project: "other_dddddd", LastActiveEpoch: now - 60*day}}

	got := Suggest("update the knowledge base indexer", nil, activity, nil, now, 4)
	assert.Equal(t, "knowledge_base_cccccc", got.Suggestions[0].Project)
	assert.Equal(t, "name match", got.Suggestions[0].Reason)
}

func TestSuggest_NameMatchIsCapped(t *testing.T) {
	got := Suggest("a_b alpha beta gamma delta", nil,
		[]Activity{{Project: "alpha_beta_gamma_delta_eeeeee"}}, nil, now, 4)
	assert.LessOrEqual(t, got.Suggestions[0].Score, maxNameMatchScore+0.001)
}

func TestSuggest_ShortTokensDoNotMatchNames(t *testing.T) {
	got := Suggest("go to it", nil, []Activity{{Project: "go_ffffff"}, {Project: "it_gggggg"}}, nil, now, 4)
	for _, s := range got.Suggestions {
		assert.Equal(t, "recent", s.Reason, "two-letter words are noise")
	}
}

func TestSuggest_RecencyBreaksTiesAndDecays(t *testing.T) {
	activity := []Activity{
		{Project: "old_aaaaaa", LastActiveEpoch: now - 200*day},
		{Project: "new_bbbbbb", LastActiveEpoch: now - 1*day},
	}
	got := Suggest("zzz", nil, activity, nil, now, 4)
	assert.Equal(t, []string{"new_bbbbbb", "old_aaaaaa"}, ids(got))
	assert.False(t, got.Confident, "recency alone is never confident")
	assert.Greater(t, got.Suggestions[0].Score, got.Suggestions[1].Score)
}

func TestSuggest_NoSignalFallsBackToRecentProjects(t *testing.T) {
	activity := []Activity{{Project: "a_aaaaaa", LastActiveEpoch: now - 3*day}, {Project: "b_bbbbbb", LastActiveEpoch: now - day}}
	got := Suggest("", nil, activity, nil, now, 4)

	assert.Equal(t, []string{"b_bbbbbb", "a_aaaaaa"}, ids(got))
	for _, s := range got.Suggestions {
		assert.Equal(t, "recent", s.Reason)
	}
	assert.False(t, got.Confident)
}

func TestSuggest_AliasHitsAndActivityAreCreditedToCanonical(t *testing.T) {
	aliases := map[string]string{"frag_ffffff": "repo_aaaaaa"}
	hits := []Hit{{"frag_ffffff", "from fragment", 0.6}, {"repo_aaaaaa", "from main", 0.6}}
	activity := []Activity{
		{Project: "frag_ffffff", Sessions: 1, Observations: 2, LastActiveEpoch: now - 5*day},
		{Project: "repo_aaaaaa", Sessions: 3, Observations: 4, LastActiveEpoch: now - 9*day},
	}

	got := Suggest("x", hits, activity, aliases, now, 4)
	require.Len(t, got.Suggestions, 1, "the fragment must not appear as its own suggestion")
	s := got.Suggestions[0]
	assert.Equal(t, "repo_aaaaaa", s.Project)
	assert.Equal(t, 2, s.Hits)
	assert.Equal(t, int64(4), s.Sessions)
	assert.Equal(t, int64(6), s.Observations)
	assert.Equal(t, now-5*day, s.LastActiveEpoch, "latest activity of either")
}

func TestSuggest_ConfidenceNeedsClearLeadAndEvidence(t *testing.T) {
	t.Run("close race is not confident", func(t *testing.T) {
		got := Suggest("x", []Hit{{"a_aaaaaa", "", 0.9}, {"b_bbbbbb", "", 0.8}}, nil, nil, now, 4)
		assert.False(t, got.Confident)
	})
	t.Run("clear lead but weak evidence is not confident", func(t *testing.T) {
		got := Suggest("x", []Hit{{"a_aaaaaa", "", 0.3}, {"b_bbbbbb", "", 0.05}}, nil, nil, now, 4)
		assert.False(t, got.Confident)
	})
	t.Run("a single strong candidate is confident", func(t *testing.T) {
		got := Suggest("x", []Hit{{"a_aaaaaa", "", 0.9}}, nil, nil, now, 4)
		assert.True(t, got.Confident)
	})
}

func TestSuggest_LimitAndDefaults(t *testing.T) {
	var hits []Hit
	for _, p := range []string{"a_111111", "b_222222", "c_333333", "d_444444", "e_555555", "f_666666"} {
		hits = append(hits, Hit{p, "", 0.5})
	}
	assert.Len(t, Suggest("x", hits, nil, nil, now, 0).Suggestions, 4, "default limit")
	assert.Len(t, Suggest("x", hits, nil, nil, now, 2).Suggestions, 2)
	assert.Len(t, Suggest("x", hits, nil, nil, now, 50).Suggestions, 6)
}

func TestSuggest_IgnoresBlankProjectsAndNonPositiveSimilarity(t *testing.T) {
	got := Suggest("x", []Hit{{"", "no project", 0.9}, {"a_aaaaaa", "zero", 0}, {"a_aaaaaa", "neg", -0.2}},
		[]Activity{{Project: ""}}, nil, now, 4)
	assert.Empty(t, got.Suggestions)
	assert.False(t, got.Confident)
}

func TestSuggest_EmptyInputs(t *testing.T) {
	got := Suggest("", nil, nil, nil, now, 4)
	assert.Empty(t, got.Suggestions)
	assert.False(t, got.Confident)
}

func TestSuggest_DeterministicOrderOnTies(t *testing.T) {
	hits := []Hit{{"b_222222", "", 0.5}, {"a_111111", "", 0.5}}
	for i := 0; i < 20; i++ {
		assert.Equal(t, []string{"a_111111", "b_222222"}, ids(Suggest("x", hits, nil, nil, now, 4)))
	}
}

func TestSuggest_TitlesSkipBlanksAndKeepBestTwo(t *testing.T) {
	hits := []Hit{{"a_aaaaaa", "", 0.9}, {"a_aaaaaa", "second", 0.8}, {"a_aaaaaa", "first?", 0.85}, {"a_aaaaaa", "third", 0.1}}
	got := Suggest("x", hits, nil, nil, now, 4)
	assert.Equal(t, []string{"first?", "second"}, got.Suggestions[0].TopTitles)
}
