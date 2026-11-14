package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

// sandboxRig is an agents value over a real home folder and a real work folder.
type sandboxRig struct {
	f    *agentFixture
	home string
	work string
}

func newSandboxRig(t *testing.T) *sandboxRig {
	t.Helper()

	home, work := t.TempDir(), t.TempDir()
	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.trees = treeAt(work)
	f.agents.home = func() (string, error) { return home, nil }
	f.agents.runners = []model.Runner{{Name: "main", Agent: model.Agent("fake"), ConfigDir: filepath.Join(home, "login")}}

	return &sandboxRig{f: f, home: home, work: work}
}

func (r *sandboxRig) touch(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, nil, 0o600))
}

func TestSandboxPlan_ASandboxedStepGetsAPlanAndAStepWithoutOneGetsNone(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := request()

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Nil(t, r.f.spec.Sandbox)

	req.Sandbox = &model.Sandbox{Network: []string{"api.example.org"}}
	_, err = r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, r.f.spec.Sandbox)
	assert.Equal(t, r.work, r.f.spec.Sandbox.Work)
	assert.Equal(t, []string{"api.example.org"}, r.f.spec.Sandbox.Network)
	assert.Empty(t, r.f.spec.Sandbox.AllowWrite)
}

func TestSandboxPlan_TheEngineListHoldsTheCredentialPathsTheRunnerFolderAndTheEnvFilesOfTheWorkFolder(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.touch(t, filepath.Join(r.home, ".claude.json"))
	r.touch(t, filepath.Join(r.home, ".claude", "settings.json"))
	r.touch(t, filepath.Join(r.home, ".cargo", "credentials.toml"))
	r.touch(t, filepath.Join(r.work, ".env"))
	r.touch(t, filepath.Join(r.work, ".env.local"))

	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	deny := r.f.spec.Sandbox.Deny
	for _, want := range []string{
		".ssh", ".aws", ".config/gh", ".config/gcloud", ".azure", ".gnupg", ".netrc", ".npmrc", ".pypirc", ".git-credentials", ".docker", ".kube",
		".terraform.d", ".config/op", ".config/beehave", "Library/Keychains", ".local/share/keyrings", ".claude", ".claude.json", ".cargo/credentials.toml",
	} {
		assert.Contains(t, deny, filepath.Join(r.home, want))
	}

	assert.Contains(t, deny, filepath.Join(r.home, "login"), "the config folder of the runner")
	assert.NotContains(t, deny, filepath.Join(r.home, ".cargo", "credentials*"), "a glob is expanded to the paths that exist")
}

func TestSandboxPlan_TheEnvFilesOfTheWorkFolderAreDeniedByPatternAtAnyDepthAndBeforeTheyExist(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	// No .env file exists when the plan is made, and the patterns are there all the same.
	assert.Equal(t, []string{filepath.Join(r.work, ".env*"), filepath.Join(r.work, "**", ".env*")}, r.f.spec.Sandbox.DenyPatterns)
}

// runGit runs git in dir.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // a test runs git on its own temporary folder
	command.Dir = dir

	out, err := command.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestMatching_ABadPatternMatchesNothing(t *testing.T) {
	t.Parallel()

	assert.Empty(t, matching("["))
}

func TestSandboxPlan_AnAllowWritePathMayNotReopenGitTheAgentSettingsOrAHooksFolder(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	runGit(t, r.work, "init", "-q")
	runGit(t, r.work, "config", "core.hooksPath", "ci/hooks")

	req := request()

	for _, bad := range []string{
		".git", ".git/hooks", ".git/config", ".claude", ".claude/settings.json", ".husky", ".husky/pre-commit", ".githooks", "ci/hooks", "ci/hooks/pre-push",
		"app/.git/hooks", "sub/.claude", filepath.Join(r.work, ".git", "hooks"), ".",
	} {
		r.f.spec.Sandbox = nil
		req.Sandbox = &model.Sandbox{AllowWrite: []string{bad}}

		got, err := r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, bad)
		assert.Contains(t, got.Note, "The step did not start", bad)
		assert.Nil(t, r.f.spec.Sandbox, "the agent never ran for "+bad)
	}

	req.Sandbox = &model.Sandbox{AllowWrite: []string{"cache", "out/.github"}}
	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(r.work, "cache"), filepath.Join(r.work, "out", ".github")}, r.f.spec.Sandbox.AllowWrite)
}

func TestSandboxPlan_TheClosedPathsOfTheWorkFolderAreDeniedForWriteAndKeepTheirFilesReadable(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	runGit(t, r.work, "init", "-q")
	runGit(t, r.work, "config", "core.hooksPath", "ci/hooks")

	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	want := []string{
		filepath.Join(r.work, ".git"), filepath.Join(r.work, ".husky"), filepath.Join(r.work, ".githooks"), filepath.Join(r.work, "ci", "hooks"),
	}
	assert.ElementsMatch(t, want, r.f.spec.Sandbox.DenyWrite)
	assert.NotContains(t, r.f.spec.Sandbox.DenyWrite, filepath.Join(r.work, ".claude"), "the CLI writes its own state there")
}

func TestSandboxPlan_AGitFileOfALinkedWorkTreeIsNotInTheWriteDenyList(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.touch(t, filepath.Join(r.work, ".git"))

	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.NotContains(t, r.f.spec.Sandbox.DenyWrite, filepath.Join(r.work, ".git"))
	assert.Contains(t, r.f.spec.Sandbox.DenyWrite, filepath.Join(r.work, ".husky"))
}

func TestSandboxPlan_APathIsComparedWithoutCaseAndAfterSymlinksAreResolved(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	require.NoError(t, os.Mkdir(filepath.Join(r.work, ".git"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(r.home, ".ssh"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(r.work, ".git"), filepath.Join(r.work, "lnk")))
	require.NoError(t, os.Symlink(filepath.Join(r.home, ".ssh"), filepath.Join(r.work, "sshlnk")))
	require.NoError(t, os.Symlink(filepath.Join(r.home, ".ssh"), filepath.Join(r.work, "shared")))

	req := request()

	for _, bad := range []string{"lnk", "lnk/hooks", "sshlnk", "shared/new/deeper", filepath.Join(r.home, ".SSH")} {
		r.f.spec.Sandbox = nil
		req.Sandbox = &model.Sandbox{AllowWrite: []string{bad}}

		got, err := r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, bad)
		assert.Nil(t, r.f.spec.Sandbox, bad)
	}

	for _, link := range []string{"sshlnk", "shared"} {
		r.f.spec.Sandbox = nil
		r.f.agents.projects = map[string]config.Project{"p": {Project: model.Project{SandboxRead: []string{filepath.Join(r.work, link)}}}}
		req.Sandbox = &model.Sandbox{}

		got, err := r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, link)
		assert.Contains(t, got.Note, "sandbox_read path", link)
	}

	r.f.agents.projects = nil
	r.f.spec.Sandbox = nil
	req.Sandbox = &model.Sandbox{AllowWrite: []string{"fresh/dir"}}
	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.NotNil(t, r.f.spec.Sandbox)
}

func TestHooksPath_ReadsTheFolderThatCoreHooksPathNames(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	r := newSandboxRig(t)
	runGit(t, r.work, "init", "-q")

	assert.Empty(t, hooksPath(t.Context(), r.work, r.home), "not set")

	runGit(t, r.work, "config", "core.hooksPath", "")
	assert.Empty(t, hooksPath(t.Context(), r.work, r.home), "set empty")

	runGit(t, r.work, "config", "core.hooksPath", "tools/hooks")
	assert.Equal(t, filepath.Join(r.work, "tools", "hooks"), hooksPath(t.Context(), r.work, r.home))

	runGit(t, r.work, "config", "core.hooksPath", "~/shared-hooks")
	assert.Equal(t, filepath.Join(r.home, "shared-hooks"), hooksPath(t.Context(), r.work, r.home))

	runGit(t, r.work, "config", "core.hooksPath", "/opt/hooks")
	assert.Equal(t, "/opt/hooks", hooksPath(t.Context(), r.work, r.home))
}

func TestSandboxPlan_ASandboxReadPathMayNotOpenTheHomeFolderTheFileSystemOrACredentialPath(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := request()
	req.Sandbox = &model.Sandbox{}

	for _, bad := range []string{
		"/", "~", "~/", r.home, filepath.Dir(r.home), "~/.ssh", "~/.ssh/id_ed25519", "~/.config", "~/.config/beehave/state",
		filepath.Join(r.home, "login"), "/srv/keys", "/opt/app/.env", "/opt/app/.env.local",
	} {
		r.f.spec.Sandbox = nil
		r.f.agents.projects = map[string]config.Project{"p": {Project: model.Project{SandboxRead: []string{bad}, SandboxDeny: []string{"/srv/keys"}}}}

		got, err := r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, bad)
		assert.Contains(t, got.Note, "sandbox_read path", bad)
		assert.Nil(t, r.f.spec.Sandbox, "the agent never ran for "+bad)
	}

	r.f.agents.projects = map[string]config.Project{"p": {Project: model.Project{SandboxRead: []string{"/opt/tools", "~/sdk"}}}}
	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{"/opt/tools", filepath.Join(r.home, "sdk")}, r.f.spec.Sandbox.Read)
}

func TestSandboxPlan_TheProjectFileExtendsTheReadBaseAndTheCredentialListAndNeverShrinksThem(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.f.agents.projects = map[string]config.Project{"p": {Project: model.Project{
		SandboxRead: []string{"/opt/tools", "~/sdk"}, SandboxDeny: []string{"~/.config/vault", "/srv/keys"},
	}}}

	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	plan := r.f.spec.Sandbox
	assert.Equal(t, []string{"/opt/tools", filepath.Join(r.home, "sdk")}, plan.Read)
	assert.Contains(t, plan.Deny, filepath.Join(r.home, ".config", "vault"))
	assert.Contains(t, plan.Deny, "/srv/keys")
	assert.Contains(t, plan.Deny, filepath.Join(r.home, ".ssh"), "the built-in entries stay")
}

func TestSandboxPlan_AnAllowWritePathStartsAtTheWorkFolderAndMayNotTouchACredentialPath(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := request()
	req.Sandbox = &model.Sandbox{AllowWrite: []string{"out", "~/cache", "/var/data"}}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(r.work, "out"), filepath.Join(r.home, "cache"), "/var/data"}, r.f.spec.Sandbox.AllowWrite)

	for _, bad := range []string{"~/.ssh", "~/.ssh/keys", "~", filepath.Dir(r.home), "~/.config/beehave/state", ".env"} {
		r.f.spec.Sandbox = nil
		req.Sandbox = &model.Sandbox{AllowWrite: []string{bad}}

		got, runErr := r.f.agents.Run(t.Context(), req)

		require.NoError(t, runErr)
		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, bad)
		assert.Contains(t, got.Note, "The step did not start", bad)
		assert.Nil(t, r.f.spec.Sandbox, "the agent never ran for "+bad)
	}
}

func TestSandboxPlan_ALinkedWorkTreeGetsItsSharedGitDirectoryAndAPlainCheckoutGetsNone(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	common := filepath.Join(t.TempDir(), "main", ".git")
	gitdir := filepath.Join(common, "worktrees", "w")

	require.NoError(t, os.MkdirAll(gitdir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(r.work, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o600))

	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{common}, r.f.spec.Sandbox.GitDirs)

	plain := newSandboxRig(t)
	require.NoError(t, os.Mkdir(filepath.Join(plain.work, ".git"), 0o700))

	_, err = plain.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Empty(t, plain.f.spec.Sandbox.GitDirs)
}

func TestSandboxPlan_ARelativeGitFileAndAGitDirectoryWithoutCommondirAreRead(t *testing.T) {
	t.Parallel()

	work := t.TempDir()
	gitdir := filepath.Join(work, "..", "elsewhere")
	require.NoError(t, os.MkdirAll(gitdir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: ../elsewhere"), 0o600))

	assert.Equal(t, []string{filepath.Clean(gitdir)}, sharedGitDirs(work))

	assert.Empty(t, sharedGitDirs(t.TempDir()), "no .git")

	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, ".git"), []byte("not a pointer"), 0o600))
	assert.Empty(t, sharedGitDirs(other))
}

func TestSandboxPlan_AStepWithASandboxDoesNotStartWhenTheHomeFolderIsUnknown(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.f.agents.home = func() (string, error) { return "", os.ErrNotExist }
	req := request()
	req.Sandbox = &model.Sandbox{}

	got, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "home folder")
	assert.Nil(t, r.f.spec.Sandbox)
}

func TestAgents_TheSpecOfAStepCarriesItsSandboxPlan(t *testing.T) {
	t.Parallel()

	plan := &agent.SandboxPlan{Work: "/w"}

	assert.Same(t, plan, agents{}.specOf(request(), model.Runner{}, "", nil, nil, "", plan).Sandbox)
	assert.Nil(t, agents{}.specOf(request(), model.Runner{}, "", nil, nil, "", nil).Sandbox)
}

func TestWithin_ComparesWholeNamesAndNotPrefixes(t *testing.T) {
	t.Parallel()

	assert.True(t, within("/a/b", "/a/b"))
	assert.True(t, within("/a/b", "/a/b/c"))
	assert.False(t, within("/a/b", "/a/bc"))
	assert.False(t, within("/a/b", "/a"))
}

func TestNewAgents_FindsTheHomeFolderOfTheOwner(t *testing.T) {
	t.Parallel()

	a := newAgents(config.Config{}, flow.Options{}, treeAt(t.TempDir()), newAdapters(nil, nil, nil))

	home, err := a.home()

	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(home))
}
