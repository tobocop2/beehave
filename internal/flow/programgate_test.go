package flow

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGate_AProgramGateRecordsTheHeadAndTheTreeItRanOn(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.foldOf("1")

	body := `g = gate(run = ["check-all"], head = "made-up-head", name = "g")
command(name = "report", run = ["report", str(g.passed), "head=" + g.head, "tree=" + g.tree])
`
	_, err := rig.walk(t, body)

	require.NoError(t, err)
	assert.Equal(t, []string{"report True head=" + rig.repo.must(t, "rev-parse", "HEAD") + " tree=" + rig.repo.must(t, "rev-parse", "HEAD^{tree}")}, filterReport(rig.h.commands.ran()),
		"the gate records the head and the tree of the worktree it read, and not the head keyword")
}

func TestGate_AProgramGateOnAWorktreeWithUncommittedChangesIsRefused(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	require.NoError(t, os.WriteFile(filepath.Join(rig.repo.dir, "README"), []byte("not committed\n"), 0o600))

	_, err := rig.walk(t, `g = gate(run = ["check-all"], name = "g")
`)

	require.ErrorContains(t, err, "has changes that are not committed")
	assert.False(t, slices.Contains(rig.h.commands.ran(), "check-all"), "the program never runs on a dirty worktree")
}
