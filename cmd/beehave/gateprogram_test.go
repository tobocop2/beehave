package main

import (
	"os"
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

func TestProjectGate_AToolThatTheEngineCannotFindAtItsStartStopsTheGateProgramJobWithTheReason(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	pg := projectGate{projects: programGateProject("/srv/source", []string{"Makefile"}), programs: programs{local: gateRunner(calls, model.CommandResult{}, model.CommandResult{})}}.withTools(t.TempDir())

	out, code := pg.programShellFor("p")(t.Context(), t.TempDir(), t.TempDir(), "make check")

	assert.Equal(t, 1, code)
	assert.Contains(t, out, "find the tool git for the gate program")
	assert.Empty(t, calls.binaries, "no job started")
}

func TestProjectGate_TheEngineResolvesTheToolsOfTheGateProgramJobFromThePathAtItsStart(t *testing.T) {
	t.Parallel()

	tools, err := resolveGateTools("")
	require.NoError(t, err)

	got := projectGate{}.withTools("")

	require.NoError(t, got.toolsErr)
	assert.Equal(t, tools, got.tools)

	for _, found := range []string{tools.Git, tools.Find, tools.Sort, tools.Readlink, tools.Cat, tools.Digest} {
		assert.True(t, filepath.IsAbs(found), found)
	}
}

func TestResolveGateTools_TakesShasumWhenThereIsNoSha256sumAndRefusesWhenThereIsNeither(t *testing.T) {
	t.Parallel()

	tools, err := resolveGateTools("")
	require.NoError(t, err)

	links := map[string]string{"git": tools.Git, "find": tools.Find, "sort": tools.Sort, "readlink": tools.Readlink, "cat": tools.Cat}
	folder := func() string {
		dir := t.TempDir()

		for name, target := range links {
			require.NoError(t, os.Symlink(target, filepath.Join(dir, name)))
		}

		return dir
	}

	_, err = resolveGateTools(folder())
	require.ErrorContains(t, err, "find the tool sha256sum or shasum for the gate program")

	links["shasum"] = tools.Digest
	found, err := resolveGateTools(folder())
	require.NoError(t, err)

	assert.Equal(t, "shasum", filepath.Base(found.Digest))
}

func TestDepsOn_TheGateOfTheEngineHoldsTheToolsOfTheGateProgramJob(t *testing.T) {
	t.Parallel()

	pg, ok := engineDeps(t).Gate.(projectGate)
	require.True(t, ok, "the engine builds a project gate")

	require.NoError(t, pg.toolsErr)
	assert.True(t, filepath.IsAbs(pg.tools.Git), pg.tools.Git)
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

	assert.Contains(t, job, `clone --no-local -- /tree`, "the job copies the worktree of the item")
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
	assert.Contains(t, fake.argv[2], "clone --no-local -- "+repo)
	assert.Contains(t, fake.plan.Read, repo)
	assert.Contains(t, fake.plan.Read, "/src")
}
