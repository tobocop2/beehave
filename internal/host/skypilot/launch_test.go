package skypilot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	mode       os.FileMode
}

func (r *taskReader) Run(ctx context.Context, args []string) model.CommandResult {
	r.path = args[len(args)-1]

	raw, err := os.ReadFile(r.path)
	if err == nil {
		r.text = string(raw)
	}

	if info, statErr := os.Stat(r.path); statErr == nil {
		r.mode = info.Mode().Perm()
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
	require.ErrorIs(t, err, os.ErrNotExist, "the task file is deleted after the launch")

	_, err = os.Stat(filepath.Dir(sky.path))
	require.ErrorIs(t, err, os.ErrNotExist, "the folder of the task file is deleted after the launch")
}

func TestUp_TheTaskFileIsReadableByTheOwnerOnly(t *testing.T) {
	t.Parallel()

	sky := launchWithSetup(t, "make setup", "beehave", "main", "https://example.test/o/n.git")

	assert.Equal(t, os.FileMode(0o600), sky.mode, "a set-up command can hold a secret")
}

// launchWithSetup runs Up on a host that has the command and the checkout and returns the reader that saw the task file.
func launchWithSetup(t *testing.T, command string, project, branch, url string) *taskReader {
	t.Helper()

	cfg := hostConfig(nil)
	cfg.SetupCommand = command
	sky := &taskReader{Scripted: &proctest.Scripted{}}
	sky.Plan("", "launch")

	ssh := &proctest.Scripted{}
	ssh.Plan("flock from util-linux 2.39.3\n", "flock", "--version")

	h, err := New(clusterName, cfg, sky, ssh, func(*link.Link, model.HostConfig, Slot) Queue { return &fakeQueue{} },
		WithCheckout(project, branch, url))
	require.NoError(t, err)
	require.NoError(t, h.Up(context.Background()))

	return sky
}

// setupText returns the task file text that a launch with the command and the checkout reads.
func setupText(t *testing.T, command string, project, branch, url string) string {
	t.Helper()

	return launchWithSetup(t, command, project, branch, url).text
}

func TestUp_AnyUserSetupCommandReachesTheTaskFileAsOneYAMLScalarThatReadsBackToTheScript(t *testing.T) {
	t.Parallel()

	for _, command := range []string{
		`echo "a b" && echo 'c'`,
		"echo $HOME $(date) `id`",
		"pip install -r req.txt\nmake setup\n",
		"cat <<EOF\n${VAR} {{ x }}\tend\nEOF",
		"echo \\ done # not: a comment\n---",
	} {
		text := setupText(t, command, "beehave", "main", "https://example.test/o/n.git")

		require.True(t, strings.HasPrefix(text, "setup: \""), command)
		require.True(t, strings.HasSuffix(text, "\"\n"), command)

		script, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(text, "setup: "), "\n"))
		require.NoError(t, err, command)
		assert.True(t, strings.HasSuffix(script, "\ncd beehave\n"+command), "the command is the last lines of the script, unchanged: %q", script)
	}
}

func TestUp_TheCloneURLTheBranchAndTheProjectAreQuotedForTheShell(t *testing.T) {
	t.Parallel()

	text := setupText(t, "make setup", "my proj'x$y", "feat/a b'c$d", "https://example.test/o/a b'c$d.git")

	script, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(text, "setup: "), "\n"))
	require.NoError(t, err)

	assert.Contains(t, script, `git clone --branch 'feat/a b'\''c$d' 'https://example.test/o/a b'\''c$d.git' 'my proj'\''x$y'`)
	assert.Contains(t, script, `cd 'my proj'\''x$y'`)
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
	assert.ErrorContains(t, err, "`host.setup_command` needs the project name, the base branch and the clone URL")
}

func TestNew_ACheckoutWithOneEmptyFieldIsRefusedAndTheMessageNamesTheFix(t *testing.T) {
	t.Parallel()

	for name, checkout := range map[string][3]string{
		"project": {"", "main", "https://example.test/o/n.git"},
		"branch":  {"beehave", "", "https://example.test/o/n.git"},
		"url":     {"beehave", "main", ""},
	} {
		cfg := hostConfig(nil)
		cfg.SetupCommand = "make setup"

		_, err := New(clusterName, cfg, nil, nil, nil, WithCheckout(checkout[0], checkout[1], checkout[2]))

		require.ErrorIs(t, err, ErrConfig, name)
		assert.ErrorContains(t, err, "give all three, or remove the key", name)
	}
}
