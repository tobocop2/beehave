package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

func programGateProject(source string, program []string) map[string]config.Project {
	projects := sandboxedProject(model.SandboxOn, config.LocalHost)
	found := projects["p"]
	found.Source, found.GateProgram = source, program
	projects["p"] = found

	return projects
}

// programBox is a sandbox over a project with a gate_program whose item repository is a clone that the engine made: it returns the box, the enforcer and the repository.
func programBox(t *testing.T, source string) (gateSandbox, *fakeEnforcer, string) {
	t.Helper()

	fake := &fakeEnforcer{}
	box := sandboxFor(t, fake, nil)
	repo := repoWithCommit(t)
	box.projects = programGateProject(source, []string{"Makefile"})
	box.clones = cloneLock{repos: filepath.Dir(repo)}

	return box, fake, repo
}

func TestGateSandbox_TheJobOfAGateProgramMayReadTheItemRepositoryAndTheSourceAndWriteItsOwnFolder(t *testing.T) {
	t.Parallel()

	box, fake, from := programBox(t, "/srv/source")
	root := t.TempDir()

	out, code := box.runJob(t.Context(), "p", from, root, []string{"sh", "-c", "true"}, 0)
	require.Zero(t, code, out)

	assert.Contains(t, fake.plan.Read, from)
	assert.Contains(t, fake.plan.Read, "/srv/source")
	assert.Contains(t, fake.plan.Write, root)
	assert.NotContains(t, fake.plan.Write, from)
	assert.Equal(t, root, fake.dir)
}

func TestGateSandbox_AJobWhoseSourceIsAForgeUrlReadsOnlyTheItemRepository(t *testing.T) {
	t.Parallel()

	box, fake, from := programBox(t, "https://example.invalid/r.git")

	out, code := box.runJob(t.Context(), "p", from, t.TempDir(), []string{"true"}, 0)
	require.Zero(t, code, out)

	assert.Contains(t, fake.plan.Read, from)
	assert.NotContains(t, fake.plan.Read, "https://example.invalid/r.git")
}

func TestGateSandbox_ACommandThatIsNotAJobReadsNothingBeyondItsPlan(t *testing.T) {
	t.Parallel()

	box, fake, repo := programBox(t, "/srv/source")

	out, code := box.run(t.Context(), "p", repo, []string{"true"}, nil, 0)
	require.Zero(t, code, out)

	assert.NotContains(t, fake.plan.Read, "/srv/source")
}

func TestProjectGate_AGateProgramJobRunsUnderTheSandboxWhenTheProjectHasItOn(t *testing.T) {
	t.Parallel()

	box, fake, from := programBox(t, "/srv/source")
	calls := &gateCalls{}
	gate := projectGate{projects: box.projects, programs: programs{sandbox: box, local: gateRunner(calls, model.CommandResult{}, model.CommandResult{})}}

	_, code := gate.programShellFor("p")(t.Context(), from, t.TempDir(), "make check")
	require.Zero(t, code)

	assert.Equal(t, []string{"sh", "-c", "make check"}, fake.argv)
	assert.Empty(t, calls.binaries, "the job never reaches the plain runner")
}

func TestProjectGate_AGateProgramJobRunsInItsFolderWithTheShellWhenTheSandboxIsOff(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	projects := programGateProject("/srv/source", []string{"Makefile"})
	found := projects["p"]
	found.Sandbox = model.SandboxOff
	projects["p"] = found

	gate := projectGate{projects: projects, programs: programs{local: gateRunner(calls, model.CommandResult{}, model.CommandResult{Stdout: "ok"})}}
	root := t.TempDir()

	out, code := gate.programShellFor("p")(t.Context(), t.TempDir(), root, "make check")
	require.Zero(t, code)

	assert.Equal(t, "ok", out)
	assert.Equal(t, []string{"sh"}, calls.binaries)
	assert.Equal(t, []string{root}, calls.dirs)
}

func TestProjectGate_TheCloneURLOfALocalProjectIsItsSourceOnlyWithAGateProgram(t *testing.T) {
	t.Parallel()

	gate := projectGate{}

	with, err := gate.cloneURL(programGateProject("/srv/source", []string{"Makefile"})["p"])
	require.NoError(t, err)
	assert.Equal(t, "/srv/source", with)

	without, err := gate.cloneURL(programGateProject("/srv/source", nil)["p"])
	require.NoError(t, err)
	assert.Empty(t, without)
}

func TestProjectGate_ARunOfAProjectWithAGateProgramRunsTheJobThatClonesTheItemAndReadsTheBaseFromTheSource(t *testing.T) {
	t.Parallel()

	rig := newAnchorRig(t)
	found := rig.gate.projects["p"]
	found.GateProgram = []string{"Makefile"}
	rig.gate.projects["p"] = found

	result, err := rig.gate.Run(t.Context(), "p", "1", "", flow.GateJob{})
	require.NoError(t, err)
	require.True(t, result.Passed, result.Report)

	var job string

	for _, command := range rig.commands {
		if strings.HasPrefix(command, "sh -c ") {
			job = command
		}
	}

	assert.Contains(t, job, "git clone -- /tree", "the job clones the worktree of the item")
	assert.Contains(t, job, "gpsha="+anchorOld, "the job reads the base commit that the engine read")
	assert.Contains(t, job, "gpurl=/src", "the job falls back to the source of the project")
	assert.True(t, strings.HasSuffix(job, "gpcompare"), "the job ends with the final compare")
}

func TestProjectGate_ARunOfASandboxedProjectWithAGateProgramRunsTheJobUnderTheEnforcerWithTheItemReadable(t *testing.T) {
	t.Parallel()

	box, fake, repo := programBox(t, "/src")
	rig := newAnchorRig(t)
	found := box.projects["p"]
	found.BaseBranch, found.Gate = "main", "make check"
	box.projects["p"] = found
	rig.gate.projects = box.projects
	rig.gate.programs.trees = treeAt(repo)
	rig.gate.programs.sandbox = box

	result, err := rig.gate.Run(t.Context(), "p", "1", "", flow.GateJob{})
	require.NoError(t, err)
	require.True(t, result.Passed, result.Report)

	require.Len(t, fake.argv, 3)
	assert.Contains(t, fake.argv[2], "git clone -- "+repo)
	assert.Contains(t, fake.plan.Read, repo)
	assert.Contains(t, fake.plan.Read, "/src")
}
