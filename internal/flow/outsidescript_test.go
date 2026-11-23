package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// outsideScriptHarness starts a run whose workflow names a script outside the repository, and leaves the run open at the question of the owner.
// It returns the harness, the path of the script and a client of another build.
func outsideScriptHarness(t *testing.T) (*harness, string, *Client) {
	t.Helper()

	script := filepath.Join(t.TempDir(), "notify.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho one\n"), 0o700))

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
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho two\n"), 0o700))

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorContains(t, err, script+" changed since the run started")
	require.ErrorContains(t, err, "beehave resume "+testItem+" --on-this-build --on-this-file", "the refusal says how to go on")
}

func TestClient_ResumeOnThisBuildMovesARunWhoseOutsideScriptDidNotChange(t *testing.T) {
	t.Parallel()

	_, _, other := outsideScriptHarness(t)

	require.NoError(t, other.ResumeOnThisBuild(testItem))
}
