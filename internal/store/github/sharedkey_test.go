package github_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/store/github"
	"github.com/tobocop2/beehave/internal/store/storetest"
)

// sharedRepeats is the number of times each two-writer test runs its schedule.
const sharedRepeats = 5

// ghLookups is the number of lookups of two writers: each one searches the key and then lists the newest issues.
const ghLookups = 4

// twoGhWriters returns a fake gh and two writers over it, behind a gate that holds each create until both writers looked the key up.
func twoGhWriters(t *testing.T) (*fakeGh, [2]store.Writer) {
	t.Helper()

	fake := newFakeGh()
	gate := storetest.NewCreateGate(fake, 1, ghLookups, storetest.CreateWait)

	var writers [2]store.Writer

	for i := range writers {
		writer, err := github.New(ghRepo, strict(gate))
		require.NoError(t, err)

		writers[i] = writer
	}

	return fake, writers
}

func TestWriter_TwoWritersOfOneSharedKeyUnderTheKeyLockFileOneIssue(t *testing.T) {
	t.Parallel()

	for range sharedRepeats {
		fake, writers := twoGhWriters(t)

		ids := storetest.CreateTogether(t, t.TempDir(), writers, "foreign:o/r:41", true)

		assert.Len(t, fake.issues, 1, "the second writer looks the key up after the first one filed")
		assert.Equal(t, ids[0], ids[1])
	}
}

func TestWriter_TwoWritersOfOneSharedKeyWithNoLockFileTwoIssues(t *testing.T) {
	t.Parallel()

	fake, writers := twoGhWriters(t)

	storetest.CreateTogether(t, t.TempDir(), writers, "foreign:o/r:41", false)

	assert.Len(t, fake.issues, 2, "the control: the schedule of the gate files two issues when no lock holds the lookup and the create together")
}
