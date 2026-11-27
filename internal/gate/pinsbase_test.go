package gate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/gate"
	"github.com/tobocop2/beehest/internal/model"
)

// Two commits that the engine could read for a base branch, and one that holds no pin file.
const (
	shaOld  = "1111111111111111111111111111111111111111"
	shaNew  = "2222222222222222222222222222222222222222"
	shaNone = "3333333333333333333333333333333333333333"
)

// shaShell answers `git show <sha>:<file>` with the pin text of that commit, and records each git show command. A commit that it does not hold gives exit code 128.
type shaShell struct {
	pins     map[string]string
	commands []string
}

func (r *shaShell) run(_ context.Context, _, command string) (string, int) {
	rest, found := strings.CutPrefix(command, "git show ")
	if !found {
		return "", 0
	}

	r.commands = append(r.commands, command)

	sha, _, _ := strings.Cut(rest, ":")
	if text, ok := r.pins[sha]; ok {
		return text, 0
	}

	return "fatal: invalid object name\n", 128
}

// twoCommits is a base that pinned one version at an older commit and another at a newer one.
func twoCommits() *shaShell {
	return &shaShell{pins: map[string]string{shaOld: "gofake 1.27.3\n", shaNew: "gofake 1.28.0\n"}}
}

func keyAt(t *testing.T, shell *shaShell, sha string) gate.Key {
	t.Helper()

	p := pinnedHostProject()
	p.BuildHost = "cluster"

	g := gate.New(shell.run, (&fakeTree{states: []gate.State{clean(headA)}}).read, nil)
	key, err := g.Key(context.Background(), gate.Request{Project: p, Dir: "/w", BaseSHA: sha})
	require.NoError(t, err)

	return key
}

func TestKey_TheGateReadsThePinFileAtTheBaseCommitByItsSHAAndTheKeyChangesWithIt(t *testing.T) {
	t.Parallel()

	shell := twoCommits()

	atOld := keyAt(t, shell, shaOld)
	atNew := keyAt(t, shell, shaNew)

	require.NotEmpty(t, atOld.ID)
	require.NotEmpty(t, atNew.ID)
	assert.NotEqual(t, atOld.ID, atNew.ID)
	assert.Equal(t, []string{"git show " + shaOld + ":.tool-versions", "git show " + shaNew + ":.tool-versions"}, shell.commands, "a SHA, never origin/<branch>")
}

func TestKey_TheBaseCommitIsPartOfTheKeyEvenWhenBothCommitsHoldTheSamePinFile(t *testing.T) {
	t.Parallel()

	same := &shaShell{pins: map[string]string{shaOld: "gofake 1.27.3\n", shaNew: "gofake 1.27.3\n"}}

	assert.NotEqual(t, keyAt(t, same, shaOld).ID, keyAt(t, same, shaNew).ID)
}

func TestKey_APinFileThatTheBaseCommitLacksGivesNoKey(t *testing.T) {
	t.Parallel()

	assert.Empty(t, keyAt(t, twoCommits(), shaNone).ID)
}

func TestKey_AGateWithNoBaseCommitGivesNoKeyAndReadsNothing(t *testing.T) {
	t.Parallel()

	shell := twoCommits()

	assert.Empty(t, keyAt(t, shell, "").ID)
	assert.Empty(t, shell.commands, "an empty SHA must not turn into a read of a branch")
}

func TestKey_EveryReadOfThePinFileOfALocalGateIsAtTheBaseCommit(t *testing.T) {
	t.Parallel()

	shell := twoCommits()
	p := pinnedHostProject()
	p.BuildHost = "local"

	_, err := gate.New(shell.run, (&fakeTree{states: []gate.State{clean(headA)}}).read, nil).Key(context.Background(), gate.Request{Project: p, Dir: "/w", BaseSHA: shaNew})

	require.NoError(t, err)
	assert.Equal(t, []string{"git show " + shaNew + ":.tool-versions", "git show " + shaNew + ":.tool-versions"}, shell.commands, "the key reads the text, and the installed-tool check reads the pinned tools")
}

func submitAt(t *testing.T, sha, base string) (gate.Job, *fakeRemote, error) {
	t.Helper()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := gate.New(twoCommits().run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	job, err := g.Submit(context.Background(), gate.Request{Project: pinnedHostProject(), Dir: "/w", CloneURL: cloneURL, BaseSHA: sha, Base: base})

	return job, remote, err
}

func TestSubmit_ThePlanPlansThePinnedToolsOfTheBaseCommitAndRecordsItsSHA(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct{ sha, version string }{"an older commit": {shaOld, "1.27.3"}, "a newer commit": {shaNew, "1.28.0"}} {
		job, _, err := submitAt(t, c.sha, "")

		require.NoError(t, err, name)
		require.NotNil(t, job.Plan.Pins, name)
		assert.Equal(t, []gate.Pin{{Tool: "gofake", Version: c.version}}, job.Plan.Pins.Tools, name)
		assert.Equal(t, c.sha, job.Plan.BaseSHA, name)
	}
}

func TestSubmit_TheProbeSentToTheBuildHostHoldsTheVersionOfTheBaseCommit(t *testing.T) {
	t.Parallel()

	_, remote, err := submitAt(t, shaNew, "")

	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.Contains(t, remote.submitted[0].Command, "1.28.0")
	assert.NotContains(t, remote.submitted[0].Command, "1.27.3")
}

func TestSubmit_TheJobOfACandidateHeadFindsTheMergeBaseAtTheSHAAndNeverAtTheRemoteBranch(t *testing.T) {
	t.Parallel()

	_, remote, err := submitAt(t, shaNew, "main")

	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.Contains(t, remote.submitted[0].Command, "git merge-base HEAD "+shaNew)
	assert.NotContains(t, remote.submitted[0].Command, "origin/main")
}

func TestSubmit_AGateWithAPinBaseAndNoBaseRunsNoCandidateCheck(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := gate.New(twoCommits().run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	job, err := g.Submit(context.Background(), gate.Request{Project: pinnedHostProject(), Dir: "/w", CloneURL: cloneURL, PinBase: "integration/s1", BaseSHA: shaNew})

	require.NoError(t, err)
	assert.Empty(t, job.Plan.Base, "a pin base is no base: the plan holds none")
	assert.Equal(t, shaNew, job.Plan.BaseSHA)
	require.Len(t, remote.submitted, 1)
	assert.NotContains(t, remote.submitted[0].Command, "merge-base", "no candidate check")
}

func TestSubmit_ACandidateHeadWithNoBaseCommitStartsNoJobAndNamesTheBase(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}

	_, err := hostGateOn(&fakeShell{}, remote).Submit(context.Background(), gate.Request{Project: project("gpu-a"), Dir: "/w", CloneURL: cloneURL, Base: "integration/s1"})

	require.ErrorIs(t, err, gate.ErrNoBase)
	require.ErrorContains(t, err, "integration/s1")
	assert.Empty(t, remote.submitted)
}

func TestSubmit_AGateWithPinsAndNoBaseCommitIsVoidAndNamesTheBase(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	shell := twoCommits()
	g := gate.New(shell.run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	job, err := g.Submit(context.Background(), gate.Request{Project: pinnedHostProject(), Dir: "/w", CloneURL: cloneURL, PinBase: "integration/s1"})

	require.NoError(t, err)
	require.NotNil(t, job.Plan.Pins)
	assert.Contains(t, job.Plan.Pins.Void, "integration/s1")
	assert.Empty(t, remote.submitted, "a void pin file starts no job")
	assert.Empty(t, shell.commands)
}

func TestRun_APlanStoredBeforeTheBaseCommitWasRecordedIsVoidAndNeverAMatch(t *testing.T) {
	t.Parallel()

	for name, plan := range map[string]gate.Plan{
		"pins that were read, no base commit": {Gate: "true", BuildHost: "local", Pins: &gate.PinCheck{Tools: []gate.Pin{{Tool: "gofake", Version: "0.0.1"}}}},
		"the same plan with its base commit":  {Gate: "true", BuildHost: "local", BaseSHA: shaOld, Pins: &gate.PinCheck{Tools: []gate.Pin{{Tool: "gofake", Version: "0.0.1"}}}},
	} {
		shell := twoCommits()
		g := gate.New(shell.run, (&fakeTree{states: []gate.State{clean(headA), clean(headA)}}).read, nil)
		p := pinnedHostProject()
		p.BuildHost = "local"

		result, err := g.Run(context.Background(), gate.Request{Project: p, Dir: "/w", Plan: plan})

		require.NoError(t, err, name)
		assert.Empty(t, shell.commands, "%s: a plan that holds its pins reads no pin file", name)

		if plan.BaseSHA == "" {
			assert.False(t, result.Passed, name)
			assert.Contains(t, result.Report, "no base commit for the pin file .tool-versions", name)
			assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown)}, result.Steps, name)
		} else {
			assert.NotContains(t, result.Report, "no base commit", name)
		}
	}
}

func TestRun_AGateThatReadsThePinFileNowTakesTheBaseCommitFromItsPlan(t *testing.T) {
	t.Parallel()

	shell := twoCommits()
	g := gate.New(shell.run, (&fakeTree{states: []gate.State{clean(headA), clean(headA)}}).read, nil)
	p := pinnedHostProject()
	p.BuildHost = "local"

	_, err := g.Run(context.Background(), gate.Request{Project: p, Dir: "/w", Plan: gate.Plan{Gate: "true", BuildHost: "local", BaseSHA: shaOld}})

	require.NoError(t, err)
	require.NotEmpty(t, shell.commands)
	assert.Equal(t, "git show "+shaOld+":.tool-versions", shell.commands[0])
}
