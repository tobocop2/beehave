package flow

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientEngine_NamesTheModeThePidAndTheStartOfTheEngineThatHoldsTheFolder(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.Mode = ModeServe
	client := harness.client(t, testVersion)

	before := time.Now().UTC().Add(-time.Second)
	engine := harness.engine(t)

	info, running, err := client.Engine()
	require.NoError(t, err)
	require.True(t, running)
	assert.Equal(t, ModeServe, info.Mode)
	assert.Equal(t, os.Getpid(), info.PID)
	assert.True(t, info.Since.After(before), "the start time is the time of this engine")
	assert.Equal(t, time.UTC, info.Since.Location())

	require.NoError(t, engine.Shutdown())

	_, running, err = client.Engine()
	require.NoError(t, err)
	assert.False(t, running, "a stopped engine is not an engine")
}

func TestClientEngine_AnEngineThatSetsNoModeRunsOneItem(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)
	harness.engine(t)

	info, running, err := client.Engine()
	require.NoError(t, err)
	require.True(t, running)
	assert.Equal(t, ModeRun, info.Mode)
}

func TestClientEngine_AFileThatACrashedEngineLeftIsIgnoredWhenNoEngineHoldsTheLock(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)
	require.NoError(t, announceEngine(client.dir, "v-old"))
	require.NoError(t, writeEngineInfo(client.dir, ModeServe, 4242, time.Now().UTC()))

	info, running, err := client.Engine()
	require.NoError(t, err)
	assert.False(t, running)
	assert.Zero(t, info, "the file describes an engine; the lock decides whether one runs")
	assert.NoFileExists(t, filepath.Join(client.dir, engineInfoName), "the check that took the lock removes the stale file")
}

func TestClientEngine_AFileThatCannotBeReadIsReported(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)
	harness.engine(t)
	require.NoError(t, os.WriteFile(filepath.Join(client.dir, engineInfoName), []byte("{"), 0o600))

	_, _, err := client.Engine()
	require.ErrorContains(t, err, engineInfoName)
}

func TestWriteEngineInfo_AStartTimeThatJSONCannotEncodeWritesNoFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tooLate := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)

	err := writeEngineInfo(dir, ModeServe, 1, tooLate)

	require.ErrorContains(t, err, "encode "+engineInfoName)
	assert.NoFileExists(t, filepath.Join(dir, engineInfoName))
}

func TestWriteEngineInfo_AFolderThatCannotBeWrittenIsReported(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing")

	err := writeEngineInfo(missing, ModeServe, 1, time.Now())

	require.ErrorContains(t, err, "write "+engineInfoName)
	assert.NoFileExists(t, filepath.Join(missing, engineInfoName))
}

func TestReadEngineInfo_AnInfoPathThatIsAFolderIsReportedNotTakenForNoEngine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, engineInfoName), 0o700))

	info, err := readEngineInfo(dir)

	require.ErrorContains(t, err, "read "+engineInfoName)
	assert.Zero(t, info)
}

func TestNewEngine_AnInfoFileThatCannotBeWrittenFailsTheOpenAndLeavesTheLockFree(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)
	folder := filepath.Join(client.dir, engineInfoName)
	require.NoError(t, os.MkdirAll(folder, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "x"), nil, 0o600))

	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())

	require.ErrorContains(t, err, "write "+engineInfoName)
	assert.Nil(t, engine)

	held, err := holdEngine(client.dir)
	require.NoError(t, err, "a failed open leaves the engine lock free")
	require.NoError(t, held.Unlock())
}
