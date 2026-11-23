package workflow_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

func TestDebouncePeriod_ReadsTheWindowOfAStartAndRefusesZeroAndNoDuration(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		text string
		want time.Duration
		err  string
	}{
		"empty is no debounce": {text: "", want: 0},
		"thirty seconds":       {text: "30s", want: 30 * time.Second},
		"zero":                 {text: "0s", err: `debounce is "0s", which must be above zero. Write a duration such as "30s", or leave debounce out to start at once`},
		"below zero":           {text: "-1m", err: `debounce is "-1m", which must be above zero. Write a duration such as "30s", or leave debounce out to start at once`},
		"no duration":          {text: "soon", err: `debounce is "soon", which is not a duration. Write a duration such as "90s" or "10m"`},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := workflow.DebouncePeriod(c.text)
			if c.err != "" {
				require.EqualError(t, err, c.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestCheckDebounce_RefusesAWindowBelowZeroAndTakesZeroAsNone(t *testing.T) {
	t.Parallel()

	require.NoError(t, workflow.CheckDebounce(0))
	require.NoError(t, workflow.CheckDebounce(time.Second))
	require.EqualError(t, workflow.CheckDebounce(-time.Second), `debounce is "-1s", which must be above zero. Write a duration such as "30s", or leave debounce out to start at once`)
}

func TestCheck_ADebounceKeywordOfSpawnIsAnOptionOfBeehaveRun(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    spawn([\"1\"], debounce = \"30s\")\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "2:18 spawn takes no argument debounce. debounce is an option of beehave run.")
}
