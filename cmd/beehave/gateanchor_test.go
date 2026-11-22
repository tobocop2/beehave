package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

const (
	anchorOld = "1111111111111111111111111111111111111111"
	anchorNew = "2222222222222222222222222222222222222222"
)

// fixedTip is a read of a branch tip that gives sha and err for every branch.
func fixedTip(sha string, err error) func(context.Context, string, string, string) (string, error) {
	return func(context.Context, string, string, string) (string, error) { return sha, err }
}

// recordingTip is a read of a branch tip that appends each branch it is asked for to branches and gives sha.
func recordingTip(branches *[]string, sha string) func(context.Context, string, string, string) (string, error) {
	return func(_ context.Context, _, _, branch string) (string, error) {
		*branches = append(*branches, branch)

		return sha, nil
	}
}

// anchorRig is a gate over a project with a source, whose base commit is read by a fake.
type anchorRig struct {
	gate projectGate

	// tips is what the read of the tip got: the remote and the branch of each call.
	tips []string
	// tip is the answer of the read. fetches are the project, the remote and the commit of each fetch. fetchErr is the answer of the fetch. held is
	// the answer of git cat-file: false means that the clone lacks the commit.
	tip      string
	tipErr   error
	held     func(fetched int) bool
	fetches  []string
	fetchErr error
	// commands are the programs that the gate started.
	commands []string
}

func newAnchorRig(t *testing.T) *anchorRig {
	t.Helper()

	rig := &anchorRig{tip: anchorOld, held: func(int) bool { return true }}
	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p", Source: "/src", BaseBranch: "main", Gate: "make check", BuildHost: config.LocalHost}}}
	local := func(_, binary, _ string, _ time.Duration) proc.Runner {
		return recording{run: func(args []string) model.CommandResult {
			rig.commands = append(rig.commands, binary+" "+strings.Join(args, " "))

			switch {
			case binary != "git":
				return model.CommandResult{Stdout: "ok"}
			case args[0] == "status":
				return model.CommandResult{}
			case args[0] == "cat-file":
				if rig.held(len(rig.fetches)) {
					return model.CommandResult{}
				}

				return model.CommandResult{ExitCode: 128, Stderr: "fatal: Not a valid object name"}
			}

			return model.CommandResult{Stdout: "head1\ntree1\n"}
		}}
	}

	rig.gate = projectGate{
		projects: projects,
		programs: programs{trees: treeAt("/tree"), local: local},
		tip: func(_ context.Context, _, remote, branch string) (string, error) {
			rig.tips = append(rig.tips, remote+" "+branch)

			return rig.tip, rig.tipErr
		},
		fetch: func(_ context.Context, project, remote, sha string) error {
			rig.fetches = append(rig.fetches, project+" "+remote+" "+sha)

			return rig.fetchErr
		},
	}

	return rig
}

func TestProjectGate_SubmitReadsTheBaseCommitOnceFromTheSourceAndCarriesItInThePlan(t *testing.T) {
	t.Parallel()

	rig := newAnchorRig(t)

	job, err := rig.gate.Submit(t.Context(), "p", "1", "", "", "", "")

	require.NoError(t, err)
	assert.Equal(t, []string{"/src main"}, rig.tips, "one read, at the source of the project, of the base branch of the project")
	assert.Equal(t, anchorOld, job.Plan.BaseSHA)
}

func TestProjectGate_TheBranchThatTheBaseCommitIsReadFromIsThePinBaseThenTheBaseThenTheProjectBase(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct{ base, pinBase, want string }{
		"the pin base wins":                  {"integration/s2", "integration/s1", "/src integration/s1"},
		"the base when there is no pin base": {"integration/s2", "", "/src integration/s2"},
		"the project base":                   {"", "", "/src main"},
	} {
		rig := newAnchorRig(t)

		_, err := rig.gate.Submit(t.Context(), "p", "1", "", c.base, c.pinBase, "")

		require.NoError(t, err, name)
		assert.Equal(t, []string{c.want}, rig.tips, name)
	}
}

func TestProjectGate_TheKeyChangesWhenTheBaseBranchMovesToAnotherCommit(t *testing.T) {
	t.Parallel()

	rig := newAnchorRig(t)

	before, err := rig.gate.Key(t.Context(), "p", "1", "", "", "")
	require.NoError(t, err)

	rig.tip = anchorNew

	after, err := rig.gate.Key(t.Context(), "p", "1", "", "", "")
	require.NoError(t, err)

	require.NotEmpty(t, before.ID)
	assert.NotEqual(t, before.ID, after.ID, "a pass at one base commit must not stand for the gate at another")
}

func TestProjectGate_ARunThatHoldsAPlanReadsNoBaseCommitAndOneWithoutAPlanReadsIt(t *testing.T) {
	t.Parallel()

	rig := newAnchorRig(t)
	planned := flow.GateJob{Plan: gate.Plan{Gate: "make check", BuildHost: config.LocalHost, Pins: &gate.PinCheck{}, BaseSHA: anchorOld}}

	_, err := rig.gate.Run(t.Context(), "p", "1", "", planned)
	require.NoError(t, err)
	assert.Empty(t, rig.tips, "the plan holds the commit that Submit read: the gate does not read it again")

	_, err = rig.gate.Run(t.Context(), "p", "1", "", flow.GateJob{})
	require.NoError(t, err)
	assert.Equal(t, []string{"/src main"}, rig.tips)
}

func TestProjectGate_ABaseBranchThatCannotBeReadFailsTheGateBeforeAnyProgramRunsAndNamesTheBase(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		tip string
		err error
	}{"the read fails": {"", errors.New("no route")}, "the remote answers no commit": {"", gitexec.ErrNoTip}} {
		rig := newAnchorRig(t)
		rig.tip, rig.tipErr = c.tip, c.err

		_, submitErr := rig.gate.Submit(t.Context(), "p", "1", "", "", "", "")
		_, keyErr := rig.gate.Key(t.Context(), "p", "1", "", "", "")
		_, runErr := rig.gate.Run(t.Context(), "p", "1", "", flow.GateJob{})

		for _, err := range []error{submitErr, keyErr, runErr} {
			require.ErrorContains(t, err, `read the base branch "main" of project p`, name)
		}

		assert.NotContains(t, strings.Join(rig.commands, "\n"), "sh -c", name)
	}
}

func TestProjectGate_ACommitThatTheCloneLacksIsFetchedOnceAndAGateThatStillLacksItFails(t *testing.T) {
	t.Parallel()

	rig := newAnchorRig(t)
	rig.held = func(fetched int) bool { return fetched > 0 }

	_, err := rig.gate.Submit(t.Context(), "p", "1", "", "", "", "")

	require.NoError(t, err)
	assert.Equal(t, []string{"p /src " + anchorOld}, rig.fetches, "one fetch, of the commit by its id, from the source that gave it")

	still := newAnchorRig(t)
	still.held = func(int) bool { return false }

	_, err = still.gate.Submit(t.Context(), "p", "1", "", "", "", "")

	require.ErrorContains(t, err, `the base branch "main" of project p is at `+anchorOld)
	assert.Len(t, still.fetches, 1)

	unfetchable := newAnchorRig(t)
	unfetchable.held = func(int) bool { return false }
	unfetchable.fetchErr = errors.New("offline")

	_, err = unfetchable.gate.Submit(t.Context(), "p", "1", "", "", "", "")

	require.ErrorContains(t, err, "fetch the commit")
	require.ErrorContains(t, err, "offline")
	require.ErrorContains(t, err, `the base branch "main" of project p is at `+anchorOld)
}

func TestProjectGate_AKeyAndASubmitOfOneGateNameTheSameBaseCommitWhenTheBaseMovesBetweenThem(t *testing.T) {
	t.Parallel()

	rig := newAnchorRig(t)

	key, err := rig.gate.Key(t.Context(), "p", "1", "", "", "")
	require.NoError(t, err)

	rig.tip = anchorNew

	job, err := rig.gate.Submit(t.Context(), "p", "1", "", "", "", key.BaseSHA)

	require.NoError(t, err)
	assert.Equal(t, anchorOld, key.BaseSHA)
	assert.Equal(t, anchorOld, job.Plan.BaseSHA, "the job takes the commit that the key read, not the tip that moved after it")
	assert.Equal(t, []string{"/src main"}, rig.tips, "one read of the tip for the whole gate")

	later, err := rig.gate.Submit(t.Context(), "p", "1", "", "", "", "")

	require.NoError(t, err)
	assert.Equal(t, anchorNew, later.Plan.BaseSHA, "a Submit with no key reads the tip itself")
}

func TestProjectGate_AProjectWithNoRepositoryHasNoBaseCommitAndReadsNone(t *testing.T) {
	t.Parallel()

	rig := newAnchorRig(t)
	rig.gate.projects = localProject()

	job, err := rig.gate.Submit(t.Context(), "p", "1", "", "", "", "")

	require.NoError(t, err)
	assert.Empty(t, rig.tips)
	assert.Empty(t, job.Plan.BaseSHA)
}

func TestProjectGate_AForgeThatGivesNoCloneURLFailsTheReadAndReadsNoTip(t *testing.T) {
	t.Parallel()

	rig := newAnchorRig(t)
	rig.gate.projects = map[string]config.Project{"p": {Project: model.Project{Name: "p", Forge: "github", BaseBranch: "main", Gate: "make check", BuildHost: config.LocalHost}}}
	rig.gate.forge = func(model.Project) (forge.Forge, error) { return nil, errors.New("no forge") }

	_, err := rig.gate.Submit(t.Context(), "p", "1", "", "", "", "")

	require.ErrorContains(t, err, "find the repository of project p")
	assert.Empty(t, rig.tips)
}
