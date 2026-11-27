package flow

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
)

// outsideScriptHarness starts a run whose workflow names a script outside the repository, and leaves the run open at the question of the owner.
// It returns the harness, the path of the script and a client of another build.
func outsideScriptHarness(t *testing.T) (*harness, string, *Client) {
	t.Helper()

	script := filepath.Join(t.TempDir(), "notify.sh")
	require.NoError(t, exectest.Write(script, "#!/bin/sh\necho one\n"))

	harness := newHarness(t)
	harness.flowIs(t, waitFlow+"    command(run = [\""+script+"\"])\n")
	engine := harness.engine(t)

	runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))
	expireLease(harness)

	return harness, script, harness.client(t, otherBuild)
}

func TestClient_ResumeOnThisBuildRefusesARunWhoseOutsideScriptChangedAndNamesIt(t *testing.T) {
	t.Parallel()

	_, script, other := outsideScriptHarness(t)
	require.NoError(t, exectest.Write(script, "#!/bin/sh\necho two\n"))

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorContains(t, err, script+" changed since the run started")
	require.NotContains(t, err.Error(), "outside:", "the refusal shows the plain path")
	require.ErrorContains(t, err, "beehest resume "+testItem+" --on-this-build --on-this-file", "the refusal says how to go on")
}

func TestClient_ResumeOnThisBuildMovesARunWhoseOutsideScriptDidNotChange(t *testing.T) {
	t.Parallel()

	_, _, other := outsideScriptHarness(t)

	require.NoError(t, other.ResumeOnThisBuild(testItem))
}
