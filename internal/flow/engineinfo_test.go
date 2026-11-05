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
