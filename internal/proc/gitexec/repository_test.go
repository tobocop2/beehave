package gitexec_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

// engineLayout is a state folder with a clone of an origin at repos/p and a linked worktree of it at trees/p/item, as the engine makes them.
type engineLayout struct {
	root, repos, clone, tree, prog, marks, inc string
	rev                                        string
}

// cloneRunner returns a pinned runner for a clone folder whose .git folder is empty, for tests with a fake git.
func cloneRunner(t *testing.T) gitexec.Runner {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "p", ".git"), 0o700))

	return gitexec.NewRepositories(root).Runner(filepath.Join(root, "p"), 0)
}

// ownerGit sets an owner global config with an identity and no system config, and returns the global config file.
func ownerGit(t *testing.T) string {
	t.Helper()

	global := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(global, []byte("[user]\n\tname = Owner\n\temail = owner@example.com\n[advice]\n\tdetachedHead = false\n"), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	return global
}

func plainGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // the test runs git with its own arguments
	cmd.Dir = dir
	cmd.Stdin = nil

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)

	return strings.TrimSpace(string(out))
}

func newEngineLayout(t *testing.T) engineLayout {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	l := engineLayout{root: root, repos: filepath.Join(root, "repos"), marks: filepath.Join(root, "marks"), prog: filepath.Join(root, "prog")}
	l.clone, l.tree, l.inc = filepath.Join(l.repos, "p"), filepath.Join(root, "trees", "p", "item"), filepath.Join(root, "include.cfg")
	origin := filepath.Join(root, "origin")

	require.NoError(t, os.MkdirAll(l.marks, 0o700))
	plainGit(t, root, "init", "-q", "-b", "main", origin)
	require.NoError(t, os.WriteFile(filepath.Join(origin, "a.txt"), []byte("one\n"), 0o600))
	plainGit(t, origin, "add", "a.txt")
	plainGit(t, origin, "commit", "-q", "-m", "first")
	plainGit(t, root, "clone", "-q", origin, l.clone)
	l.rev = plainGit(t, l.clone, "rev-parse", "HEAD")
	plainGit(t, l.clone, "worktree", "add", "-q", "-b", "item", l.tree, l.rev)
	// The edit changes the size and the mtime, so the index stat data of the file cannot match, whatever the timestamp tick.
	edited := filepath.Join(l.tree, "a.txt")
	require.NoError(t, os.WriteFile(edited, []byte("two, edited\n"), 0o600))

	later := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(edited, later, later))
	require.NoError(t, os.WriteFile(filepath.Join(l.tree, "u.txt"), []byte("u\n"), 0o600))

	return l
}

// program writes the planted program: it leaves a marker, then acts as the kind says (exit 0, copy stdin, or fail).
func (l engineLayout) program(t *testing.T, kind string) {
	t.Helper()

	tail := map[string]string{"touch": "exit 0", "cat": "exec cat", "fail": "exit 1"}[kind]
	require.NoError(t, exectest.Write(l.prog, "#!/bin/sh\n: > \""+l.marks+"/ran\"\n"+tail+"\n"))
	require.NoError(t, os.WriteFile(l.inc, []byte("[core]\n\tfsmonitor = "+l.prog+"\n"), 0o600))
}

func (l engineLayout) ran() bool {
	_, err := os.Stat(filepath.Join(l.marks, "ran"))

	return err == nil
}

// planted is a setting that names a program: its pairs, the kind of program and whether attributes must name the driver.
type planted struct {
	name  string
	kind  string
	attrs bool
	pairs [][2]string
}

// startingSettings are the settings that start a program on a local engine call when git reads them.
func startingSettings(l engineLayout) []planted {
	return []planted{
		{name: "fsmonitor", kind: "touch", pairs: [][2]string{{"core.fsmonitor", l.prog}}},
		{name: "filterCleanSmudge", kind: "cat", attrs: true, pairs: [][2]string{{"filter.evil.clean", l.prog}, {"filter.evil.smudge", l.prog}}},
		{name: "filterProcess", kind: "fail", attrs: true, pairs: [][2]string{{"filter.evil.process", l.prog}}},
		{name: "includePath", kind: "touch", pairs: [][2]string{{"include.path", l.inc}}},
		{name: "includeIf", kind: "touch", pairs: [][2]string{{"includeIf.gitdir:/**.path", l.inc}}},
		{name: "worktreeConfig", kind: "touch", pairs: [][2]string{{"core.repositoryformatversion", "1"}, {"extensions.worktreeConfig", "true"}}},
	}
}

// otherSettings are the planted settings that start no program on a local engine call, listed so the refusal covers each of them.
func otherSettings(l engineLayout) []planted {
	return []planted{
		{name: "hooksPath", pairs: [][2]string{{"core.hooksPath", l.root}}},
		{name: "sshCommand", pairs: [][2]string{{"core.sshCommand", l.prog}}},
		{name: "editor", pairs: [][2]string{{"core.editor", l.prog}}},
		{name: "pager", pairs: [][2]string{{"core.pager", l.prog}}},
		{name: "diffExternal", pairs: [][2]string{{"diff.external", l.prog}}},
		{name: "textconv", pairs: [][2]string{{"diff.evil.textconv", l.prog}}},
		{name: "diffDriverCommand", pairs: [][2]string{{"diff.evil.command", l.prog}}},
		{name: "gpgProgram", pairs: [][2]string{{"gpg.program", l.prog}}},
		{name: "alternateRefsCommand", pairs: [][2]string{{"core.alternateRefsCommand", l.prog}}},
		{name: "pushURL", pairs: [][2]string{{"remote.origin.pushurl", l.prog}}},
	}
}

// plant writes the setting into the config file of common, and the attributes and worktree configs that it needs.
func (l engineLayout) plant(t *testing.T, common string, setting planted) {
	t.Helper()

	l.program(t, setting.kind)

	for _, pair := range setting.pairs {
		plainGit(t, l.root, "config", "--file", filepath.Join(common, "config"), pair[0], pair[1])
	}

	if setting.attrs {
		require.NoError(t, os.MkdirAll(filepath.Join(common, "info"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(common, "info", "attributes"), []byte("* filter=evil diff=evil\n"), 0o600))
	}

	if setting.name == "worktreeConfig" {
		fsmonitor := []byte("[core]\n\tfsmonitor = " + l.prog + "\n")
		for _, dir := range []string{common, filepath.Join(l.clone, ".git", "worktrees", "item"), filepath.Join(common, "worktrees", "item")} {
			if _, err := os.Stat(dir); err == nil {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "config.worktree"), fsmonitor, 0o600))
			}
		}
	}
}

// redirect puts the context in place and returns the common dir whose config the plain git of the tree reads.
func (l engineLayout) redirect(t *testing.T, context string) string {
	t.Helper()

	engine := filepath.Join(l.clone, ".git")

	switch context {
	case "gitfile":
		evil := filepath.Join(l.tree, ".evil")
		require.NoError(t, os.CopyFS(evil, os.DirFS(engine)))
		plainGit(t, l.root, "--git-dir="+evil, "symbolic-ref", "HEAD", "refs/heads/item")
		index, err := os.ReadFile(filepath.Join(engine, "worktrees", "item", "index")) //nolint:gosec // a file of the test
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(evil, "index"), index, 0o600)) //nolint:gosec // a file of the test
		require.NoError(t, os.WriteFile(filepath.Join(l.tree, ".git"), []byte("gitdir: "+evil+"\n"), 0o600))

		return evil
	case "commondir":
		evil := filepath.Join(l.root, "evilcommon")
		require.NoError(t, os.CopyFS(evil, os.DirFS(engine)))
		require.NoError(t, os.WriteFile(filepath.Join(engine, "worktrees", "item", "commondir"), []byte(evil+"\n"), 0o600))

		return evil
	default:
		return engine
	}
}

// engineCalls are the local engine calls in the tree that start a planted program at the base.
func engineCalls(rev string) [][]string {
	return [][]string{
		{"status", "--porcelain"},
		{"diff", "--stat", rev},
		{"ls-files", "--others", "--exclude-standard", "-z", "--", ":/"},
		{"checkout", "-q", "--detach", rev},
	}
}

func TestGitexec_PlantedSettingStartsNoProgram(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	ownerGit(t)

	for _, context := range []string{"clone", "gitfile", "commondir"} { //nolint:paralleltest // the parent test sets the environment of the process
		for i, setting := range startingSettings(engineLayout{}) {
			t.Run(context+"/"+setting.name, func(t *testing.T) {
				control := newEngineLayout(t)
				control.plant(t, control.redirect(t, context), startingSettings(control)[i])
				cmd := exec.CommandContext(t.Context(), "git", "add", "a.txt")
				cmd.Dir = control.tree
				_ = cmd.Run() //nolint:errcheck // the control reads the marker, and a failing filter fails the add
				require.True(t, control.ran(), "the positive control: plain git starts the program")

				l := newEngineLayout(t)
				l.plant(t, l.redirect(t, context), startingSettings(l)[i])
				runner := gitexec.NewRepositories(l.repos).Runner(l.tree, 0)

				for _, call := range engineCalls(l.rev) {
					runner.Run(t.Context(), call)
				}

				assert.False(t, l.ran(), "an engine call started the program")
			})
		}
	}
}

func TestGitexec_RunsInTheRepositoryOfTheEngineLayout(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	ownerGit(t)

	for _, context := range []string{"gitfile", "commondir"} { //nolint:paralleltest // the parent test sets the environment of the process
		t.Run(context, func(t *testing.T) {
			l := newEngineLayout(t)
			l.redirect(t, context)

			out := gitexec.NewRepositories(l.repos).Runner(l.tree, 0).Run(t.Context(), []string{"rev-parse", "--path-format=absolute", "--absolute-git-dir", "--git-common-dir"})
			require.Zero(t, out.ExitCode, out.Stderr)

			assert.Equal(t, []string{filepath.Join(l.clone, ".git", "worktrees", "item"), filepath.Join(l.clone, ".git")}, strings.Fields(out.Stdout))
		})
	}
}

// countingGit puts a git first on PATH that logs each start and runs the real git, and returns the log.
func countingGit(t *testing.T) string {
	t.Helper()

	binary, err := exec.LookPath("git")
	require.NoError(t, err)

	bin := t.TempDir()
	log := filepath.Join(bin, "starts")
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), "#!/bin/sh\necho \"$1\" >> "+log+"\nexec "+binary+" \"$@\"\n"))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))

	return log
}

func TestGitexec_RefusesARepositorySettingOutsideTheAllowList(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	ownerGit(t)

	base := newEngineLayout(t)
	settings := append(startingSettings(base), otherSettings(base)...)
	settings = append(settings, planted{name: "aNewSetting", pairs: [][2]string{{"future.setting", "x"}}})

	for _, setting := range settings { //nolint:paralleltest // the parent test sets the environment of the process
		t.Run(setting.name, func(t *testing.T) {
			l := newEngineLayout(t)
			l.plant(t, filepath.Join(l.clone, ".git"), setting)
			log := countingGit(t)

			for _, dir := range []string{l.clone, l.tree} {
				out := gitexec.NewRepositories(l.repos).Runner(dir, 0).Run(t.Context(), []string{"status"})

				var refusal *gitexec.SettingError
				require.ErrorAs(t, out.SetupErr, &refusal)
				assert.Equal(t, l.clone, refusal.Clone)
				assert.Contains(t, out.Stderr, strings.ToLower(setting.pairs[len(setting.pairs)-1][0]))
				assert.Contains(t, out.Stderr, "git config --unset")
				assert.NotZero(t, out.ExitCode)
			}

			assert.Equal(t, "config\nconfig", calls(t, log), "only the read of the settings started git")
		})
	}
}

func TestGitexec_ARefusalNamesAWholeKeyWhoseSubsectionHoldsASpace(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	ownerGit(t)

	l := newEngineLayout(t)
	plainGit(t, l.clone, "config", "remote.a remote.b.sshcommand", "x")

	out := gitexec.NewRepositories(l.repos).Runner(l.tree, 0).Run(t.Context(), []string{"status"})

	var refusal *gitexec.SettingError
	require.ErrorAs(t, out.SetupErr, &refusal)
	assert.Equal(t, []string{"remote.a remote.b.sshcommand"}, refusal.Keys, "the refusal names the key as git stores it, not its parts")
}

// relativeRecord rewrites the gitdir file of the record name in the clone with the name of the .git file of tree relative to the record, as git 2.48 and
// later write it under worktree.useRelativePaths.
func relativeRecord(t *testing.T, l engineLayout, name, tree string) {
	t.Helper()

	record := filepath.Join(l.clone, ".git", "worktrees", name)
	relative, err := filepath.Rel(record, filepath.Join(tree, ".git"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(record, "gitdir"), []byte(relative+"\n"), 0o600))
}

// The records of the worktrees name their .git files relative to the record, as git writes them under worktree.useRelativePaths. The test writes them
// by hand, so it runs on a git that predates the setting. The pinned runner still finds the worktree and skips a relative record whose folder is gone.
func TestGitexec_AWorktreeWithARelativeRecordRuns(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	ownerGit(t)

	l := newEngineLayout(t)
	gone := filepath.Join(l.root, "trees", "p", "gone")
	plainGit(t, l.clone, "worktree", "add", "-q", "--detach", gone, l.rev)
	relativeRecord(t, l, "item", l.tree)
	relativeRecord(t, l, "gone", gone)
	require.NoError(t, os.RemoveAll(gone))

	for _, dir := range []string{l.clone, l.tree} {
		out := gitexec.NewRepositories(l.repos).Runner(dir, 0).Run(t.Context(), []string{"status", "--porcelain"})
		require.Zero(t, out.ExitCode, "%s: %s", dir, out.Stderr)
	}

	repo, err := gitexec.NewRepositories(l.repos).Of(l.tree)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(l.clone, ".git", "worktrees", "item"), repo.GitDir)
}

func TestGitexec_AllowListCoversTheKeysThatCloneAndWorktreeAddWrite(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	ownerGit(t)

	l := newEngineLayout(t)
	tracked := filepath.Join(l.root, "trees", "p", "tracked")
	plainGit(t, l.clone, "worktree", "add", "-q", "-b", "tracked", tracked, "origin/main")
	plainGit(t, l.clone, "config", "user.name", "Local")
	plainGit(t, l.clone, "config", "user.email", "local@example.com")

	for _, dir := range []string{l.clone, l.tree, tracked} {
		out := gitexec.NewRepositories(l.repos).Runner(dir, 0).Run(t.Context(), []string{"status", "--porcelain"})
		assert.Zero(t, out.ExitCode, "%s: %s", dir, out.Stderr)
	}

	identity := gitexec.NewRepositories(l.repos).Runner(l.tree, 0).ReadConfig(t.Context(), "user.email")
	assert.Equal(t, "local@example.com", strings.TrimSpace(identity.Stdout))
}

func TestGitexec_KeepsTheOwnerGlobalConfig(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	global := ownerGit(t)
	l := newEngineLayout(t)
	l.program(t, "cat")

	plainGit(t, l.root, "config", "--file", global, "filter.evil.clean", l.prog)
	require.NoError(t, os.MkdirAll(filepath.Join(l.clone, ".git", "info"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(l.clone, ".git", "info", "attributes"), []byte("* filter=evil\n"), 0o600))
	// Git runs a clean filter on status only when the size matches the index and the mtime does not.
	require.NoError(t, os.WriteFile(filepath.Join(l.tree, "a.txt"), []byte("two\n"), 0o600))
	require.NoError(t, os.Chtimes(filepath.Join(l.tree, "a.txt"), time.Unix(0, 0), time.Unix(0, 0)))

	out := gitexec.NewRepositories(l.repos).Runner(l.tree, 0).Run(t.Context(), []string{"status", "--porcelain"})
	require.Zero(t, out.ExitCode, out.Stderr)

	assert.True(t, l.ran(), "the filter of the owner global config runs on an engine call")
}

func TestRepositories_AFolderThatNoCloneHoldsARecordOfIsUnknown(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	ownerGit(t)

	l := newEngineLayout(t)
	plain := filepath.Join(l.root, "plain")
	plainGit(t, l.root, "init", "-q", plain)

	twin := filepath.Join(l.clone, ".git", "worktrees", "twin")
	require.NoError(t, os.CopyFS(twin, os.DirFS(filepath.Join(l.clone, ".git", "worktrees", "item"))))

	pointed := filepath.Join(l.repos, "pointed")
	require.NoError(t, os.Mkdir(pointed, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(pointed, ".git"), []byte("gitdir: "+filepath.Join(plain, ".git")+"\n"), 0o600))

	cases := map[string]struct{ repos, dir string }{
		"a_repository_outside_the_clones":        {l.repos, plain},
		"a_folder_of_the_clones_with_a_git_file": {l.repos, pointed},
		"a_folder_that_does_not_exist":           {l.repos, filepath.Join(l.root, "missing")},
		"a_tree_with_two_records":                {l.repos, l.tree},
		"a_root_that_does_not_exist":             {filepath.Join(l.root, "none"), l.clone},
	}

	for name, c := range cases { //nolint:paralleltest // the parent test sets the environment of the process
		t.Run(name, func(t *testing.T) {
			out := gitexec.NewRepositories(c.repos).Runner(c.dir, 0).Run(t.Context(), []string{"status"})
			require.ErrorIs(t, out.SetupErr, gitexec.ErrUnknownRepository)
			assert.NotZero(t, out.ExitCode)
		})
	}
}

func TestRunner_ARepositoryWhoseSettingsCannotBeReadRunsNoGit(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	ownerGit(t)

	l := newEngineLayout(t)
	require.NoError(t, os.Remove(filepath.Join(l.clone, ".git", "config")))

	out := gitexec.NewRepositories(l.repos).Runner(l.clone, 0).Run(t.Context(), []string{"status"})

	assert.Contains(t, out.Stderr, "read the git settings of "+l.clone)
	assert.NotZero(t, out.ExitCode)
}
