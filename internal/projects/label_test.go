package projects

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func ms(date string) int64 {
	t, _ := time.ParseInLocation("2006-01-02", date, time.Local)
	return t.Add(12 * time.Hour).UnixMilli()
}

func TestLabels_UniqueNamesAreUsedAsTheyAre(t *testing.T) {
	got := Labels([]Info{{ID: "claude-mnemonic_41bfcd", Observations: 70}, {ID: "billing_api_b6d754", Observations: 97}}, nil)

	assert.Equal(t, Label{Label: "claude-mnemonic", Use: "claude-mnemonic"}, got["claude-mnemonic_41bfcd"])
	assert.Equal(t, Label{Label: "billing_api", Use: "billing_api"}, got["billing_api_b6d754"], "underscores inside a name are part of the name")
}

func TestLabels_NamesakesAreDistinguishedAndMustBeUsedById(t *testing.T) {
	got := Labels([]Info{
		{ID: "app_40d35c", Observations: 49, LastActiveEpoch: ms("2026-10-01"), SampleTitle: "Naming rules"},
		{ID: "app_4a4494", Observations: 1, LastActiveEpoch: ms("2026-10-02")},
		{ID: "other_111111", Observations: 3},
	}, nil)

	assert.Equal(t, Label{Label: `app (49 observations, last used 2026-10-01, e.g. "Naming rules")`, Use: "app_40d35c"}, got["app_40d35c"])
	assert.Equal(t, Label{Label: "app (1 observation, last used 2026-10-02)", Use: "app_4a4494"}, got["app_4a4494"])
	assert.Equal(t, "other", got["other_111111"].Use, "an unrelated unique name is unaffected")
}

func TestLabels_NamesAreComparedCaseInsensitively(t *testing.T) {
	got := Labels([]Info{{ID: "App_aaaaaa"}, {ID: "app_bbbbbb"}}, nil)
	assert.Equal(t, "App_aaaaaa", got["App_aaaaaa"].Use, "\"App\" and \"app\" resolve as the same name, so neither may be used by name")
	assert.Equal(t, "app_bbbbbb", got["app_bbbbbb"].Use)
}

func TestLabels_AliasesOfOneProjectAreNotNamesakes(t *testing.T) {
	aliases := map[string]string{"app_ffffff": "app_aaaaaa"} // a fragment that still holds data, merged logically
	got := Labels([]Info{{ID: "app_aaaaaa", Observations: 5}, {ID: "app_ffffff", Observations: 2}}, aliases)

	assert.Equal(t, Label{Label: "app", Use: "app"}, got["app_aaaaaa"])
	assert.Equal(t, Label{Label: "app", Use: "app"}, got["app_ffffff"], "the alias shares its project's label")
}

func TestLabels_AnAliasPointingAtAProjectNotListedStillLabelsSensibly(t *testing.T) {
	got := Labels([]Info{{ID: "frag_ffffff", Observations: 2}}, map[string]string{"frag_ffffff": "main_aaaaaa"})
	assert.Equal(t, Label{Label: "main", Use: "main"}, got["frag_ffffff"])
}

func TestLabels_AliasedNamesakesStayDistinctFromAnotherRealProject(t *testing.T) {
	aliases := map[string]string{"app_ffffff": "app_aaaaaa"}
	got := Labels([]Info{{ID: "app_aaaaaa", Observations: 5}, {ID: "app_ffffff"}, {ID: "app_bbbbbb", Observations: 7}}, aliases)

	assert.Equal(t, "app_aaaaaa", got["app_aaaaaa"].Use)
	assert.Equal(t, "app_aaaaaa", got["app_ffffff"].Use, "the alias is referred to by its project's id")
	assert.Equal(t, "app_bbbbbb", got["app_bbbbbb"].Use)
}

func TestLabels_Empty(t *testing.T) {
	assert.Empty(t, Labels(nil, nil))
}

func TestDetail(t *testing.T) {
	assert.Equal(t, "0 observations", Detail(Info{}))
	assert.Equal(t, "1 observation", Detail(Info{Observations: 1}))
	assert.Equal(t, `2 observations, e.g. "A title"`, Detail(Info{Observations: 2, SampleTitle: "  A title "}))
	assert.Equal(t, "3 observations, last used 2026-10-01", Detail(Info{Observations: 3, LastActiveEpoch: ms("2026-10-01")}))
}
