package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func selectorItems() []model.Item {
	tagged := queueItem("21", model.SeverityP1)
	tagged.Labels = []string{"beehave-round", "bug"}

	other := queueItem("22", model.SeverityP1)
	other.Labels = []string{"bug"}

	return []model.Item{tagged, other, queueItem("23", model.SeverityP1)}
}

func TestQueue_ALabelArgumentTakesOnlyTheReadyRowsWithTheLabel(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, selectorItems())

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`name = "q", label = "beehave-round"`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:ready:P1:0", "free=2"}, seenBy(t, h), "a ready row without the label stays out of the queue")
}

func TestQueue_AnItemsArgumentIsNoArgumentOfTheCall(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, selectorItems())

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`**{"name": "q", "items": ["23"]}`)), modeOff())

	require.ErrorContains(t, err, "items", "a workflow picks its items from the rows, so the call takes no list of items")
}

func TestQueue_ACallWithNoSelectorQueuesEveryRow(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, selectorItems())

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`name = "q"`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:ready:P1:0", "22:ready:P1:0", "23:ready:P1:0", "free=2"}, seenBy(t, h))
}

func TestFacts_NoFactIsNamedForARound(t *testing.T) {
	t.Parallel()

	for name := range factsOutputs(model.Project{}) {
		assert.NotContains(t, name, "round", "the fact %q is named for a round: the workflow reads its settings from vars", name)
	}
}
