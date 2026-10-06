package worker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTopIDsByRelevance(t *testing.T) {
	t.Run("the best scores win the cut, not the newest ids", func(t *testing.T) {
		// ids 1..6, id 1 the oldest and the best match
		scores := map[int64]float64{1: 0.9, 2: 0.2, 3: 0.3, 4: 0.4, 5: 0.5, 6: 0.6}
		assert.Equal(t, []int64{1, 6, 5}, topIDsByRelevance([]int64{6, 5, 4, 3, 2, 1}, scores, 3))
	})
	t.Run("without scores the order given is the ranking", func(t *testing.T) {
		assert.Equal(t, []int64{9, 3}, topIDsByRelevance([]int64{9, 3, 7, 1}, nil, 2))
	})
	t.Run("equal scores keep the order they came in", func(t *testing.T) {
		scores := map[int64]float64{4: 0.5, 5: 0.5, 6: 0.5}
		assert.Equal(t, []int64{5, 4, 6}, topIDsByRelevance([]int64{5, 4, 6}, scores, 0))
	})
	t.Run("a limit of zero keeps all, and the input is not changed", func(t *testing.T) {
		in := []int64{1, 2, 3}
		got := topIDsByRelevance(in, map[int64]float64{3: 1}, 0)
		assert.Equal(t, []int64{3, 1, 2}, got)
		assert.Equal(t, []int64{1, 2, 3}, in)
	})
	t.Run("nothing in, nothing out", func(t *testing.T) {
		assert.Empty(t, topIDsByRelevance(nil, nil, 5))
	})
}
