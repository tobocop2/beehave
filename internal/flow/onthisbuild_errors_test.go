package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_ResumeOnThisBuildRefusesWhatItCannotMove(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	other := harness.client(t, otherBuild)

	require.ErrorIs(t, other.ResumeOnThisBuild("not an id"), ErrBadItem)
	require.ErrorIs(t, other.ResumeOnThisBuild(testItem), errNoRun)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	err = other.ResumeOnThisBuild(testItem)
	require.ErrorIs(t, err, errWrongState, "a finished run has nothing to move")
	require.ErrorContains(t, err, "only a cancelled, failed or open run moves to this build") //nolint:misspell // user-facing state word
}

func TestClient_ResumeOnThisBuildRefusesARunWhoseStepsDifferInFormatOnly(t *testing.T) {
	t.Parallel()

	harness, _, _ := strandedHarness(t)
	client := harness.client(t, testVersion)
	client.version = testVersion // the run is stored with the version of the steps, this build without it

	require.ErrorIs(t, client.ResumeOnThisBuild(testItem), errStranded)
}

func TestClient_ResumeOnThisBuildNamesTheScriptsOutsideTheRepositoryWhenAFileChanged(t *testing.T) {
	t.Parallel()

	const outside = "    command(run = [\"/bin/sh\", \"-c\", \"true\"])\n"

	harness := newHarness(t)
	harness.flowIs(t, waitFlow+outside)
	engine := harness.engine(t)

	runInBackground(engine, testItem)
	waitForTheOwner(t, harness)

	harness.flowIs(t, waitFlow+outside+"# edited\n")

	err := harness.client(t, otherBuild).ResumeOnThisBuild(testItem)

	require.ErrorContains(t, err, "changed since the run started")
	require.ErrorContains(t, err, "These scripts are outside the repository and are not pinned: /bin/sh")
}

func TestClient_ResumeOnThisBuildCountsTheCommandsWhoseScriptsAreNotPinned(t *testing.T) {
	t.Parallel()

	const dynamic = "    argv = [\"./s.sh\"]\n    command(run = argv)\n"

	harness := newHarness(t)
	harness.flowIs(t, waitFlow+dynamic)
	engine := harness.engine(t)

	runInBackground(engine, testItem)
	waitForTheOwner(t, harness)
	expireLease(harness)

	harness.flowIs(t, waitFlow+dynamic+"# edited\n")

	err := harness.client(t, otherBuild).ResumeOnThisBuild(testItem)

	require.ErrorContains(t, err, "changed since the run started")
	require.ErrorContains(t, err, "1 command calls have a run list that is not a literal, so their scripts are not pinned")
}

func TestClient_ResumeOnThisBuildReportsACancelThatCannotBeMarked(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	require.NoError(t, os.WriteFile(filepath.Join(harness.opts.StateDir, cancelsDir), nil, ownerMode))

	require.ErrorContains(t, other.ResumeOnThisBuild(testItem), "mark the cancel of run "+testRun)

	row, err := other.row(testRun)
	require.NoError(t, err)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a refused run stays with the build that made it")
}

func TestClient_ResumeOnThisBuildReportsAClaimThatCannotBeTaken(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	harness.life.failing["claim"] = true

	expireLease(harness)

	err := other.ResumeOnThisBuild(testItem)
	require.ErrorContains(t, err, "claim of item "+testItem)
	require.ErrorContains(t, err, "is now canceled", "the cancel went through, so the error says what state the run is in")
	require.ErrorContains(t, err, "obee resume "+testItem+" --on-this-build again", "the error says how to retry")

	row, err := other.row(testRun)
	require.NoError(t, err)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a run that took no claim stays with the build that made it")
}

func TestClient_ReopenHereGivesBackItsClaimWhenTheRewindFails(t *testing.T) {
	t.Parallel()

	_, _, other := strandedHarness(t)
	last, err := other.newest(testItem)
	require.NoError(t, err)

	err = other.reopenHere(testItem, last) // the run is still open, and DBOS rewinds only a run that ended

	require.Error(t, err)

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion))
}
