package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/cli"
)

const (
	initWorkflow = "workflow.star"
	initProject  = "projects/work.star"
)

var initPrompts = []string{"prompts/plan.md", "prompts/implement.md", "prompts/review.md", "prompts/fix.md"}

// git runs git in dir and fails the test when git fails.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // the test runs git with its own arguments
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// commit makes an empty commit, so that HEAD can be detached and a worktree can be added.
func commit(t *testing.T, dir string) {
	t.Helper()

	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "first")
}

// initRepo makes a git repository in a folder named work, on the branch, and makes it the current folder.
func initRepo(t *testing.T, branch string, initArgs ...string) string {
	t.Helper()

	repo := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	git(t, repo, append(append([]string{"init", "-q"}, initArgs...), "-b", branch)...)
	t.Chdir(repo)

	return repo
}

func runInit(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), append([]string{"init"}, args...), cli.BuildInfo{Registry: registry(), Version: "x"}, env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

// filesBelow lists the files below dir, as paths relative to dir, without the git folder.
func filesBelow(t *testing.T, dir string) []string {
	t.Helper()

	var found []string

	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, relErr := filepath.Rel(dir, path)
		if entry.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}

		if !entry.IsDir() {
			found = append(found, rel)
		}

		return relErr
	}))
	sort.Strings(found)

	return found
}

func readText(t *testing.T, path string) string {
	t.Helper()

	text, err := os.ReadFile(path) //nolint:gosec // a test reads a file below its own temporary folder
	require.NoError(t, err)

	return string(text)
}

func TestInit_WritesTheProjectFileIntoBeehaveConfigAndNowhereElse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	confDir := filepath.Join(t.TempDir(), "conf")
	repo := initRepo(t, "main")

	code, _, stderr := runInit(t, map[string]string{"BEEHAVE_CONFIG": confDir})

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{initProject}, filesBelow(t, confDir))
	assert.Empty(t, filesBelow(t, home), "init writes nothing below the home folder")

	wantRepo := append([]string{initWorkflow}, initPrompts...)
	sort.Strings(wantRepo)
	assert.Equal(t, wantRepo, filesBelow(t, repo))
}

func TestInit_TheProjectFileIsAcceptedByCheckConfig(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	confDir := t.TempDir()
	writeConfig(t, confDir, "runners.star", validRunners)
	initRepo(t, "main")

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)
	require.Equal(t, 0, code, stderr)

	code, stdout, stderr := runCheckConfig(t, map[string]string{}, "--config", confDir)

	require.Equal(t, 0, code, stdout+stderr)
}

func TestInit_WritesTheWorkflowAndPromptsIntoTheRepository(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	template, err := filepath.Abs("initfiles")
	require.NoError(t, err)

	repo := initRepo(t, "main")

	code, stdout, stderr := runInit(t, map[string]string{}, "--config", t.TempDir())

	require.Equal(t, 0, code, stderr)

	for _, file := range append([]string{initWorkflow}, initPrompts...) {
		assert.Equal(t, readText(t, filepath.Join(template, file)), readText(t, filepath.Join(repo, file)), file)
		assert.Contains(t, stdout, file)
	}

	assert.Contains(t, stdout, "--gate", "the output tells the user to set the gate")
}

func TestInit_TheFilesAndFoldersAreOnlyForTheUser(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	confDir := filepath.Join(t.TempDir(), "conf")
	repo := initRepo(t, "main")

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)
	require.Equal(t, 0, code, stderr)

	for path, want := range map[string]os.FileMode{
		confDir:                                  0o700,
		filepath.Join(confDir, "projects"):       0o700,
		filepath.Join(confDir, initProject):      0o600,
		filepath.Join(repo, "prompts"):           0o700,
		filepath.Join(repo, initWorkflow):        0o600,
		filepath.Join(repo, "prompts", "fix.md"): 0o600,
	} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, want, info.Mode().Perm(), path)
	}
}

func TestInit_RunFindsTheWrittenWorkflowWithNoFlag(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	rig := newRig(t)
	rig.useFile = false
	rig.dir = t.TempDir()
	writeConfig(t, rig.dir, "runners.star", validRunners)
	initRepo(t, "main")

	code, _, stderr := runInit(t, map[string]string{}, "--config", rig.dir)
	require.Equal(t, 0, code, stderr)

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished\n", stdout)
	assert.Equal(t, []string{"12@work workflow.star"}, rig.runner.files)
}

func TestInit_RefusesWhenAnyTargetExistsAndWritesNothing(t *testing.T) { //nolint:paralleltest // the subtests change the folder of the process
	targets := append([]string{initProject, initWorkflow}, initPrompts...)

	for _, target := range targets { //nolint:paralleltest // the subtests change the folder of the process
		t.Run(target, func(t *testing.T) {
			confDir := t.TempDir()
			repo := initRepo(t, "main")
			path := filepath.Join(repo, target)

			if target == initProject {
				path = filepath.Join(confDir, target)
			}

			writeConfig(t, filepath.Dir(path), filepath.Base(path), "mine\n")
			before := append(filesBelow(t, confDir), filesBelow(t, repo)...)

			code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)

			require.NotEqual(t, 0, code)
			assert.Contains(t, stderr, path)
			assert.Contains(t, stderr, "no file was written")
			assert.Equal(t, before, append(filesBelow(t, confDir), filesBelow(t, repo)...))
			assert.Equal(t, "mine\n", readText(t, path))
		})
	}
}

func TestInit_ALinkAtATargetIsAFileAlsoWhenItDanglesAndNothingIsWritten(t *testing.T) { //nolint:paralleltest // the subtests change the folder of the process
	for name, target := range map[string]string{"the_workflow": initWorkflow, "the_project_file": initProject} { //nolint:paralleltest // the subtests change the folder of the process
		t.Run(name, func(t *testing.T) {
			confDir := t.TempDir()
			repo := initRepo(t, "main")
			outside := filepath.Join(t.TempDir(), "outside")

			path := filepath.Join(repo, target)
			if target == initProject {
				path = filepath.Join(confDir, target)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			}

			require.NoError(t, os.Symlink(outside, path))

			code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)

			require.NotEqual(t, 0, code)
			assert.Contains(t, stderr, "no file was written")
			assert.NoFileExists(t, outside)
			assert.NoDirExists(t, filepath.Join(repo, "prompts"))

			if target == initWorkflow {
				assert.NoDirExists(t, filepath.Join(confDir, "projects"), "the config folder got no file")
			}
		})
	}
}

func TestInit_NamesEveryExistingTarget(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	confDir := t.TempDir()
	repo := initRepo(t, "main")
	writeConfig(t, repo, initWorkflow, "a\n")
	writeConfig(t, repo, initPrompts[0], "b\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, filepath.Join(repo, initWorkflow))
	assert.Contains(t, stderr, filepath.Join(repo, initPrompts[0]))
}

func TestInit_RefusesAFolderThatIsNotARepository(t *testing.T) {
	confDir := t.TempDir()
	dir := t.TempDir()
	t.Chdir(dir)

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "run beehave init in the top folder of a git repository")
	assert.Empty(t, filesBelow(t, confDir))
	assert.Empty(t, filesBelow(t, dir))
}

func TestInit_AWorktreeCountsAsARepositoryAndGivesItsBranch(t *testing.T) {
	confDir := t.TempDir()
	mainRepo := initRepo(t, "main")
	commit(t, mainRepo)

	worktree := filepath.Join(t.TempDir(), "work")
	git(t, mainRepo, "worktree", "add", "-q", "-b", "side", worktree)
	t.Chdir(worktree)

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, readText(t, filepath.Join(confDir, initProject)), `base_branch = "side"`)
}

func TestInit_ADotGitFolderThatGitCannotReadIsRefusedAndNamesTheFlag(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o700))
	t.Chdir(repo)

	code, _, stderr := runInit(t, map[string]string{}, "--config", t.TempDir())

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "--base-branch")
}

func TestInit_AReftableRepositoryGivesItsBranch(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	confDir := t.TempDir()
	initRepo(t, "trunk", "--ref-format=reftable")

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, readText(t, filepath.Join(confDir, initProject)), `base_branch = "trunk"`)
}

func TestInit_ConfigFlagWinsOverTheVariable(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	flagDir := filepath.Join(t.TempDir(), "flag")
	variableDir := filepath.Join(t.TempDir(), "variable")
	initRepo(t, "main")

	code, _, stderr := runInit(t, map[string]string{"BEEHAVE_CONFIG": variableDir}, "--config", flagDir)

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{initProject}, filesBelow(t, flagDir))
	assert.NoDirExists(t, variableDir)
}

func TestInit_FlagsReachTheProjectFile(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	confDir := t.TempDir()
	repo := initRepo(t, "main")

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir, "--name", "other", "--base-branch", "trunk", "--gate", "just test", "--test-report", "out/*.xml")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "project(\n    source = \""+repo+"\",\n    base_branch = \"trunk\",\n    sandbox = \"on\",\n    gate = \"just test\",\n    test_report = \"out/*.xml\",\n)\n",
		readText(t, filepath.Join(confDir, "projects", "other.star")))
}

func TestInit_RefusesAnEmptyGateOrTestReportWithTheWordsOfCheckConfig(t *testing.T) { //nolint:paralleltest // the subtests change the folder of the process
	for name, args := range map[string][]string{ //nolint:paralleltest // the subtests change the folder of the process
		"an_empty_gate":        {"--gate", ""},
		"a_blank_gate":         {"--gate", "  "},
		"an_empty_test_report": {"--test-report", ""},
	} {
		t.Run(name, func(t *testing.T) {
			confDir := t.TempDir()
			initRepo(t, "main")

			code, _, stderr := runInit(t, map[string]string{}, append([]string{"--config", confDir}, args...)...)

			require.NotEqual(t, 0, code)
			assert.Contains(t, stderr, "`"+strings.ReplaceAll(strings.TrimPrefix(args[0], "--"), "-", "_")+"` must be a non-empty string. Set it")
			assert.Empty(t, filesBelow(t, confDir))
		})
	}
}

func TestInit_RefusesAnUnusableName(t *testing.T) { //nolint:paralleltest // the subtests change the folder of the process
	for name, value := range map[string]string{ //nolint:paralleltest // the subtests change the folder of the process
		"a_path_out_of_the_folder": "../escape",
		"a_folder_inside":          "a/b",
		"a_leading_dot":            ".hidden",
		"a_space":                  "my repo",
	} {
		t.Run(name, func(t *testing.T) {
			confDir := t.TempDir()
			initRepo(t, "main")

			code, _, stderr := runInit(t, map[string]string{}, "--config", confDir, "--name", value)

			require.NotEqual(t, 0, code)
			assert.Contains(t, stderr, "--name")
			assert.Empty(t, filesBelow(t, confDir))
		})
	}
}

func TestInit_RefusesAFolderNameThatIsNotAProjectName(t *testing.T) {
	confDir := t.TempDir()
	repo := filepath.Join(t.TempDir(), "my repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	git(t, repo, "init", "-q", "-b", "main")
	t.Chdir(repo)

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "--name")
	assert.Empty(t, filesBelow(t, confDir))
}

func TestInit_RefusesADetachedHead(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	confDir := t.TempDir()
	repo := initRepo(t, "main")
	commit(t, repo)
	git(t, repo, "checkout", "-q", "--detach")

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "--base-branch")
	assert.Empty(t, filesBelow(t, confDir))
}

func TestInit_ADetachedHeadIsAcceptedWhenTheBranchIsNamed(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	confDir := t.TempDir()
	repo := initRepo(t, "main")
	commit(t, repo)
	git(t, repo, "checkout", "-q", "--detach")

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir, "--base-branch", "main")

	require.Equal(t, 0, code, stderr)
}

func TestInit_BaseBranchDefaultsToTheCurrentBranch(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	confDir := t.TempDir()
	initRepo(t, "master")

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, readText(t, filepath.Join(confDir, initProject)), `base_branch = "master"`)
}

func TestInit_ReportsAWriteFailureAndTheFilesAlreadyWritten(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	confDir := t.TempDir()
	repo := initRepo(t, "main")
	writeConfig(t, repo, "prompts", "a file where the prompts folder belongs\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "already written")
	assert.Contains(t, stderr, filepath.Join(confDir, initProject))
}

func TestInit_ReportsAConfigFolderThatCannotBeMade(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	initRepo(t, "main")

	code, _, stderr := runInit(t, map[string]string{}, "--config", blocker)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "make the folder")
	assert.NotContains(t, stderr, "already written")
}

func TestInit_AFileNameThatTheSystemRefusesIsReportedAfterNothingWasWritten(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	confDir := t.TempDir()
	initRepo(t, "main")

	code, _, stderr := runInit(t, map[string]string{}, "--config", confDir, "--name", strings.Repeat("n", 300))

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "create ")
	assert.NotContains(t, stderr, "already written")
}

func TestInit_WithNoHomeFolderAndNoConfigIsRefused(t *testing.T) {
	t.Setenv("HOME", "")
	initRepo(t, "main")

	code, _, stderr := runInit(t, map[string]string{})

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "HOME")
}

// failAfter is a writer that accepts the first n writes and then fails.
type failAfter struct{ n int }

func (w *failAfter) Write(p []byte) (int, error) {
	if w.n == 0 {
		return 0, errFake
	}

	w.n--

	return len(p), nil
}

func TestInit_AFailedWriteToTheOutputIsReported(t *testing.T) { //nolint:paralleltest // the subtests change the folder of the process
	for writes := range 2 { //nolint:paralleltest // the subtests change the folder of the process
		t.Run(strconv.Itoa(writes)+"_writes_succeed", func(t *testing.T) {
			initRepo(t, "main")

			var stderr bytes.Buffer

			code := cli.Execute(context.Background(), []string{"init", "--config", t.TempDir()}, cli.BuildInfo{Registry: registry(), Version: "x"}, map[string]string{}, &failAfter{n: writes}, &stderr)

			require.NotEqual(t, 0, code)
			assert.Contains(t, stderr.String(), errFake.Error())
		})
	}
}

func TestInit_TakesNoArgument(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	initRepo(t, "main")

	code, _, _ := runInit(t, map[string]string{}, "--config", t.TempDir(), "extra")

	require.NotEqual(t, 0, code)
}

func TestInit_HelpExitsZero(t *testing.T) {
	t.Parallel()

	code, stdout, _ := runInit(t, map[string]string{}, "--help")

	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "--base-branch")
}
