package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
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
	require.NoError(t, os.MkdirAll(filepath.Join(root, "p", "1%2E5"), 0o700))

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
	assert.Equal(t, filepath.Join(root, "p", "1%2E5"), inDir)
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
		args   []string
		binary string
		dir    string
		limit  time.Duration
	)

	runs := programs{
		trees: treeAt("/tree"),
		local: func(b, d string, timeout time.Duration) proc.Runner {
			binary, dir, limit = b, d, timeout

			return scripted{result: model.CommandResult{Stdout: "out", ExitCode: 3}, args: &args}
		},
	}

	got, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"make", "check"}, Timeout: time.Minute})
	require.NoError(t, err)
	assert.Equal(t, flow.CommandOutput{Stdout: "out", RC: 3}, got)
	assert.Equal(t, "make", binary)
	assert.Equal(t, "/tree", dir)
	assert.Equal(t, time.Minute, limit)
	assert.Equal(t, []string{"check"}, args)
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

func (f *fakeHost) Submit(_ context.Context, spec model.JobSpec) (model.JobID, error) {
	f.spec = spec

	return "job-1", f.submitErr
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

			runs := programs{hosts: func(string) (host.Host, error) { return test.hosted, nil }}

			got, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Host: "h", Run: []string{"echo", "it's"}})
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, test.want, got)
			assert.Equal(t, `'echo' 'it'\''s'`, test.hosted.spec.Command)
			assert.Equal(t, "obee-1", test.hosted.spec.Name)
		})
	}
}

func TestPrograms_AHostThatCannotBeBuiltIsAnError(t *testing.T) {
	t.Parallel()

	runs := programs{hosts: func(string) (host.Host, error) { return nil, errFake }}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Host: "h", Run: []string{"x"}})
	require.ErrorIs(t, err, errFake)
}

func TestNewPrograms_BuildTheHostOfTheProjectOnlyWhenItHasOne(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Projects: map[string]config.Project{"bare": {}}}
	runs := newPrograms(cfg, treeAt("/tree"), newAdapters(nil, nil))

	_, err := runs.hosts("zzz")
	require.ErrorIs(t, err, errUnknownProject)

	_, err = runs.hosts("bare")
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

	got, err := runs.hosts("h")

	require.NoError(t, err)
	assert.Same(t, hosted, got)
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

	runner, ok := trees.git(t.TempDir()).(proc.ExecRunner)

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
