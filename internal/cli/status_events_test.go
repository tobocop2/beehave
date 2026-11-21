package cli_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
)

func TestStatus_EventsPrintsTheKeyAndTheLastValueOfEachPublishedKey(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.published = []flow.PublishedValue{{Key: "progress", Value: []byte(`{"done":1}`)}, {Key: "note", Value: []byte(`"hi"`)}}

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1", "--events")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"published item-14-r1"}, rig.steering.calls)
	assert.Equal(t, "progress\t{\"done\":1}\nnote\t\"hi\"\n", stdout)
	assert.True(t, rig.steering.closeOK)
}

func TestStatus_EventsOfARunThatPublishedNothingSaysSo(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1", "--events")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "run item-14-r1 has published no keys\n", stdout)
}

func TestStatus_EventsWithJSONPrintsOneList(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.published = []flow.PublishedValue{{Key: "progress", Value: []byte(`{"done":1}`)}}

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1", "--events", "--json")

	require.Equal(t, 0, code, stderr)
	assert.JSONEq(t, `[{"key":"progress","value":{"done":1}}]`, stdout)
}

func TestStatus_APublishedReadThatFailsFailsTheEventsView(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.publishedErr = errors.New("the store is gone")

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1", "--events")

	assert.Equal(t, 1, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "the store is gone")
}

func TestStatus_EventsNeedsExactlyOneRunIdAndNoItem(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no run id": {"status", "--events"},
		"with item": {"status", "--events", "--item", "14"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)

			code, _, stderr := rig.execute(t.Context(), args...)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr, "--events takes one run id")
			assert.Empty(t, rig.steering.calls)
		})
	}
}
