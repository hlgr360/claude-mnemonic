package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObservationJSON_ArchivedFields(t *testing.T) {
	live := &Observation{ID: 1}
	raw, err := json.Marshal(live)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.NotContains(t, m, "is_archived", "a live note says nothing about being archived")
	assert.NotContains(t, m, "archived_reason")

	archived := &Observation{ID: 2, IsArchived: true, ArchivedReason: "rolled-up into #7"}
	raw, err = json.Marshal(archived)
	require.NoError(t, err)
	m = nil
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, true, m["is_archived"])
	assert.Equal(t, "rolled-up into #7", m["archived_reason"])
}
