package skypilot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sizeTableText = "3:n2-standard-4,n2-standard-16"

// launchedType starts the host with queued waiting jobs and returns the value of the -t flag of the launch, or "" when the launch has none.
func launchedType(t *testing.T, placement *placed, queued int) string {
	t.Helper()

	h, _, sky, _ := newHost(t, hostConfig(placement))
	sky.Plan("", "launch")

	require.NoError(t, h.UpQueued(context.Background(), queued))
	require.Len(t, sky.Calls, 1)

	for i, arg := range sky.Calls[0] {
		if arg == "-t" {
			return sky.Calls[0][i+1]
		}
	}

	return ""
}

func TestUpQueued_AQueueOfOneAndAQueueOfTwelveStartTheTwoSizesOfTheTable(t *testing.T) {
	t.Parallel()

	table := &placed{instanceSizes: sizeTableText}

	assert.Equal(t, "n2-standard-4", launchedType(t, table, 1))
	assert.Equal(t, "n2-standard-16", launchedType(t, table, 12))
}

func TestUpQueued_TheLimitOfARowHoldsTheQueueOfThatLength(t *testing.T) {
	t.Parallel()

	table := &placed{instanceSizes: sizeTableText}

	assert.Equal(t, "n2-standard-4", launchedType(t, table, 3))
	assert.Equal(t, "n2-standard-16", launchedType(t, table, 4))
}

func TestUpQueued_AnInstanceTypeAloneIsATableOfOneRowForEveryQueue(t *testing.T) {
	t.Parallel()

	single := &placed{instanceType: "n2-standard-8"}

	assert.Equal(t, "n2-standard-8", launchedType(t, single, 0))
	assert.Equal(t, "n2-standard-8", launchedType(t, single, 40))
}

func TestUpQueued_NoSizeLeavesTheChoiceToSkyPilot(t *testing.T) {
	t.Parallel()

	assert.Empty(t, launchedType(t, &placed{cloud: "gcp"}, 12))
}

func TestUp_StartsTheFirstRowOfTheTable(t *testing.T) {
	t.Parallel()

	h, _, sky, _ := newHost(t, hostConfig(&placed{instanceSizes: sizeTableText}))
	sky.Plan("", "launch")

	require.NoError(t, h.Up(context.Background()))
	assert.Contains(t, sky.Calls[0], "n2-standard-4")
}

func TestNew_RefusesABadSizeTableAndNamesTheKey(t *testing.T) {
	t.Parallel()

	for name, table := range map[string]string{
		"last_row_has_a_limit":      "3:a,9:b",
		"a_middle_row_has_no_limit": "a,b",
		"limits_do_not_rise":        "5:a,5:b,c",
		"limits_fall":               "5:a,3:b,c",
		"limit_is_not_a_number":     "x:a,b",
		"limit_is_zero":             "0:a,b",
		"type_is_an_option":         "3:--x,b",
		"type_is_empty":             "3:a,",
	} {
		_, err := New("c", hostConfig(&placed{instanceSizes: table}), nil, nil, nil)

		require.ErrorIs(t, err, ErrConfig, name)
		assert.Contains(t, err.Error(), "host.skypilot.instance_sizes", name)
	}
}

func TestNew_RefusesAnInstanceTypeWithASizeTable(t *testing.T) {
	t.Parallel()

	_, err := New("c", hostConfig(&placed{instanceType: "a", instanceSizes: "3:a,b"}), nil, nil, nil)

	require.ErrorIs(t, err, ErrConfig)
	assert.Contains(t, err.Error(), "instance_type")
	assert.Contains(t, err.Error(), "instance_sizes")
}
