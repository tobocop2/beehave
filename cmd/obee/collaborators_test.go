package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/proc/gitexec"
	"github.com/tobocop2/obee/internal/store"
)

var errFake = errors.New("fake failure")

// scripted is a proc.Runner that records its arguments and returns one result.
type scripted struct {
	result model.CommandResult
	args   *[]string
}

func (s scripted) Run(_ context.Context, args []string) model.CommandResult {
	if s.args != nil {
		*s.args = args
	}

	return s.result
}

type treeAt string

func (t treeAt) Path(context.Context, string, string) (string, error) { return string(t), nil }

func (t treeAt) Ensure(context.Context, string, string) (string, error) { return string(t), nil }

type treeFails struct{}

func (treeFails) Path(context.Context, string, string) (string, error) { return "", errFake }

func TestWorktrees_PathIsTheFolderThatGitListsAsAWorktree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "p", "bb-1")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	other := filepath.Join(root, "p", "bb-2")
	require.NoError(t, os.MkdirAll(other, 0o700))

	tests := []struct {
		name    string
		item    string
		result  model.CommandResult
		want    string
		wantErr string
	}{
		{"listed", "bb-1", model.CommandResult{Stdout: "worktree /nowhere\n\nworktree " + dir + "\nHEAD abc\n"}, dir, ""},
		{"not listed", "bb-2", model.CommandResult{Stdout: "worktree " + dir + "\n"}, "", ""},
		{"no folder", "bb-3", model.CommandResult{}, "", ""},
		{"git fails", "bb-1", model.CommandResult{ExitCode: 128, Stderr: "not a repository"}, "", "exited with code 128: not a repository"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var asked *[]string

			trees := worktrees{root: root, git: func(string) proc.Runner { return scripted{result: test.result, args: asked} }}

			got, err := trees.Path(t.Context(), "p", test.item)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestWorktrees_AskGitToListTheWorktreesInTheItemFolder(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "p", "1_2E5"), 0o700))

	var (
		asked []string
		inDir string
	)

	trees := worktrees{root: root, git: func(dir string) proc.Runner {
		inDir = dir

		return scripted{args: &asked}
	}}

	_, err := trees.Path(t.Context(), "p", "1.5")
	require.NoError(t, err)
	assert.Equal(t, []string{"worktree", "list", "--porcelain"}, asked)
	assert.Equal(t, filepath.Join(root, "p", "1_2E5"), inDir)
}

func TestWorktrees_TheFolderOfAnItemHoldsOnlyLettersDigitsAndFewMarks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	item := "review#5 \"x\"/y"

	var inDir string

	folder := filepath.Join(root, "p", model.SafeName(item))
	require.NoError(t, os.MkdirAll(folder, 0o700))

	trees := worktrees{root: root, git: func(dir string) proc.Runner {
		inDir = dir

		return scripted{args: new([]string)}
	}}

	_, err := trees.Path(t.Context(), "p", item)
	require.NoError(t, err)

	rel, err := filepath.Rel(root, inDir)
	require.NoError(t, err)
	assert.Regexp(t, `^[A-Za-z0-9._/-]+$`, rel)
}

func TestWorktrees_AFolderThatCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// A file where the project folder should be makes the item folder unreadable (ENOTDIR).
	require.NoError(t, os.WriteFile(filepath.Join(root, "p"), nil, 0o600))

	_, err := worktrees{root: root}.Path(t.Context(), "p", "1")
	require.ErrorContains(t, err, "read the folder")
}

func TestWorktrees_ALinkThatCannotBeFollowedIsAnError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "p"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "p", "1")))

	// os.Stat follows the link and reports a missing target, which is no worktree.
	got, err := worktrees{root: root}.Path(t.Context(), "p", "1")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestPrograms_ALocalProgramRunsInTheWorktreeAndKeepsItsExitCode(t *testing.T) {
	t.Parallel()

	var (
		args []string
		spec proc.ExecRunner
	)

	runs := programs{
		trees: treeAt("/tree"),
		exec: func(s proc.ExecRunner) proc.Runner {
			spec = s

			return scripted{result: model.CommandResult{Stdout: "out", Stderr: "err", ExitCode: 3}, args: &args}
		},
	}

	request := flow.CommandRequest{Project: "p", Item: "1", Run: []string{"make", "check"}, Timeout: time.Minute, Stdin: "the text", Env: []string{"PATH=/bin"}}

	got, err := runs.Run(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, flow.CommandOutput{Stdout: "out", Stderr: "err", RC: 3}, got)
	assert.Equal(t, proc.ExecRunner{Binary: "make", Dir: "/tree", Timeout: time.Minute, Only: []string{"PATH=/bin"}, Stdin: "the text"}, spec, "the environment of the request replaces the environment of the process")
	assert.Equal(t, []string{"check"}, args)
}

func TestPrograms_ACappedProgramGetsTheCapAndOnlyAForkFailureNamesIt(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		result model.CommandResult
		cap    int
		note   bool
	}{
		"fork failure in stderr":         {model.CommandResult{Stdout: "out", Stderr: "sh: fork: Resource temporarily unavailable", ExitCode: 2}, 5, true},
		"cannot fork in stdout":          {model.CommandResult{Stdout: "out: Cannot fork", ExitCode: 2}, 5, true},
		"a failure with another cause":   {model.CommandResult{Stdout: "out", Stderr: "assertion failed", ExitCode: 1}, 5, false},
		"a fork text but exit code 0":    {model.CommandResult{Stdout: "out", Stderr: "Cannot fork", ExitCode: 0}, 5, false},
		"a fork failure with no cap set": {model.CommandResult{Stdout: "out", Stderr: "Cannot fork", ExitCode: 2}, 0, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var spec proc.ExecRunner

			runs := programs{
				trees: treeAt("/tree"),
				exec: func(s proc.ExecRunner) proc.Runner {
					spec = s

					return scripted{result: tc.result, args: new([]string)}
				},
			}

			got, err := runs.Run(t.Context(), flow.CommandRequest{Run: []string{"probe"}, MaxNewProcesses: tc.cap})
			require.NoError(t, err)
			assert.Equal(t, tc.cap, spec.MaxNewProcesses)
			assert.Equal(t, tc.result.ExitCode, got.RC)
			assert.True(t, strings.HasPrefix(got.Stdout, "out"), "the output of the program stays first")
			assert.Equal(t, tc.note, strings.Contains(got.Stdout, "max_new_processes = 5"))
		})
	}
}

func TestPrograms_ACapThatCannotBeCountedOrSetFailsTheStepWithItsCause(t *testing.T) {
	t.Parallel()

	runs := programs{
		trees: treeAt("/tree"),
		exec: func(proc.ExecRunner) proc.Runner {
			return scripted{result: model.CommandResult{ExitCode: proc.ExitCapFailed, SetupErr: errors.New("could not set max_new_processes = 9: no")}, args: new([]string)}
		},
	}

	got, err := runs.Run(t.Context(), flow.CommandRequest{Run: []string{"probe"}, MaxNewProcesses: 9})

	require.EqualError(t, err, "could not set max_new_processes = 9: no")
	assert.Empty(t, got.Stdout)

	exit125 := programs{
		trees: treeAt("/tree"),
		exec: func(proc.ExecRunner) proc.Runner {
			return scripted{result: model.CommandResult{ExitCode: proc.ExitCapFailed, Stderr: "could not open the thing"}, args: new([]string)}
		},
	}

	got, err = exit125.Run(t.Context(), flow.CommandRequest{Run: []string{"probe"}, MaxNewProcesses: 9})
	require.NoError(t, err, "a command that exits with 125 by itself is not a cap failure")
	assert.Equal(t, proc.ExitCapFailed, got.RC)
}

func TestNewPrograms_ACappedProgramThatExits125WithCouldNotKeepsItsResult(t *testing.T) { //nolint:paralleltest // the cap counts every process of the user
	runs := newPrograms(config.Config{}, treeAt(t.TempDir()), newAdapters(nil, nil))

	got, err := runs.Run(t.Context(), flow.CommandRequest{Run: []string{"sh", "-c", `echo out; echo "could not open the thing" >&2; exit 125`}, MaxNewProcesses: 1000})

	require.NoError(t, err)
	assert.Equal(t, 125, got.RC)
	assert.Contains(t, got.Stdout, "out\n")
}

func TestNewPrograms_ACommandStepGetsOnlyTheEnvironmentOfItsRequestAndItsStdin(t *testing.T) {
	t.Setenv("OBEE_TEST_SECRET", "secret")

	runs := newPrograms(config.Config{}, treeAt(t.TempDir()), newAdapters(nil, nil))

	got, err := runs.Run(t.Context(), flow.CommandRequest{Run: []string{"sh", "-c", "env; cat"}, Stdin: "from stdin\n", Env: []string{"A=1"}})

	require.NoError(t, err)
	assert.Equal(t, 0, got.RC)
	assert.Contains(t, got.Stdout, "A=1\n")
	assert.Contains(t, got.Stdout, "from stdin\n")
	assert.NotContains(t, got.Stdout, "OBEE_TEST_SECRET")
}

func TestPrograms_RefuseAnEmptyProgramAndAFailedTreeLookup(t *testing.T) {
	t.Parallel()

	_, err := programs{}.Run(t.Context(), flow.CommandRequest{})
	require.ErrorIs(t, err, errEmptyCommand)

	_, err = programs{trees: treeFails{}}.Run(t.Context(), flow.CommandRequest{Run: []string{"x"}})
	require.ErrorIs(t, err, errFake)
}

// fakeHost is a host.Host that records the job and ends it in one state.
type fakeHost struct {
	host.Host

	state     model.JobState
	submitErr error
	waitErr   error
	spec      model.JobSpec
}

func (*fakeHost) WorkRoot(context.Context) (string, error) { return "/home/ob", nil }

// Log gives no log: the gate reads the log of every failed job, and this host keeps none.
func (*fakeHost) Log(context.Context, model.JobID) (string, error) { return "", errFake }

func (f *fakeHost) Submit(_ context.Context, spec model.JobSpec) (model.JobID, error) {
	f.spec = spec

	return "job-1", f.submitErr
}

func (*fakeHost) Status(context.Context) (model.HostStatus, error) {
	return model.HostStatus{Disk: model.DiskReading{FreeBytes: 1 << 40, Readable: true}}, nil
}

func (f *fakeHost) Wait(context.Context, model.JobID) (model.JobState, error) {
	return f.state, f.waitErr
}

func TestPrograms_AHostProgramRunsAsOneQuotedJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		hosted  *fakeHost
		want    flow.CommandOutput
		wantErr error
	}{
		{"succeeded", &fakeHost{state: model.JobSucceeded}, flow.CommandOutput{}, nil},
		{"failed", &fakeHost{state: model.JobFailed}, flow.CommandOutput{RC: 1}, nil},
		{"canceled", &fakeHost{state: model.JobCanceled}, flow.CommandOutput{}, errHostJob},
		{"submit fails", &fakeHost{submitErr: errFake}, flow.CommandOutput{}, errFake},
		{"wait fails", &fakeHost{waitErr: errFake}, flow.CommandOutput{}, errFake},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runs := programs{hosts: func(string, string) (host.Host, error) { return test.hosted, nil }}

			got, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Host: "h", Run: []string{"echo", "it's"}})
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, test.want, got)
			assert.Equal(t, `echo 'it'\''s'`, test.hosted.spec.Command)
			assert.Equal(t, "obee-1", test.hosted.spec.Name)
		})
	}
}

func TestHostOutput_ADiedOrStaleJobSaysWhatAPersonCanDo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state model.JobState
		want  string
	}{
		{model.JobDied, "scheduler holds no record"},
		{model.JobStale, "host.stale_minutes"},
		{model.JobTimedOut, "host.max_run"},
		{model.JobCanceled, "the state is canceled"},
	}

	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			t.Parallel()

			_, err := hostOutput(test.state)
			require.ErrorIs(t, err, errHostJob)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestPrograms_AHostThatCannotBeBuiltIsAnError(t *testing.T) {
	t.Parallel()

	runs := programs{hosts: func(string, string) (host.Host, error) { return nil, errFake }}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Host: "h", Run: []string{"x"}})
	require.ErrorIs(t, err, errFake)
}

func TestNewPrograms_BuildTheHostOfTheProjectOnlyWhenItHasOne(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Projects: map[string]config.Project{"bare": {}}}
	runs := newPrograms(cfg, treeAt("/tree"), newAdapters(nil, nil))

	_, err := runs.hosts("zzz", "")
	require.ErrorIs(t, err, errUnknownProject)

	_, err = runs.hosts("bare", "")
	require.ErrorIs(t, err, errNoHost)

	runner := runs.local("sh", t.TempDir(), time.Second)
	assert.Equal(t, 0, runner.Run(t.Context(), []string{"-c", "exit 0"}).ExitCode)
}

func TestNewPrograms_ReturnTheHostThatTheTableBuildsForTheProject(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{}
	table := newAdapters(nil, nil)
	table.hosts = map[model.HostProvider]func(model.Project) (host.Host, error){
		model.HostSkyPilot: func(model.Project) (host.Host, error) { return hosted, nil },
	}

	project := config.Project{Project: model.Project{Name: "h", Host: &model.HostConfig{Provider: model.HostSkyPilot}}}
	runs := newPrograms(config.Config{Projects: map[string]config.Project{"h": project}}, treeAt("/tree"), table)

	got, err := runs.hosts("h", "")

	require.NoError(t, err)

	_, err = got.Submit(t.Context(), model.JobSpec{Name: "j"})
	require.NoError(t, err)
	assert.Equal(t, "j", hosted.spec.Name)
}

func TestNewPrograms_BuildTheHostOfTheClusterThatTheRunChose(t *testing.T) {
	t.Parallel()

	var built []string

	table := newAdapters(nil, nil)
	table.hosts = map[model.HostProvider]func(model.Project) (host.Host, error){
		model.HostSkyPilot: func(project model.Project) (host.Host, error) {
			built = append(built, project.BuildHost)

			return &fakeHost{}, nil
		},
	}

	project := config.Project{Project: model.Project{Name: "h", BuildHost: "gpu", BuildHosts: []string{"gpu", "gpu-b"}, Host: &model.HostConfig{Provider: model.HostSkyPilot}}}
	runs := newPrograms(config.Config{Projects: map[string]config.Project{"h": project}}, treeAt("/tree"), table)

	_, err := runs.hosts("h", "gpu-b")
	require.NoError(t, err)

	_, err = runs.hosts("h", "")
	require.NoError(t, err)

	assert.Equal(t, []string{"gpu-b", "gpu"}, built, "the adapter builds the cluster that was chosen; no choice builds the build host")

	_, err = runs.hosts("h", "rogue")
	require.ErrorIs(t, err, model.ErrUndeclaredHost, "the host table refuses a cluster that the project does not declare")

	_, err = runs.declared("zzz", "")
	require.ErrorIs(t, err, errUnknownProject)

	chosen, err := runs.declared("h", "gpu-b")
	require.NoError(t, err)
	assert.Equal(t, "gpu-b", chosen)
}

func TestNewWorktrees_RunGitInTheFolderAndKeepTheTreesUnderTheStateFolder(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	trees := newWorktrees(nil, flow.Options{StateDir: state}, nil)

	assert.Equal(t, filepath.Join(state, "trees"), trees.root)
	assert.Equal(t, filepath.Join(state, "repos"), trees.repos)
	assert.Equal(t, 0, trees.git(t.TempDir()).Run(t.Context(), []string{"--version"}).ExitCode)
}

func TestNewWorktrees_EveryGitCallRefusesToPromptForCredentials(t *testing.T) {
	t.Parallel()

	trees := newWorktrees(nil, flow.Options{StateDir: t.TempDir()}, nil)

	runner, ok := trees.git(t.TempDir()).(gitexec.Runner)

	require.True(t, ok)
	assert.Contains(t, runner.Env, "GIT_TERMINAL_PROMPT=0")
}

// fakeTracker is a store.Tracker that returns fixed fields for an item.
type fakeTracker struct {
	store.Tracker
}

func (fakeTracker) Item(_ context.Context, id string) (map[string]string, error) {
	return map[string]string{"id": id}, nil
}

func TestTrackerItems_ReadTheFieldsFromTheTrackerOfTheProject(t *testing.T) {
	t.Parallel()

	items := trackerItems{
		projects: map[string]config.Project{"p": {}},
		tracker: func(context.Context, model.Project) (store.Tracker, error) {
			return fakeTracker{}, nil
		},
	}

	got, err := items.Fields(t.Context(), "p", "bb-1")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"id": "bb-1"}, got)

	_, err = items.Fields(t.Context(), "zzz", "bb-1")
	require.ErrorIs(t, err, errUnknownProject)

	items.tracker = func(context.Context, model.Project) (store.Tracker, error) { return nil, errFake }
	_, err = items.Fields(t.Context(), "p", "bb-1")
	require.ErrorIs(t, err, errFake)
}

type closingTracker struct {
	fakeTracker

	closed int
	err    error
}

func (c *closingTracker) Close() error {
	c.closed++

	return c.err
}

func TestTrackerItems_ClosesTheTrackerAfterEveryReadAndReportsAFailedClose(t *testing.T) {
	t.Parallel()

	closing := &closingTracker{}
	items := trackerItems{
		projects: map[string]config.Project{"p": {}},
		tracker:  func(context.Context, model.Project) (store.Tracker, error) { return closing, nil },
	}

	_, err := items.Fields(t.Context(), "p", "bb-1")
	require.NoError(t, err)
	assert.Equal(t, 1, closing.closed)

	closing.err = errFake
	_, err = items.Fields(t.Context(), "p", "bb-1")
	require.ErrorIs(t, err, errFake)
}

func TestTrackerItems_ALocalProjectRefusesAnItemThatWasNeverAdded(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, stateAt(t.TempDir()))
	items := trackerItems{projects: map[string]config.Project{"p": {Project: localProjectNamed("p")}}, tracker: table.tracker}

	_, err := items.Fields(t.Context(), "p", "fix-login")

	require.EqualError(t, err, "no item fix-login in project p. Add it: obee item add")
}
