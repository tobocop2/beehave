package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

// The tests below keep the answers of the Go claim stops that claim_stop replaced. Each case is a pull request on which the base gave one answer.

func TestClaimStop_AnEmptyLoginEndsTheSearchForAnAssigneeAsTheOldStopDid(t *testing.T) {
	t.Parallel()

	empty := pullFake(func(f *model.PullFacts) { f.Assignees = []string{"", "maintainer"} })

	assert.Equal(t, clearLine, claimRun(t, empty, `name = "a", `+ownerLogins).line, "an assignee with no login ends the search, as before")

	later := pullFake(func(f *model.PullFacts) { f.Assignees = []string{"owner", "", "maintainer"} })

	assert.Equal(t, clearLine, claimRun(t, later, `name = "a", `+ownerLogins).line)
}

func TestClaimStop_ALabelWithACommaHoldsWhenOneOfItsPartsIsTheHoldLabel(t *testing.T) {
	t.Parallel()

	held := pullFake(func(f *model.PullFacts) { f.Labels = []string{"a,hold"} })

	got := claimRun(t, held, `name = "a", hold = "hold"`)

	require.NoError(t, got.err)
	assert.Contains(t, got.line, "False|HELD|9|", "the label list is split at a comma, as before")
}

func TestClaimStop_AnAssigneeThatCanBeReadStopsBeforeALinkThatCannot(t *testing.T) {
	t.Parallel()

	fake := pullFake(func(f *model.PullFacts) {
		f.Assignees = []string{"maintainer"}
		f.Unread = []string{"cannot read the link #99999999999999999999 in the body"}
	})

	got := claimRun(t, fake, `name = "a", `+ownerLogins)

	require.NoError(t, got.err)
	assert.Contains(t, got.line, "False|ASSIGNED|11|STOP: #7 is assigned to 'maintainer'", "the assignee that is read stops the item, as before")
}

func TestClaimStop_AReadWithNoAssigneeListOrABadLinkIsRefusedOnlyWithOwnLogins(t *testing.T) {
	t.Parallel()

	for name, unread := range map[string]string{
		"no assignee list": "the read has no assignees",
		"a bad link":       "cannot read the link #99999999999999999999 in the body",
	} {
		fake := pullFake(func(f *model.PullFacts) { f.Unread = []string{unread} })

		assert.Equal(t, clearLine, claimRun(t, fake, `name = "a"`).line, name+": no own logins reads neither")

		got := claimRun(t, fake, `name = "a", `+ownerLogins)

		require.Error(t, got.err, name)
		assert.ErrorContains(t, got.err, "cannot read #7: "+unread, name)
	}
}
