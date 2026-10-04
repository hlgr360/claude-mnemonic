package projects

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func titles(ts ...string) map[string]bool {
	m := map[string]bool{}
	for _, t := range ts {
		m[NormalizeTitle(t)] = true
	}
	return m
}

func cand(id string, notes int, ids ...Identity) DuplicateCandidate {
	return DuplicateCandidate{ID: id, Observations: notes, Identities: ids, LastActiveEpoch: int64(notes)}
}

func exists(paths ...string) func(string) bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return func(p string) bool { return m[p] }
}

func codes(s DuplicateSuggestion) []string {
	var out []string
	for _, r := range s.Reasons {
		out = append(out, r.Code)
	}
	return out
}

func TestSuggestDuplicates_TheSameRemoteIsStrongEvenUnderDifferentNames(t *testing.T) {
	shop := Identity{Remote: "git.example.org/team/shop", Root: "/work/shop"}
	moved := Identity{Remote: "git.example.org/team/shop", Root: "/work/clients/webshop"}
	got, _ := SuggestDuplicates([]DuplicateCandidate{
		cand("shop_aaaaaa", 40, shop), cand("webshop_bbbbbb", 6, moved), cand("other_cccccc", 9, Identity{Remote: "git.example.org/team/other"}),
	}, nil, exists("/work/shop", "/work/clients/webshop"))
	require.Len(t, got, 1)
	assert.Equal(t, "shop_aaaaaa", got[0].Survivor, "the project with more notes survives")
	assert.Equal(t, "webshop_bbbbbb", got[0].Other)
	assert.Equal(t, StrengthStrong, got[0].Strength)
	assert.Contains(t, codes(got[0]), EvidenceSameRemote)
	assert.Contains(t, got[0].Reasons[0].Text, "git.example.org/team/shop")
}

func TestSuggestDuplicates_NamesakesWithDifferentRemotesAreNeverSuggested(t *testing.T) {
	got, _ := SuggestDuplicates([]DuplicateCandidate{
		cand("app_aaaaaa", 40, Identity{Remote: "git.example.org/team/app", Root: "/a/app"}),
		cand("app_bbbbbb", 2, Identity{Remote: "git.example.org/clients/app", Root: "/b/app"}),
	}, nil, exists("/a/app"))
	assert.Empty(t, got, "different remotes: real namesakes, whatever else looks alike")

	same := titles("One", "Two", "Three")
	a, b := cand("app_aaaaaa", 40, Identity{Remote: "x/y"}), cand("app_bbbbbb", 2, Identity{Remote: "x/z"})
	a.Titles, b.Titles = same, same
	got, _ = SuggestDuplicates([]DuplicateCandidate{a, b}, nil, nil)
	assert.Empty(t, got, "even with the same notes")
}

func TestSuggestDuplicates_ANameAloneIsNotEvidence(t *testing.T) {
	got, _ := SuggestDuplicates([]DuplicateCandidate{cand("app_aaaaaa", 40), cand("app_bbbbbb", 30)}, nil, nil)
	assert.Empty(t, got)
	got, _ = SuggestDuplicates([]DuplicateCandidate{
		cand("app_aaaaaa", 40, Identity{Root: "/a/app"}), cand("app_bbbbbb", 30, Identity{Root: "/b/app"})}, nil, exists("/a/app", "/b/app"))
	assert.Empty(t, got, "no remote, both folders exist, both are big: nothing to go on")
}

func TestSuggestDuplicates_NameWithEvidenceWhenNoRemoteIsKnown(t *testing.T) {
	a, b := cand("app_aaaaaa", 40), cand("app_bbbbbb", 30)
	a.Titles, b.Titles = titles("Retry policy", "Cache lifetime", "Queue size", "Only in a"), titles("retry  policy", "Cache lifetime", "Only in b")
	got, _ := SuggestDuplicates([]DuplicateCandidate{a, b}, nil, nil)
	require.Len(t, got, 1)
	assert.Equal(t, StrengthMedium, got[0].Strength)
	assert.Equal(t, []string{EvidenceSameTitles}, codes(got[0]))
	assert.Contains(t, got[0].Reasons[0].Text, "2 notes")

	one := cand("app_aaaaaa", 40)
	two := cand("app_bbbbbb", 30)
	one.Titles, two.Titles = titles("Same single title"), titles("Same single title")
	got, _ = SuggestDuplicates([]DuplicateCandidate{one, two}, nil, nil)
	assert.Empty(t, got, "one shared title is not enough")
}

func TestSuggestDuplicates_AFolderThatIsGoneIsMediumAndTheSmallSideIsWeak(t *testing.T) {
	old := cand("app_aaaaaa", 20, Identity{Root: "/old/app"})
	now := cand("app_bbbbbb", 25, Identity{Root: "/new/app"})
	got, _ := SuggestDuplicates([]DuplicateCandidate{old, now}, nil, exists("/new/app"))
	require.Len(t, got, 1)
	assert.Equal(t, StrengthMedium, got[0].Strength)
	assert.Equal(t, "app_bbbbbb", got[0].Survivor)
	assert.Equal(t, []string{EvidencePathGone}, codes(got[0]))
	assert.Contains(t, got[0].Reasons[0].Text, "app_aaaaaa")

	// the bigger project is the one whose folder went away: the evidence is the same, said about it
	got, _ = SuggestDuplicates([]DuplicateCandidate{cand("app_aaaaaa", 90, Identity{Root: "/old/app"}), now}, nil, exists("/new/app"))
	require.Len(t, got, 1)
	assert.Equal(t, "app_aaaaaa", got[0].Survivor)
	assert.Contains(t, got[0].Reasons[0].Text, "app_aaaaaa")

	got, _ = SuggestDuplicates([]DuplicateCandidate{cand("app_aaaaaa", 40), cand("app_bbbbbb", 2)}, nil, nil)
	require.Len(t, got, 1)
	assert.Equal(t, StrengthWeak, got[0].Strength)
	assert.Equal(t, []string{EvidenceFewNotes}, codes(got[0]))
	assert.Equal(t, "app_aaaaaa", got[0].Survivor)

	got, _ = SuggestDuplicates([]DuplicateCandidate{cand("app_aaaaaa", 2), cand("app_bbbbbb", 3)}, nil, nil)
	assert.Empty(t, got, "two small projects are no sign of anything")
}

func TestSuggestDuplicates_DismissalsAreRememberedPerPairAndCounted(t *testing.T) {
	r := Identity{Remote: "git.example.org/team/shop"}
	cs := []DuplicateCandidate{cand("shop_aaaaaa", 40, r), cand("shop_bbbbbb", 30, r), cand("shop_cccccc", 5, r)}
	got, n := SuggestDuplicates(cs, nil, nil)
	assert.Len(t, got, 3, "three clones: three pairs")
	assert.Zero(t, n)

	dismissed := func(a, b string) bool {
		return (a == "shop_cccccc" && b == "shop_aaaaaa") || (b == "shop_cccccc" && a == "shop_aaaaaa")
	}
	got, n = SuggestDuplicates(cs, dismissed, nil)
	assert.Len(t, got, 2, "only the dismissed pair is left out; the other pairs of the group stay")
	assert.Equal(t, 1, n)
}

func TestSuggestDuplicates_StrongestFirstThenTheBiggest(t *testing.T) {
	r1, r2 := Identity{Remote: "git.example.org/a/x"}, Identity{Remote: "git.example.org/a/y"}
	weakA, weakB := cand("w_aaaaaa", 50), cand("w_bbbbbb", 1)
	got, _ := SuggestDuplicates([]DuplicateCandidate{weakA, weakB, cand("y_aaaaaa", 5, r2), cand("y_bbbbbb", 5, r2), cand("x_aaaaaa", 30, r1), cand("x_bbbbbb", 30, r1)}, nil, nil)
	require.Len(t, got, 3)
	assert.Equal(t, []string{StrengthStrong, StrengthStrong, StrengthWeak}, []string{got[0].Strength, got[1].Strength, got[2].Strength})
	assert.Equal(t, "x_aaaaaa", got[0].Survivor, "among equals the pair with more notes comes first")
	assert.Equal(t, "y_aaaaaa", got[1].Survivor)
}

func TestSuggestDuplicates_TieGoesToTheMoreRecentlyActive(t *testing.T) {
	r := Identity{Remote: "git.example.org/a/x"}
	a, b := cand("x_aaaaaa", 10, r), cand("x_bbbbbb", 10, r)
	a.LastActiveEpoch, b.LastActiveEpoch = 100, 200
	got, _ := SuggestDuplicates([]DuplicateCandidate{a, b}, nil, nil)
	require.Len(t, got, 1)
	assert.Equal(t, "x_bbbbbb", got[0].Survivor)
	assert.Empty(t, mustNone(SuggestDuplicates(nil, nil, nil)))
}

func mustNone(s []DuplicateSuggestion, _ int) []DuplicateSuggestion { return s }
