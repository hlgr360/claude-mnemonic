package worker

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/worker/sdk"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/models"
)

const (
	// The ladder: with a target, how old a note must be to be rolled up grows as the project is nearer its target.
	ladderCalmDays = 90
	ladderOverDays = 60
	ladderFarDays  = 30

	// quarterSettleDays is how long after a quarter ended its monthly roll-ups are condensed into the quarter note: by then
	// every raw note of the quarter is old enough to have been rolled into a month, whichever step of the ladder applies.
	quarterSettleDays = 90
	// quarterMinRollups is how many monthly roll-ups a quarter needs to be worth a quarter note.
	quarterMinRollups = 2

	// rollupRestoreGraceDays is how long the notes of a roll-up or consolidation that a person restored are left alone, so
	// the next pass does not fold them again straight away.
	rollupRestoreGraceDays = 30
)

// rollupPressure says how old a note must be to be rolled up in a project, and how many groups a pass may write for it
// (as a multiple of ROLLUP_MAX_GROUPS_PER_RUN), given how many live notes the project holds.
type rollupPressure struct {
	// Name is "" without a target, else "calm" (up to the target), "over target" or "far over target" (over twice).
	Name    string
	AgeDays int
	Factor  int
}

// rollupPressureFor applies the ladder. Without a target the fixed ROLLUP_MIN_AGE_DAYS applies, as before.
func rollupPressureFor(cfg *config.Config, live int) rollupPressure {
	target := cfg.RollupTargetLiveNotes
	if target <= 0 {
		age := cfg.RollupMinAgeDays
		if age <= 0 {
			age = 60
		}
		return rollupPressure{AgeDays: age, Factor: 1}
	}
	switch {
	case live <= target:
		return rollupPressure{Name: "calm", AgeDays: ladderCalmDays, Factor: 1}
	case live <= 2*target:
		return rollupPressure{Name: "over target", AgeDays: ladderOverDays, Factor: 2}
	}
	return rollupPressure{Name: "far over target", AgeDays: ladderFarDays, Factor: 3}
}

// quarterOf returns the calendar quarter (UTC) a time falls in.
func quarterOf(t time.Time) (year, quarter int) {
	t = t.UTC()
	return t.Year(), (int(t.Month())-1)/3 + 1
}

// quarterLabel is "2026-Q1".
func quarterLabel(year, quarter int) string { return fmt.Sprintf("%d-Q%d", year, quarter) }

// quarterEnd is the first instant after the quarter.
func quarterEnd(year, quarter int) time.Time {
	return time.Date(year, time.Month(quarter*3+1), 1, 0, 0, 0, 0, time.UTC)
}

var monthLabelRe = regexp.MustCompile(`^(\d{4})-(\d{2})`)

// rollupQuarter says which quarter a monthly roll-up belongs to: by the month in its fold's label ("2026-03", or
// "2026-03 (part 2)"), which survives a roll-up that kept today's date because a cap is set, else by its own date.
func rollupQuarter(label string, createdEpoch int64) (year, quarter int) {
	if m := monthLabelRe.FindStringSubmatch(label); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		if mo >= 1 && mo <= 12 {
			return y, (mo-1)/3 + 1
		}
	}
	return quarterOf(time.UnixMilli(createdEpoch))
}

// groupQuarters groups the monthly roll-ups (oldest first) by calendar quarter and keeps the quarters that ended more than
// settleDays ago and hold at least minRollups roll-ups. labels names the month of a roll-up by its id (see
// ObservationFoldStore.RollupLabels). The result is oldest quarter first and depends only on its input.
func groupQuarters(rollups []*models.Observation, labels map[int64]string, now time.Time, settleDays, minRollups int) []rollupGroup {
	type key struct{ year, quarter int }
	byQuarter := map[key][]*models.Observation{}
	for _, r := range rollups {
		y, q := rollupQuarter(labels[r.ID], r.CreatedAtEpoch)
		byQuarter[key{y, q}] = append(byQuarter[key{y, q}], r)
	}
	var keys []key
	for k := range byQuarter {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].year != keys[j].year {
			return keys[i].year < keys[j].year
		}
		return keys[i].quarter < keys[j].quarter
	})
	var out []rollupGroup
	for _, k := range keys {
		members := byQuarter[k]
		if len(members) < minRollups || quarterEnd(k.year, k.quarter).Add(time.Duration(settleDays)*24*time.Hour).After(now) {
			continue
		}
		sort.SliceStable(members, func(i, j int) bool { return members[i].CreatedAtEpoch < members[j].CreatedAtEpoch })
		out = append(out, rollupGroup{Level: sdk.LevelQuarter, Label: quarterLabel(k.year, k.quarter), Notes: members})
	}
	return out
}
