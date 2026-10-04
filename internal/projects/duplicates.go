package projects

import (
	"fmt"
	"sort"
	"strings"
)

// Strength of the evidence that two projects are one.
const (
	StrengthStrong = "strong" // the same git remote
	StrengthMedium = "medium" // the same notes, or a folder that is gone
	StrengthWeak   = "weak"   // only that one side holds very little
)

// Evidence codes.
const (
	EvidenceSameRemote = "same_remote"
	EvidenceSameTitles = "same_titles"
	EvidencePathGone   = "path_gone"
	EvidenceFewNotes   = "few_notes"
)

// fewNotes is how small a project may be to count as "holds very little".
const fewNotes = 3

// DuplicateCandidate is one project, with what the suggestion needs to know about it.
type DuplicateCandidate struct {
	// Titles are the notes' titles, lower-cased and trimmed (see NormalizeTitle).
	Titles          map[string]bool
	ID              string
	Identities      []Identity
	Observations    int
	LastActiveEpoch int64
}

// DuplicateReason is one piece of evidence, with a sentence for a person.
type DuplicateReason struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// DuplicateSuggestion says that two projects are probably one. Survivor is the one with more data; the merge moves Other into it.
type DuplicateSuggestion struct {
	Survivor string            `json:"survivor"`
	Other    string            `json:"other"`
	Strength string            `json:"strength"`
	Reasons  []DuplicateReason `json:"reasons"`
	// AutoMergeable is true only for the strongest evidence: the same git remote, and every recorded folder of Other
	// is gone, so it is a checkout that moved or was deleted and not a second live clone.
	AutoMergeable bool `json:"auto_mergeable"`
}

// NormalizeTitle makes titles comparable.
func NormalizeTitle(t string) string { return strings.ToLower(strings.Join(strings.Fields(t), " ")) }

// SuggestDuplicates finds the pairs of projects that are probably one project, strongest first. Name finds the
// candidates (the same display name, or the same git remote under different names); evidence confirms them. Two
// projects that share only a name are never suggested without evidence, and two with different remotes are real
// namesakes and never suggested. dismissed reports a pair a person said is not the same; those are left out and
// counted. pathExists tells whether a recorded checkout folder is still there.
func SuggestDuplicates(cands []DuplicateCandidate, dismissed func(a, b string) bool, pathExists func(string) bool) (suggestions []DuplicateSuggestion, dismissedCount int) {
	byName := map[string][]int{}
	byRemote := map[string][]int{}
	for i, c := range cands {
		byName[DisplayName(c.ID)] = append(byName[DisplayName(c.ID)], i)
		for _, r := range remotesOf(c) {
			byRemote[r] = append(byRemote[r], i)
		}
	}
	pairs := map[[2]int]bool{}
	add := func(group []int) {
		for x := 0; x < len(group); x++ {
			for y := x + 1; y < len(group); y++ {
				pairs[[2]int{group[x], group[y]}] = true
			}
		}
	}
	for _, g := range byName {
		add(g)
	}
	for _, g := range byRemote {
		add(g)
	}

	for pair := range pairs {
		a, b := cands[pair[0]], cands[pair[1]]
		s, ok := assess(a, b, pathExists)
		if !ok {
			continue
		}
		if dismissed != nil && dismissed(a.ID, b.ID) {
			dismissedCount++
			continue
		}
		suggestions = append(suggestions, s)
	}

	rank := map[string]int{StrengthStrong: 3, StrengthMedium: 2, StrengthWeak: 1}
	size := func(s DuplicateSuggestion) int {
		n := 0
		for _, c := range cands {
			if c.ID == s.Survivor || c.ID == s.Other {
				n += c.Observations
			}
		}
		return n
	}
	sort.Slice(suggestions, func(i, j int) bool {
		si, sj := suggestions[i], suggestions[j]
		if rank[si.Strength] != rank[sj.Strength] {
			return rank[si.Strength] > rank[sj.Strength]
		}
		if size(si) != size(sj) {
			return size(si) > size(sj)
		}
		return si.Survivor+si.Other < sj.Survivor+sj.Other
	})
	return suggestions, dismissedCount
}

func remotesOf(c DuplicateCandidate) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range c.Identities {
		if id.Remote != "" && !seen[id.Remote] {
			seen[id.Remote] = true
			out = append(out, id.Remote)
		}
	}
	return out
}

// assess weighs the evidence for one pair.
func assess(a, b DuplicateCandidate, pathExists func(string) bool) (DuplicateSuggestion, bool) {
	ra, rb := remotesOf(a), remotesOf(b)
	shared := ""
	for _, x := range ra {
		for _, y := range rb {
			if x == y {
				shared = x
			}
		}
	}
	sameName := DisplayName(a.ID) == DisplayName(b.ID)

	// Both know where they came from and it is not the same place: namesakes, not one project.
	if shared == "" && len(ra) > 0 && len(rb) > 0 {
		return DuplicateSuggestion{}, false
	}

	// The one with more data survives; on a tie the more recently active, then the id.
	survivor, other := a, b
	if b.Observations > a.Observations || (b.Observations == a.Observations && (b.LastActiveEpoch > a.LastActiveEpoch ||
		(b.LastActiveEpoch == a.LastActiveEpoch && b.ID < a.ID))) {
		survivor, other = b, a
	}

	var reasons []DuplicateReason
	if shared != "" {
		reasons = append(reasons, DuplicateReason{EvidenceSameRemote, "both were cloned from " + shared})
	}
	if inter := titleOverlap(a.Titles, b.Titles); inter >= 2 && float64(inter) >= 0.2*float64(minInt(len(a.Titles), len(b.Titles))) {
		reasons = append(reasons, DuplicateReason{EvidenceSameTitles, fmt.Sprintf("%d notes have the same title in both", inter)})
	}
	if gone, there := foldersGone(other, pathExists), foldersThere(survivor, pathExists); gone && there {
		reasons = append(reasons, DuplicateReason{EvidencePathGone, fmt.Sprintf("the folder of %s no longer exists", other.ID)})
	} else if gone2, there2 := foldersGone(survivor, pathExists), foldersThere(other, pathExists); gone2 && there2 {
		// The bigger project is the one whose folder went away: still the same evidence, said about it.
		reasons = append(reasons, DuplicateReason{EvidencePathGone, fmt.Sprintf("the folder of %s no longer exists", survivor.ID)})
	}
	if lo, hi := minInt(a.Observations, b.Observations), maxInt(a.Observations, b.Observations); lo <= fewNotes && hi > fewNotes {
		reasons = append(reasons, DuplicateReason{EvidenceFewNotes, fmt.Sprintf("%s holds only %d notes", other.ID, other.Observations)})
	}

	// Without a shared remote the name is the only reason to look, and a name alone is not evidence.
	if shared == "" && (!sameName || len(reasons) == 0) {
		return DuplicateSuggestion{}, false
	}
	strength := StrengthWeak
	for _, r := range reasons {
		switch r.Code {
		case EvidenceSameRemote:
			strength = StrengthStrong
		case EvidenceSameTitles, EvidencePathGone:
			if strength != StrengthStrong {
				strength = StrengthMedium
			}
		}
	}
	auto := shared != "" && foldersGone(other, pathExists)
	return DuplicateSuggestion{Survivor: survivor.ID, Other: other.ID, Strength: strength, Reasons: reasons, AutoMergeable: auto}, true
}

func titleOverlap(a, b map[string]bool) int {
	if len(a) > len(b) {
		a, b = b, a
	}
	n := 0
	for t := range a {
		if b[t] {
			n++
		}
	}
	return n
}

// foldersGone reports whether the project has recorded checkout folders and none of them exists any more.
func foldersGone(c DuplicateCandidate, pathExists func(string) bool) bool {
	n := 0
	for _, id := range c.Identities {
		if id.Root == "" {
			continue
		}
		n++
		if pathExists != nil && pathExists(id.Root) {
			return false
		}
	}
	return n > 0
}

// foldersThere reports whether at least one recorded checkout folder of the project still exists.
func foldersThere(c DuplicateCandidate, pathExists func(string) bool) bool {
	if pathExists == nil {
		return false
	}
	for _, id := range c.Identities {
		if id.Root != "" && pathExists(id.Root) {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
