package beads_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/store"
	"github.com/tobocop2/beehest/internal/store/beads"
	"github.com/tobocop2/beehest/internal/store/beads/beadstest"
	"github.com/tobocop2/beehest/internal/store/storetest"
)

// sharedRepeats is the number of times each two-writer test runs its schedule.
const sharedRepeats = 5

// bdLookups is the number of lookups of two writers: each one lists the items that hold the key.
const bdLookups = 2

// twoBdWriters returns a fake bd and two writers over it, behind a gate that holds each create until both writers looked the key up.
func twoBdWriters(t *testing.T) (*writeBd, [2]store.Writer) {
	t.Helper()

	fake := newWriteBd()
	gate := storetest.NewCreateGate(fake, 0, bdLookups, storetest.CreateWait)

	var writers [2]store.Writer

	for i := range writers {
		writer, err := beads.New(gate, beadstest.Block("p"))
		require.NoError(t, err)

		writers[i] = writer
	}

	return fake, writers
}

func TestWriter_TwoWritersOfOneSharedKeyUnderTheKeyLockFileOneItem(t *testing.T) {
	t.Parallel()

	for range sharedRepeats {
		fake, writers := twoBdWriters(t)

		ids := storetest.CreateTogether(t, t.TempDir(), writers, "foreign:o/r:41", true)

		assert.Len(t, fake.issues, 1, "the second writer looks the key up after the first one filed")
		assert.Equal(t, ids[0], ids[1])
	}
}

func TestWriter_TwoWritersOfOneSharedKeyWithNoLockFileTwoItems(t *testing.T) {
	t.Parallel()

	fake, writers := twoBdWriters(t)

	storetest.CreateTogether(t, t.TempDir(), writers, "foreign:o/r:41", false)

	assert.Len(t, fake.issues, 2, "the control: the schedule of the gate files two items when no lock holds the lookup and the create together")
}
