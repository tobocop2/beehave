package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc/gitexec"
)

// repositoryProject returns the project p with a source, so the engine keeps a clone of it and pins its git calls to the records of the clones.
func repositoryProject() map[string]config.Project {
	return map[string]config.Project{"p": {Project: model.Project{Name: "p", Source: "/srv/repo"}}}
}

// plainProject returns the project p with no source and no forge, so the engine keeps no clone of it and runs git in its folders.
func plainProject() map[string]config.Project {
	return map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}
}

// ownerGlobal makes the global git config of the test hold text, with no system config.
func ownerGlobal(t *testing.T, text string) {
	t.Helper()

	global := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(global, []byte(text), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func TestProjectGit_AProjectWithARepositoryRefusesAFolderNoCloneKnowsAndAPlainProjectRunsThere(t *testing.T) {
	t.Parallel()

	dir := repoWithCommit(t)
	found := gitexec.NewRepositories(t.TempDir())

	pinned := gitOf(repositoryProject(), "p", found).Runner(dir, 0).Run(t.Context(), []string{"rev-parse", "HEAD"})
	require.ErrorIs(t, pinned.SetupErr, gitexec.ErrUnknownRepository, "a project with a repository runs git only in a clone or its worktree")

	plain := gitOf(plainProject(), "p", found).Runner(dir, 0).Run(t.Context(), []string{"rev-parse", "HEAD"})
	require.Zero(t, plain.ExitCode, plain.Stderr)
	assert.NotEmpty(t, plain.Stdout)
}

func TestProjectGit_AProjectThatTheConfigDoesNotHoldRunsNoGitInAFolderNoCloneKnows(t *testing.T) {
	t.Parallel()

	dir := repoWithCommit(t)
	found := gitexec.NewRepositories(t.TempDir())

	for name, projects := range map[string]map[string]config.Project{"no config": nil, "another project": plainProject()} {
		got := gitOf(projects, "ghost", found).Runner(dir, 0).Run(t.Context(), []string{"rev-parse", "HEAD"})
		require.ErrorIs(t, got.SetupErr, gitexec.ErrUnknownRepository, "%s: a project the config does not hold counts as one with a repository", name)
	}

	got := localRunner(plainProject(), found)("ghost", "git", dir, 0).Run(t.Context(), []string{"rev-parse", "HEAD"})
	require.ErrorIs(t, got.SetupErr, gitexec.ErrUnknownRepository, "the project gate head read of a project the config does not hold is pinned")
}

func TestSandboxPlan_AStepOfAProjectThatTheConfigDoesNotHoldDoesNotStartInAFolderNoCloneKnows(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	ownerGlobal(t, "[user]\n\tname = Global Owner\n\temail = global@example.com\n")

	r := newFolderRig(t)
	r.f.agents.projects = nil
	r.f.agents.identity = gitIdentity

	req := request()
	req.Sandbox = &model.Sandbox{}
	got, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, "a project the config does not hold counts as one with a repository")
	assert.Contains(t, got.Note, gitexec.ErrUnknownRepository.Error(), "the note names the folder that no clone knows, not the missing identity")
	assert.Nil(t, r.f.spec.Sandbox)
}

func TestGateSandbox_AGateOfAProjectThatTheConfigDoesNotHoldDoesNotStartInAFolderNoCloneKnows(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}

	out, code := sandboxFor(t, fake, nil).run(t.Context(), "ghost", repoWithCommit(t), []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code, out)
	assert.Nil(t, fake.argv, "the gate ran no code: a project the config does not hold counts as one with a repository")
}

func TestProjectGit_TheCopyOfAPlainGitFolderNamesItsRecordAndTheCommonFolder(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	dir := repoWithCommit(t)

	_, code := sandboxFor(t, fake, nil).run(t.Context(), "p", dir, []string{"true"}, nil, 0)

	require.Zero(t, code)
	assert.Contains(t, fake.plan.Read, filepath.Join(dir, ".git"), "the gate in the copy reads the objects of the folder, as git reads them")
	assert.NoDirExists(t, filepath.Join(dir, ".git", "worktrees", "work"), "the record of the copy goes when the gate ends")
}

func TestProjectGit_ACopyRecordThatGitCannotReadIsAnError(t *testing.T) {
	t.Parallel()

	_, err := gitOf(plainProject(), "p", gitexec.NewRepositories(t.TempDir())).copyRecord(t.Context(), t.TempDir())

	require.ErrorContains(t, err, "git rev-parse in")
}

func TestProjectGit_ACopyRecordOfOneLineIsAnError(t *testing.T) { //nolint:paralleltest // t.Setenv puts the fake git on the path
	putGitOnPath(t, "#!/bin/sh\necho /one/line\n")

	_, err := gitOf(plainProject(), "p", gitexec.NewRepositories(t.TempDir())).copyRecord(t.Context(), t.TempDir())

	require.ErrorContains(t, err, "not a git dir and a common dir")
}

func TestSandboxPlan_AStepOfAPlainProjectCarriesTheOwnerGlobalIdentity(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	ownerGlobal(t, "[user]\n\tname = Global Owner\n\temail = global@example.com\n")

	r := newSandboxRig(t)
	r.f.agents.projects = plainProject()
	r.f.agents.identity = gitIdentity

	req := request()
	req.Sandbox = &model.Sandbox{}
	_, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.NotNil(t, r.f.spec.Sandbox)
	assert.Equal(t, agent.GitIdentity{Name: "Global Owner", Email: "global@example.com"}, r.f.spec.Sandbox.Identity,
		"a folder of a project with no repository is no clone, and git reads the identity as the owner's git does")
}

func TestSandboxPlan_AStepOfAPlainProjectClosesTheGlobalHooksFolder(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	hooks := filepath.Join(t.TempDir(), "hooks")
	ownerGlobal(t, "[core]\n\thooksPath = "+hooks+"\n")

	r := newSandboxRig(t)
	r.f.agents.projects = plainProject()

	req := request()
	req.Sandbox = &model.Sandbox{}
	_, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.NotNil(t, r.f.spec.Sandbox)
	assert.Contains(t, r.f.spec.Sandbox.DenyWrite, hooks, "the hooks folder of the owner global config stays closed")
}

func TestGateSandbox_AGateOfAPlainProjectRefusesAWritePathInTheGlobalHooksFolder(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	hooks := filepath.Join(t.TempDir(), "hooks")
	ownerGlobal(t, "[core]\n\thooksPath = "+hooks+"\n")

	fake := &fakeEnforcer{}
	box := sandboxFor(t, fake, nil)
	project := box.projects["p"]
	project.GateAllowWrite = []string{hooks}
	box.projects["p"] = project

	out, code := box.run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "overlaps the protected path")
	assert.Nil(t, fake.argv, "the gate ran no code")
}

func TestProjectGate_TheGateOfAPlainProjectReadsTheHeadOfItsGitFolder(t *testing.T) {
	t.Parallel()

	dir := repoWithCommit(t)
	gate := projectGate{programs: newPrograms(config.Config{Projects: plainProject()}, nil, cloneLock{repos: filepath.Join(t.TempDir(), "repos")}, adapters{}, nil)}

	state, err := gate.treeOf("p")(t.Context(), dir)

	require.NoError(t, err)
	assert.Equal(t, gitOut(t, dir, "rev-parse", "HEAD"), state.Head)
}
