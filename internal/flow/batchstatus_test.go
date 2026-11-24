package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusesGiven lists each status that the merge calls were given, as "context state sha", in the order of the calls.
func (m *landMerger) statusesGiven() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var got []string

	for _, settings := range m.given {
		for _, status := range settings.Statuses {
			got = append(got, status.Context+" "+string(status.State)+" "+status.SHA)
		}
	}

	return got
}

func TestBatchMerge_EachMergeOfAGreenUnionCarriesTheGateStatusOfItsHeadAndTheReviewStatusOfAnApprovedOne(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	prs := `[struct(item = "1", head = "` + rig.heads["1"] + `", approved = True, closes = []), struct(item = "3", head = "` + rig.heads["3"] + `", closes = [])]`

	_, err := rig.walk(t, `out = merge(prs = `+prs+`, base = "main", repo = "o/r", name = "b")`)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"beehave/review success " + rig.heads["1"],
		"beehave/gate success " + rig.heads["1"],
		"beehave/gate success " + rig.heads["3"],
	}, rig.merger.statusesGiven(), "the merge run of each member posts these after its guards")
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
}

func TestBatchMerge_AMemberThatFailsTheGateIsGivenNoStatus(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.call("2", ""))

	require.NoError(t, err)
	assert.Empty(t, rig.merger.statusesGiven(), "the gate was red for the only member, so no merge runs and no head has a status")
	assert.Empty(t, rig.merger.landed())
}
