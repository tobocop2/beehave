package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

// sandboxRig is an agents value over a real home folder and a real work folder.
type sandboxRig struct {
	f    *agentFixture
	home string
	work string
}

// newSandboxRig is a rig whose work folder is a git clone that the engine records, so the hooks read of a step finds a repository.
func newSandboxRig(t *testing.T) *sandboxRig {
	t.Helper()

	r := newFolderRig(t)
	runGit(t, r.work, "init", "-q")

	return r
}

// newFolderRig is a rig whose work folder is an empty folder of the clones: no clone and no worktree record names it.
func newFolderRig(t *testing.T) *sandboxRig {
	t.Helper()

	home, work := t.TempDir(), filepath.Join(t.TempDir(), "repos", "p")
	require.NoError(t, os.MkdirAll(work, 0o700))
	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.trees = treeAt(work)
	f.agents.repos = gitexec.NewRepositories(filepath.Dir(work))
	f.agents.home = func() (string, error) { return home, nil }
	f.agents.identity = func(context.Context, gitexec.Runner) agent.GitIdentity { return rigIdentity() }
	f.agents.projects = repositoryProject()
	f.agents.runners = []model.Runner{{Name: "main", Agent: model.Agent("fake"), ConfigDir: filepath.Join(home, "login")}}

	return &sandboxRig{f: f, home: home, work: work}
}

func rigIdentity() agent.GitIdentity {
	return agent.GitIdentity{Name: "Rig Owner", Email: "rig@example.com"}
}

func TestSandboxPlan_TheIdentityIsTheGitIdentityOfTheEngineAtTheWorktree(t *testing.T) {
	global := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(global, []byte("[user]\n\tname = Global Owner\n\temail = global@example.com\n"), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	r := newSandboxRig(t)
	r.f.agents.identity = newAgents(config.Config{}, flow.Options{StateDir: filepath.Dir(filepath.Dir(r.work))}, treeAt(r.work), newAdapters(nil, nil, nil), nil).identity
	runGit(t, r.work, "config", "user.email", "worktree@example.com")

	req := request()
	req.Sandbox = &model.Sandbox{}
	_, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.NotNil(t, r.f.spec.Sandbox)
	assert.Equal(t, agent.GitIdentity{Name: "Global Owner", Email: "worktree@example.com"}, r.f.spec.Sandbox.Identity,
		"the email of the worktree config wins, and the name comes from the global config")
}

func TestSandboxPlan_TheSocketOfTheSSHAgentOfTheEngineIsOnTheDenyList(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	socket := filepath.Join(t.TempDir(), "agent.sock")
	r.f.agents.env = map[string]string{"SSH_AUTH_SOCK": socket}
	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.NotNil(t, r.f.spec.Sandbox)
	assert.Contains(t, r.f.spec.Sandbox.Deny, socket)
	assert.Contains(t, r.f.spec.Sandbox.Deny, filepath.Join(r.home, ".ssh"), "the key folder stays denied")
}

func TestSandboxPlan_ASocketThatIsALinkIsDeniedWithItsTarget(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	folder := evaluated(t, t.TempDir())
	target, link := filepath.Join(folder, "real.sock"), filepath.Join(folder, "link.sock")
	require.NoError(t, os.WriteFile(target, nil, 0o600))
	require.NoError(t, os.Symlink(target, link))
	r.f.agents.env = map[string]string{"SSH_AUTH_SOCK": link}
	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.NotNil(t, r.f.spec.Sandbox)
	assert.Contains(t, r.f.spec.Sandbox.Deny, link)
	assert.Contains(t, r.f.spec.Sandbox.Deny, target)
}

func TestSandboxPlan_AnEngineWithNoSSHAgentAddsNoSocketToTheDenyList(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.f.agents.env = map[string]string{"PATH": "/bin"}
	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	require.NotNil(t, r.f.spec.Sandbox)
	assert.NotContains(t, r.f.spec.Sandbox.Deny, "")
	assert.Equal(t, credentialPaths(r.home, filepath.Join(r.home, "login"), nil), r.f.spec.Sandbox.Deny)
}

func TestSandboxPlan_AStepWithNoGitIdentityDoesNotStartAndNamesTheGitConfigCommand(t *testing.T) {
	t.Parallel()

	for name, identity := range map[string]agent.GitIdentity{
		"no_email": {Name: "Owner"}, "no_name": {Email: "owner@example.com"}, "neither": {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := newSandboxRig(t)
			r.f.agents.identity = func(context.Context, gitexec.Runner) agent.GitIdentity { return identity }
			req := request()
			req.Sandbox = &model.Sandbox{}

			got, err := r.f.agents.Run(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
			assert.Contains(t, got.Note, "git config user.email")
			assert.Nil(t, r.f.spec.Sandbox)
		})
	}
}

func (r *sandboxRig) touch(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, nil, 0o600))
}

// writeNote resolves one path as a write path against the protected paths of the rig: the credential paths and the closed paths of the work folder.
// It returns the resolved paths and the refusal note, which is empty when the path is allowed.
func (r *sandboxRig) writeNote(t *testing.T, paths ...string) ([]string, string) {
	t.Helper()

	deny := credentialPaths(r.home, filepath.Join(r.home, "login"), nil)
	closed, err := closedPaths(t.Context(), r.git(), r.work, r.home)
	require.NoError(t, err)

	return writablePaths(paths, r.home, r.work, slices.Concat(deny, closed))
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
	// The hooks folder comes from the global config: a core.hooksPath in the clone config is a setting that the engine refuses.
	global := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	r := newSandboxRig(t)
	runGit(t, r.work, "config", "--file", global, "core.hooksPath", "ci/hooks")

	for _, bad := range []string{
		".git", ".git/hooks", ".git/config", ".claude", ".claude/settings.json", ".husky", ".husky/pre-commit", ".githooks", "ci/hooks", "ci/hooks/pre-push",
		"app/.git/hooks", "sub/.claude", filepath.Join(r.work, ".git", "hooks"), ".",
	} {
		_, note := r.writeNote(t, bad)

		assert.Contains(t, note, "The step did not start", bad)
	}

	got, note := r.writeNote(t, "cache", "out/.github")
	assert.Empty(t, note)
	assert.Equal(t, []string{filepath.Join(r.work, "cache"), filepath.Join(r.work, "out", ".github")}, got)
}

func TestSandboxPlan_TheClosedPathsOfTheWorkFolderAreDeniedForWriteAndKeepTheirFilesReadable(t *testing.T) {
	// The hooks folder comes from the global config: a core.hooksPath in the clone config is a setting that the engine refuses.
	global := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	r := newSandboxRig(t)
	runGit(t, r.work, "config", "--file", global, "core.hooksPath", "ci/hooks")

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

func TestSandboxPlan_ACloneWhoseConfigNamesAHooksFolderStartsNoStep(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	runGit(t, r.work, "config", "core.hooksPath", "ci/hooks")

	req := request()
	req.Sandbox = &model.Sandbox{}

	got, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, "a plan without the deny of the hooks folder never starts")
	assert.Contains(t, got.Note, "holds core.hookspath, which beehave does not allow")
	assert.Contains(t, got.Note, "The step did not start.")
	assert.Nil(t, r.f.spec.Sandbox)
}

func TestSandboxPlan_ACloneWhoseGitConfigCannotBeReadStartsNoStepAndTheNoteNamesTheRead(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}

	r := newSandboxRig(t)
	require.NoError(t, os.Chmod(filepath.Join(r.work, ".git", "config"), 0))

	resolved, err := filepath.EvalSymlinks(r.work)
	require.NoError(t, err)

	req := request()
	req.Sandbox = &model.Sandbox{}

	got, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "read the git settings of "+resolved)
	assert.Contains(t, got.Note, "The step did not start.")
	assert.Nil(t, r.f.spec.Sandbox)
}

func TestSandboxPlan_AGitFileOfALinkedWorkTreeIsNotInTheWriteDenyList(t *testing.T) {
	t.Parallel()

	r := newFolderRig(t)
	main := filepath.Join(filepath.Dir(r.work), "main")
	require.NoError(t, os.Mkdir(main, 0o700))
	runGit(t, main, "init", "-q", "-b", "main")
	commitIn(t, main, "start")
	runGit(t, main, "worktree", "add", "-q", "--detach", r.work, "HEAD")
	require.FileExists(t, filepath.Join(r.work, ".git"), "a linked work tree has a .git file")

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
	require.NoError(t, os.Mkdir(filepath.Join(r.home, ".ssh"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(r.work, ".git"), filepath.Join(r.work, "lnk")))
	require.NoError(t, os.Symlink(filepath.Join(r.home, ".ssh"), filepath.Join(r.work, "sshlnk")))
	require.NoError(t, os.Symlink(filepath.Join(r.home, ".ssh"), filepath.Join(r.work, "shared")))

	req := request()

	for _, bad := range []string{"lnk", "lnk/hooks", "sshlnk", "shared/new/deeper", filepath.Join(r.home, ".SSH")} {
		_, note := r.writeNote(t, bad)

		assert.Contains(t, note, "overlaps the protected path", bad)
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

	_, note := r.writeNote(t, "fresh/dir")
	assert.Empty(t, note)
}

// git returns the runner of the engine for the clone at the work folder of the rig.
func (r *sandboxRig) git() gitexec.Runner {
	return r.f.agents.repos.Runner(r.work, 0)
}

func TestHooksPath_ReadsTheFolderThatCoreHooksPathNames(t *testing.T) {
	global := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	r := newSandboxRig(t)

	hooks := func() string {
		path, err := hooksPath(t.Context(), r.git(), r.work, r.home)
		require.NoError(t, err)

		return path
	}

	assert.Empty(t, hooks(), "not set")

	runGit(t, r.work, "config", "--file", global, "core.hooksPath", "")
	assert.Empty(t, hooks(), "set empty")

	runGit(t, r.work, "config", "--file", global, "core.hooksPath", "tools/hooks")
	assert.Equal(t, filepath.Join(r.work, "tools", "hooks"), hooks())

	runGit(t, r.work, "config", "--file", global, "core.hooksPath", "~/shared-hooks")
	assert.Equal(t, filepath.Join(r.home, "shared-hooks"), hooks())

	runGit(t, r.work, "config", "--file", global, "core.hooksPath", "/opt/hooks")
	assert.Equal(t, "/opt/hooks", hooks())
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

func TestSandboxPlan_ASandboxReadPatternThatMatchesACredentialPathStopsTheStep(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := request()
	req.Sandbox = &model.Sandbox{}

	for _, pattern := range []string{"~/.ss*", "~/.s?h", "/srv/ke*"} {
		r.f.spec.Sandbox = nil
		r.f.agents.projects = map[string]config.Project{"p": {Project: model.Project{SandboxRead: []string{pattern}, SandboxDeny: []string{"/srv/keys"}}}}

		got, err := r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, pattern)
		assert.Contains(t, got.Note, "sandbox_read path", pattern)
		assert.Nil(t, r.f.spec.Sandbox, "the agent never ran for "+pattern)
	}
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

func TestSandboxPlan_TheDataFileIsTheOnlyStateFolderPathTheSandboxReads(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.f.agents.projects = map[string]config.Project{"p": {Project: model.Project{SandboxRead: []string{"/opt/tools"}}}}
	state := filepath.Join(r.home, ".local", "state", "beehave")
	data := filepath.Join(state, "inputs", "7.agent_3Aimplement_231", "data.json")

	req := request()
	req.Sandbox = &model.Sandbox{}
	req.DataFile = data

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	plan := r.f.spec.Sandbox
	require.NotNil(t, plan, "a data file under the home folder does not stop the step")
	assert.Equal(t, []string{"/opt/tools", data}, plan.Read, "the one file joins the read paths of the project")
	assert.NotContains(t, plan.Read, filepath.Dir(data), "never the inputs folder")
	assert.NotContains(t, plan.Read, state, "never the state folder")

	req.DataFile = ""
	_, err = r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{"/opt/tools"}, r.f.spec.Sandbox.Read, "a step with no data file reads no path of the state folder")
}

func TestSandboxPlan_ADataFileInACredentialPathStopsTheStep(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := request()
	req.Sandbox = &model.Sandbox{}
	req.DataFile = filepath.Join(r.home, ".ssh", "inputs", "7.json")

	result, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, "The file "+req.DataFile+" of the state folder overlaps the credential path "+filepath.Join(r.home, ".ssh"))
}

func TestSandboxPlan_AnAllowWritePathStartsAtTheWorkFolderAndMayNotTouchACredentialPath(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)

	got, note := r.writeNote(t, "out", "~/cache", "/var/data")
	assert.Empty(t, note)
	assert.Equal(t, []string{filepath.Join(r.work, "out"), filepath.Join(r.home, "cache"), "/var/data"}, got)

	for _, bad := range []string{"~/.ssh", "~/.ssh/keys", "~", filepath.Dir(r.home), "~/.config/beehave/state", ".env"} {
		_, badNote := r.writeNote(t, bad)

		assert.Contains(t, badNote, "The step did not start", bad)
	}
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

	assert.Same(t, plan, agents{}.specOf(request(), model.Runner{}, "", nil, nil, "", plan, nil).Sandbox)
	assert.Nil(t, agents{}.specOf(request(), model.Runner{}, "", nil, nil, "", nil, nil).Sandbox)
}

func TestAgents_TheSpecOfAStepCarriesItsSkills(t *testing.T) {
	t.Parallel()

	skills := []string{"/login/skills/review"}

	assert.Equal(t, skills, agents{}.specOf(request(), model.Runner{}, "", nil, nil, "", nil, skills).Skills, "a step with no sandbox carries them too")
	assert.Empty(t, agents{}.specOf(request(), model.Runner{}, "", nil, nil, "", nil, nil).Skills)
}

func TestWithin_ComparesWholeNamesAndNotPrefixes(t *testing.T) {
	t.Parallel()

	assert.True(t, within("/a/b", "/a/b"))
	assert.True(t, within("/a/b", "/a/b/c"))
	assert.False(t, within("/a/b", "/a/bc"))
	assert.False(t, within("/a/b", "/a"))
}

func TestWithin_APatternCoversEachMatchAndThePathsBelowItWithoutCase(t *testing.T) {
	t.Parallel()

	assert.True(t, within("/a/b*", "/a/bc/d"), "a pattern covers the files under each folder it matches")
	assert.True(t, within("/a/**/.env*", "/a/x/.ENV"), "a pattern compares without case")
	assert.True(t, within("/a/[b]", "/a/[b]/c"), "a rule with a glob character also covers its literal name")
	assert.False(t, within("/a/b*", "/a/c/b"))
}

func TestNewAgents_FindsTheHomeFolderOfTheOwner(t *testing.T) {
	t.Parallel()

	a := newAgents(config.Config{}, flow.Options{}, treeAt(t.TempDir()), newAdapters(nil, nil, nil), nil)

	home, err := a.home()

	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(home))
}

func TestSandboxPlan_TheFolderOfTheDataFileIsTheOnlyWorkingDirectoryThatIsAdded(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	state := filepath.Join(r.home, ".local", "state", "beehave")
	data := filepath.Join(state, "inputs", "7.agent_3Aplan_231", "data.json")

	req := request()
	req.Sandbox = &model.Sandbox{}
	req.DataFile = data

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Dir(data)}, r.f.spec.Sandbox.ReadDirs, "the folder of the one data file, never the inputs folder")
	assert.Contains(t, r.f.spec.Sandbox.DenyWrite, filepath.Dir(data), "the folder stays read only")

	req.DataFile = ""
	_, err = r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Empty(t, r.f.spec.Sandbox.ReadDirs, "a step with no data file adds no folder")
}

func TestSandboxPlan_TheFolderOfTheOutputFileOfTheCallIsAReadDirectoryAndNoSiblingCallIs(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	outputs := filepath.Join(r.home, ".local", "state", "beehave", "outputs")
	file, sibling := filepath.Join(outputs, "7.agent_3Aplan_231", "output.json"), filepath.Join(outputs, "8.agent_3Aplan_231")
	r.touch(t, file)
	r.touch(t, filepath.Join(sibling, "output.json"))

	req := request()
	req.Sandbox, req.OutputFile, req.Outputs = &model.Sandbox{}, file, []string{"regressions"}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	plan := r.f.spec.Sandbox
	assert.Equal(t, []string{file}, r.f.spec.Write, "the run spec holds the exact output file")
	assert.NotContains(t, plan.ReadDirs, filepath.Dir(file), "the plan of the sandbox no longer carries the output folder")
	assert.NotContains(t, plan.DenyWrite, filepath.Dir(file), "the folder of the output file is not read only")

	args := claude.New(nil).Sandbox(*plan, "", t.TempDir(), r.f.spec.Write, nil).Args
	assert.Contains(t, args, filepath.Dir(file), "the folder of the one output file is a working directory of the call")
	assert.NotContains(t, args, sibling, "the folder of another call stays closed")
	assert.NotContains(t, args, outputs, "never the outputs folder")

	req.Outputs = nil
	_, err = r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Empty(t, r.f.spec.Write, "a call that declares no outputs opens no output file")
}

func TestAgentRun_AStepWithNoSandboxHandsItsOutputFileToTheLaunch(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	file := filepath.Join(r.home, ".local", "state", "beehave", "outputs", "7.agent_3Areview_231", "output.json")
	r.touch(t, file)

	req := request()
	req.Sandbox, req.OutputFile, req.Outputs = nil, file, []string{"verdict"}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	assert.Nil(t, r.f.spec.Sandbox, "the step has no sandbox")
	assert.Equal(t, []string{file}, r.f.spec.Write, "the run spec holds the output file, so every launch opens it")
}
