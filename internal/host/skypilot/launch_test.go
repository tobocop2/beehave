package skypilot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

const runLimitWord = "gcp.managed_instance_group.run_duration=43200"

// upCall runs Up on a host with the placement and returns the words of the sky launch call.
func upCall(t *testing.T, cfg model.HostConfig, options ...Option) []string {
	t.Helper()

	h, sky, ssh := newOptionHost(t, cfg, options...)
	sky.Plan("", "launch")
	ssh.Plan("flock from util-linux 2.39.3\n", "flock", "--version")

	require.NoError(t, h.Up(context.Background()))
	require.Len(t, sky.Calls, 1)

	return sky.Calls[0]
}

// newOptionHost is newHost with options of New.
func newOptionHost(t *testing.T, cfg model.HostConfig, options ...Option) (*Host, *proctest.Scripted, *proctest.Scripted) {
	t.Helper()

	sky, ssh := &proctest.Scripted{}, &proctest.Scripted{}

	h, err := New(clusterName, cfg, sky, ssh, func(*link.Link, model.HostConfig, Slot) Queue { return &fakeQueue{} }, options...)
	require.NoError(t, err)

	return h, sky, ssh
}

func TestUp_ASpotHostLaunchesWithUseSpotAndLeavesOutTheRunLimitKey(t *testing.T) {
	t.Parallel()

	got := upCall(t, hostConfig(&placed{cloud: "gcp", region: "us-central1", instanceType: "n2-standard-8", spot: flag(true)}))

	assert.Equal(t, []string{"launch", "-y", "-c", "lane-host", "--down", "-i", "45", "--infra", "gcp/us-central1", "-t", "n2-standard-8", "--use-spot"}, got)
	assert.NotContains(t, strings.Join(got, " "), "run_duration", "SkyPilot refuses a spot instance in the group that the run limit makes")
}

func TestUp_ASpotKeyOfFalseLaunchesAsTheHostDoesWithNoSpotKey(t *testing.T) {
	t.Parallel()

	off := upCall(t, hostConfig(&placed{cloud: "gcp", region: "us-central1", instanceType: "n2-standard-8", spot: flag(false)}))
	none := upCall(t, hostConfig(placement()))

	assert.Equal(t, none, off)
	assert.Contains(t, off, runLimitWord)
	assert.NotContains(t, off, "--use-spot")
}

func TestUp_ADiskSizeGoesToSkyLaunchAndKeepsTheRunLimitKey(t *testing.T) {
	t.Parallel()

	got := upCall(t, hostConfig(&placed{cloud: "gcp", region: "us-central1", instanceType: "n2-standard-8", diskSizeGB: 100}))

	assert.Equal(t, []string{
		"launch", "-y", "-c", "lane-host", "--down", "-i", "45", "--config", runLimitWord,
		"--infra", "gcp/us-central1", "-t", "n2-standard-8", "--disk-size", "100",
	}, got)
}

func TestUp_ALaunchWithNoNewKeyIsTheLaunchOfTheHostWithoutThem(t *testing.T) {
	t.Parallel()

	got := upCall(t, hostConfig(placement()))

	assert.Equal(t, []string{
		"launch", "-y", "-c", "lane-host", "--down", "-i", "45", "--config", runLimitWord,
		"--infra", "gcp/us-central1", "-t", "n2-standard-8",
	}, got)
	assert.NotContains(t, got, "--wait-for", "the default wait mode counts jobs")
}

// taskReader is a sky command line that reads the task file that a launch names, while the call runs.
type taskReader struct {
	*proctest.Scripted
	path, text string
}

func (r *taskReader) Run(ctx context.Context, args []string) model.CommandResult {
	r.path = args[len(args)-1]

	raw, err := os.ReadFile(r.path)
	if err == nil {
		r.text = string(raw)
	}

	return r.Scripted.Run(ctx, args)
}

func TestUp_ASetupCommandGoesToATaskFileThatClonesTheProjectAndRunsTheCommandAndIsGoneAfterTheLaunch(t *testing.T) {
	t.Parallel()

	cfg := hostConfig(&placed{cloud: "gcp", region: "us-central1", instanceType: "n2-standard-8", spot: flag(true), diskSizeGB: 100})
	cfg.SetupCommand = "make setup"
	sky := &taskReader{Scripted: &proctest.Scripted{}}
	sky.Plan("", "launch")

	ssh := &proctest.Scripted{}
	ssh.Plan("flock from util-linux 2.39.3\n", "flock", "--version")

	h, err := New(clusterName, cfg, sky, ssh, func(*link.Link, model.HostConfig, Slot) Queue { return &fakeQueue{} },
		WithCheckout("beehave", "main", "https://example.test/o/n.git"))
	require.NoError(t, err)
	require.NoError(t, h.Up(context.Background()))

	require.Len(t, sky.Calls, 1)
	assert.Equal(t, []string{
		"launch", "-y", "-c", "lane-host", "--down", "-i", "45", "--infra", "gcp/us-central1", "-t", "n2-standard-8", "--use-spot", "--disk-size", "100", sky.path,
	}, sky.Calls[0])
	assert.True(t, strings.HasSuffix(sky.path, ".yaml"), sky.path)
	assert.Equal(t, `setup: "set -e\nmkdir -p \"$HOME/beehave-setup\"\ncd \"$HOME/beehave-setup\"\ngit clone --branch main https://example.test/o/n.git beehave\ncd beehave\nmake setup"`+"\n", sky.text)

	_, err = os.Stat(sky.path)
	assert.ErrorIs(t, err, os.ErrNotExist, "the task file is deleted after the launch")
}

func TestUp_ASetupCommandWithAFailedLaunchStillDeletesTheTaskFile(t *testing.T) {
	t.Parallel()

	cfg := hostConfig(nil)
	cfg.SetupCommand = "make setup"
	sky := &taskReader{Scripted: &proctest.Scripted{}}
	sky.PlanFail("boom", "launch")

	h, err := New(clusterName, cfg, sky, &proctest.Scripted{}, func(*link.Link, model.HostConfig, Slot) Queue { return &fakeQueue{} },
		WithCheckout("beehave", "main", "https://example.test/o/n.git"))
	require.NoError(t, err)
	require.ErrorIs(t, h.Up(context.Background()), link.ErrCommand)

	_, err = os.Stat(sky.path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestUp_ATaskFileThatCannotBeWrittenFailsTheLaunchBeforeSkyRunsAndLeavesNoFolderBehind(t *testing.T) {
	t.Parallel()

	cfg := hostConfig(nil)
	cfg.SetupCommand = "make setup"
	h, sky, _ := newOptionHost(t, cfg, WithCheckout("beehave", "main", "https://example.test/o/n.git"))

	var written string

	disk := errors.New("disk full")
	h.write = func(path string, _ []byte, _ os.FileMode) error {
		written = path

		return disk
	}

	require.ErrorIs(t, h.Up(context.Background()), disk)
	assert.Empty(t, sky.Calls, "sky is not run with no task file")

	_, err := os.Stat(filepath.Dir(written))
	assert.ErrorIs(t, err, os.ErrNotExist, "the folder of the task file is deleted")
}

func TestUp_AFolderForTheTaskFileThatCannotBeMadeFailsTheLaunchBeforeSkyRuns(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	cfg := hostConfig(nil)
	cfg.SetupCommand = "make setup"
	h, sky, _ := newOptionHost(t, cfg, WithCheckout("beehave", "main", "https://example.test/o/n.git"))

	require.ErrorContains(t, h.Up(context.Background()), "write the SkyPilot task file")
	assert.Empty(t, sky.Calls)
}

func TestUp_ALaunchWithNoSetupCommandWritesNoTaskFile(t *testing.T) {
	t.Parallel()

	got := upCall(t, hostConfig(nil), WithCheckout("beehave", "main", "https://example.test/o/n.git"))

	assert.False(t, slices.ContainsFunc(got, func(word string) bool { return strings.HasSuffix(word, ".yaml") }), "%v", got)
}

func TestNew_ASetupCommandNeedsACheckoutToCloneFrom(t *testing.T) {
	t.Parallel()

	cfg := hostConfig(nil)
	cfg.SetupCommand = "make setup"

	_, err := New(clusterName, cfg, nil, nil, nil)

	require.ErrorIs(t, err, ErrConfig)
	assert.ErrorContains(t, err, "`host.setup_command` needs the clone URL and the base branch of the project")
}
