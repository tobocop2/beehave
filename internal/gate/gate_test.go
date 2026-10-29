package gate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
)

const (
	headA = "a1b2c3"
	headB = "d4e5f6"
)

type fakeShell struct {
	output string
	code   int
	dir    string
	ran    string
	onRun  func()
}

func (f *fakeShell) run(_ context.Context, dir, command string) (string, int) {
	f.dir, f.ran = dir, command
	if f.onRun != nil {
		f.onRun()
	}

	return f.output, f.code
}

type fakeTree struct {
	states []gate.State
	err    error
	reads  int
}

func (f *fakeTree) read(_ context.Context, _ string) (gate.State, error) {
	f.reads++

	if f.err != nil {
		return gate.State{}, f.err
	}

	return f.states[min(f.reads, len(f.states))-1], nil
}

type fakeRemote struct {
	state     model.JobState
	submitErr error
	waitErr   error
	submitted []model.JobSpec
	waited    []model.JobID
}

func (f *fakeRemote) Submit(_ context.Context, spec model.JobSpec) (model.JobID, error) {
	f.submitted = append(f.submitted, spec)

	return "job-7", f.submitErr
}

func (f *fakeRemote) Wait(_ context.Context, id model.JobID) (model.JobState, error) {
	f.waited = append(f.waited, id)

	return f.state, f.waitErr
}

func project(buildHost string) model.Project {
	return model.Project{Name: "demo", Gate: "make check", BuildHost: buildHost}
}

const cloneURL = "https://github.com/owner/name.git"

func hostRequest(project model.Project) gate.Request {
	return gate.Request{Project: project, Dir: "/w", CloneURL: cloneURL}
}

func clean(head string) gate.State { return gate.State{Head: head, Clean: true} }

func build(shell *fakeShell, tree *fakeTree, remote *fakeRemote) gate.Gate {
	return gate.New(shell.run, tree.read, func(model.Project) (gate.Remote, error) { return remote, nil })
}

func TestRun_ALocalGateRunsTheProjectGateInTheTreeAndPassesOnExitZero(t *testing.T) {
	t.Parallel()

	shell := &fakeShell{output: "all green\n"}
	g := build(shell, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{})

	got, err := g.Run(context.Background(), gate.Request{Project: project("local"), Dir: "/work/tree"})
	require.NoError(t, err)
	assert.Equal(t, gate.Result{Passed: true, Report: "all green\n", Head: headA}, got)
	assert.Equal(t, "/work/tree", shell.dir)
	assert.Equal(t, "make check", shell.ran)
}

func TestRun_ALocalGateThatExitsNonZeroFailsWithItsOutput(t *testing.T) {
	t.Parallel()

	for _, code := range []int{1, 2, 7, 128 + 15} {
		shell := &fakeShell{output: "lint: 3 problems\n", code: code}
		g := build(shell, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{})

		got, err := g.Run(context.Background(), gate.Request{Project: project("local"), Dir: "/w"})
		require.NoError(t, err)
		assert.Equal(t, gate.Result{Report: "lint: 3 problems\n", Head: headA}, got, "exit %d", code)
	}
}

func TestRun_TheReportKeepsTheTailOfALongOutput(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 40000) + "THE-END"
	g := build(&fakeShell{output: long, code: 1}, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{})

	got, err := g.Run(context.Background(), gate.Request{Project: project("local"), Dir: "/w"})
	require.NoError(t, err)
	assert.Len(t, got.Report, 16384)
	assert.True(t, strings.HasSuffix(got.Report, "THE-END"))
}

func TestRun_ATreeWithUncommittedChangesIsAnErrorAndTheGateDoesNotRun(t *testing.T) {
	t.Parallel()

	shell := &fakeShell{}
	g := build(shell, &fakeTree{states: []gate.State{{Head: headA}}}, &fakeRemote{})

	_, err := g.Run(context.Background(), gate.Request{Project: project("local"), Dir: "/w"})
	require.ErrorIs(t, err, gate.ErrTreeChanged)
	assert.Contains(t, err.Error(), "has changes that are not committed. Commit or discard them")
	assert.Empty(t, shell.ran)
}

func TestRun_AHeadOrTreeThatChangesDuringTheGateIsAnError(t *testing.T) {
	t.Parallel()

	cases := map[string][]gate.State{
		"head_moved":   {clean(headA), clean("d4e5f6")},
		"tree_changed": {clean(headA), {Head: headA}},
	}

	for name, states := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := build(&fakeShell{}, &fakeTree{states: states}, &fakeRemote{})

			_, err := g.Run(context.Background(), gate.Request{Project: project("local"), Dir: "/w"})
			require.ErrorIs(t, err, gate.ErrTreeChanged)
			assert.Contains(t, err.Error(), "changed while the gate ran")
		})
	}
}

func TestRun_ATreeThatCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	failure := errors.New("not a repository")
	shell := &fakeShell{}
	g := build(shell, &fakeTree{err: failure}, &fakeRemote{})

	_, err := g.Run(context.Background(), gate.Request{Project: project("local"), Dir: "/w"})
	require.ErrorIs(t, err, failure)
	assert.Empty(t, shell.ran)

	// The second read fails: the first one passed.
	after := &readTwice{failure: failure}
	g = gate.New(shell.run, after.read, nil)

	_, err = g.Run(context.Background(), gate.Request{Project: project("local"), Dir: "/w"})
	require.ErrorIs(t, err, failure)
}

type readTwice struct {
	failure error
	reads   int
}

func (r *readTwice) read(context.Context, string) (gate.State, error) {
	r.reads++
	if r.reads > 1 {
		return gate.State{}, r.failure
	}

	return clean(headA), nil
}

func TestRun_AProjectOnAClusterSendsTheGateToTheHost(t *testing.T) {
	t.Parallel()

	shell := &fakeShell{}
	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(shell, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	var announced model.JobID

	request := hostRequest(project("gpu-a"))
	request.Submitted = func(id model.JobID) { announced = id }

	got, err := g.Run(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, gate.Result{Passed: true, Head: headA}, got)
	require.Len(t, remote.submitted, 1)
	assert.Equal(t, "gate-demo", remote.submitted[0].Name)
	assert.Contains(t, remote.submitted[0].Command, "make check")
	assert.Equal(t, []model.JobID{"job-7"}, remote.waited)
	assert.Equal(t, model.JobID("job-7"), announced)
	assert.Empty(t, shell.ran)
}

func TestRun_AFrozenHostJobIsReadAgainByItsIDAndNotSubmittedTwice(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobFailed}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	request := hostRequest(project("gpu-a"))
	request.Resume, request.ResumeHead = "job-3", headB

	got, err := g.Run(context.Background(), request)
	require.NoError(t, err)
	assert.False(t, got.Passed)
	assert.Contains(t, got.Report, "failed on gpu-a")
	assert.Empty(t, remote.submitted)
	assert.Equal(t, []model.JobID{"job-3"}, remote.waited)
}

func TestRun_AResumedHostGateJudgesTheHeadItWasSubmittedWithNotTheHeadOfTheTree(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)
	request := hostRequest(project("gpu-a"))
	request.Resume, request.ResumeHead = "job-3", headB

	got, err := g.Run(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, gate.Result{Passed: true, Head: headB}, got)
}

func TestRun_TheResultHoldsTheTreeOfTheGatedHeadAndNoTreeForAHeadItDidNotRead(t *testing.T) {
	t.Parallel()

	local := build(&fakeShell{}, &fakeTree{states: []gate.State{{Head: headA, Tree: "tree-a", Clean: true}}}, &fakeRemote{})
	got, err := local.Run(context.Background(), gate.Request{Project: project("local"), Dir: "/work/tree"})
	require.NoError(t, err)
	assert.Equal(t, gate.Result{Passed: true, Head: headA, Tree: "tree-a"}, got)

	remote := &fakeRemote{state: model.JobSucceeded}
	resumed := build(&fakeShell{}, &fakeTree{states: []gate.State{{Head: headA, Tree: "tree-a", Clean: true}}}, remote)
	request := hostRequest(project("gpu-a"))
	request.Resume, request.ResumeHead = "job-3", headB

	got, err = resumed.Run(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, gate.Result{Passed: true, Head: headB}, got, "the tree of another head is not the tree that the job judged")
}

func TestRun_AResumeWithoutTheHeadItWasSubmittedWithIsRefusedAndReadsNoJob(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)
	request := hostRequest(project("gpu-a"))
	request.Resume = "job-3"

	_, err := g.Run(context.Background(), request)
	require.ErrorIs(t, err, gate.ErrNoHead)
	assert.Empty(t, remote.waited)
	assert.Empty(t, remote.submitted)
}

func TestRun_AHostGateWithNoCloneURLIsRefusedAndSubmitsNothing(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), gate.Request{Project: project("gpu-a"), Dir: "/w"})
	require.ErrorIs(t, err, gate.ErrNoCloneURL)
	assert.Empty(t, remote.submitted)
}

func TestRun_TheHostCloneUsesTheURLTheCallerGaveNotAGitHubOne(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)
	request := hostRequest(project("gpu-a"))
	request.CloneURL = "ssh://git@forge.example/team/demo.git"

	_, err := g.Run(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.Contains(t, remote.submitted[0].Command, "git clone -- ssh://git@forge.example/team/demo.git tree")
	assert.NotContains(t, remote.submitted[0].Command, "github.com")
}

func TestRun_AHostJobThatDidNotPassAndDidNotFailIsAnErrorNeverAPass(t *testing.T) {
	t.Parallel()

	for _, state := range []model.JobState{model.JobUnknown, model.JobCanceled, model.JobRunning, model.JobPending} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()

			g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{state: state})

			_, err := g.Run(context.Background(), hostRequest(project("gpu-a")))
			require.ErrorIs(t, err, gate.ErrNoVerdict)
			assert.Contains(t, err.Error(), string(state))
		})
	}
}

func TestRun_AHostFailureIsReturnedWithTheStep(t *testing.T) {
	t.Parallel()

	failure := errors.New("host down")
	cases := map[string]*fakeRemote{
		"submit": {submitErr: failure},
		"wait":   {waitErr: failure, state: model.JobSucceeded},
	}

	for name, remote := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

			_, err := g.Run(context.Background(), hostRequest(project("gpu-a")))
			require.ErrorIs(t, err, failure)
		})
	}
}

func TestRun_AClusterWithNoHostIsAnErrorThatNamesTheFix(t *testing.T) {
	t.Parallel()

	failure := errors.New("no host block")
	g := gate.New(nil, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return nil, failure })

	_, err := g.Run(context.Background(), gate.Request{Project: project("gpu-a"), Dir: "/w"})
	require.ErrorIs(t, err, failure)
	assert.Contains(t, err.Error(), "gpu-a")
}
