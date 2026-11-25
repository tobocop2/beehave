package cli_test

import (
	"bytes"
	"context"
	"os"
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

// initRepo makes a folder named work that holds a git folder whose HEAD is head, and makes it the current folder.
func initRepo(t *testing.T, head string) string {
	t.Helper()

	repo := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte(head), 0o600))
	t.Chdir(repo)

	return repo
}

func runInit(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), append([]string{"init"}, args...), cli.BuildInfo{Registry: registry(), Version: "x"}, env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

// filesBelow lists the files below dir, as paths relative to dir.
func filesBelow(t *testing.T, dir string) []string {
	t.Helper()

	var found []string

	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		rel, relErr := filepath.Rel(dir, path)
		found = append(found, rel)

		return relErr
	}))
	sort.Strings(found)

	return found
}

func TestInit_WritesTheProjectFileIntoBeehaveConfigAndNowhereElse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	config := filepath.Join(t.TempDir(), "conf")
	repo := initRepo(t, "ref: refs/heads/main\n")

	code, _, stderr := runInit(t, map[string]string{"BEEHAVE_CONFIG": config})

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{initProject}, filesBelow(t, config))
	assert.Empty(t, filesBelow(t, home), "init writes nothing below the home folder")

	wantRepo := append([]string{".git/HEAD", initWorkflow}, initPrompts...)
	sort.Strings(wantRepo)
	assert.Equal(t, wantRepo, filesBelow(t, repo))
}

func TestInit_TheProjectFileIsAcceptedByCheckConfig(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	config := t.TempDir()
	writeConfig(t, config, "runners.star", validRunners)
	initRepo(t, "ref: refs/heads/main\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", config)
	require.Equal(t, 0, code, stderr)

	code, stdout, stderr := runCheckConfig(t, map[string]string{}, "--config", config)

	require.Equal(t, 0, code, stdout+stderr)
}

func TestInit_WritesTheWorkflowAndPromptsIntoTheRepository(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	example, err := filepath.Abs(filepath.Join("..", "..", "examples", "local"))
	require.NoError(t, err)

	repo := initRepo(t, "ref: refs/heads/main\n")

	code, stdout, stderr := runInit(t, map[string]string{}, "--config", t.TempDir())

	require.Equal(t, 0, code, stderr)

	for _, file := range append([]string{initWorkflow}, initPrompts...) {
		want, err := os.ReadFile(filepath.Join(example, file)) //nolint:gosec // a test reads files below the example folder
		require.NoError(t, err)

		got, err := os.ReadFile(filepath.Join(repo, file)) //nolint:gosec // a test reads files below its own temporary folder
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), file)
		assert.Contains(t, stdout, file)
	}

	assert.Contains(t, stdout, "--gate", "the output tells the user to set the gate")
}

func TestInit_RunFindsTheWrittenWorkflowWithNoFlag(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	rig := newRig(t)
	rig.useFile = false
	rig.dir = t.TempDir()
	writeConfig(t, rig.dir, "runners.star", validRunners)
	initRepo(t, "ref: refs/heads/main\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", rig.dir)
	require.Equal(t, 0, code, stderr)

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished\n", stdout)
	assert.Equal(t, []string{"12@work workflow.star"}, rig.runner.files)
}

func TestInit_RefusesWhenAnyTargetExistsAndWritesNothing(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	targets := append([]string{initProject, initWorkflow}, initPrompts...)

	for _, target := range targets { //nolint:paralleltest // the subtests change the folder of the process
		t.Run(target, func(t *testing.T) {
			config := t.TempDir()
			repo := initRepo(t, "ref: refs/heads/main\n")
			path := filepath.Join(repo, target)

			if target == initProject {
				path = filepath.Join(config, target)
			}

			writeConfig(t, filepath.Dir(path), filepath.Base(path), "mine\n")
			before := append(filesBelow(t, config), filesBelow(t, repo)...)

			code, _, stderr := runInit(t, map[string]string{}, "--config", config)

			require.NotEqual(t, 0, code)
			assert.Contains(t, stderr, path)
			assert.Contains(t, stderr, "no file was written")
			assert.Equal(t, before, append(filesBelow(t, config), filesBelow(t, repo)...))

			kept, err := os.ReadFile(path) //nolint:gosec // a test reads a file below its own temporary folder
			require.NoError(t, err)
			assert.Equal(t, "mine\n", string(kept))
		})
	}
}

func TestInit_NamesEveryExistingTarget(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	config := t.TempDir()
	repo := initRepo(t, "ref: refs/heads/main\n")
	writeConfig(t, repo, initWorkflow, "a\n")
	writeConfig(t, repo, initPrompts[0], "b\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", config)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, filepath.Join(repo, initWorkflow))
	assert.Contains(t, stderr, filepath.Join(repo, initPrompts[0]))
}

func TestInit_RefusesAFolderThatIsNotARepository(t *testing.T) {
	config := t.TempDir()
	dir := t.TempDir()
	t.Chdir(dir)

	code, _, stderr := runInit(t, map[string]string{}, "--config", config)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "run beehave init in the top folder of a git repository")
	assert.Empty(t, filesBelow(t, config))
	assert.Empty(t, filesBelow(t, dir))
}

func TestInit_AWorktreeFileCountsAsARepository(t *testing.T) {
	config := t.TempDir()
	gitdir := filepath.Join(t.TempDir(), "gitdir")
	require.NoError(t, os.MkdirAll(gitdir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(gitdir, "HEAD"), []byte("ref: refs/heads/side\n"), 0o600))

	repo := filepath.Join(t.TempDir(), "work")
	writeConfig(t, repo, ".git", "gitdir: "+gitdir+"\n")
	t.Chdir(repo)

	code, _, stderr := runInit(t, map[string]string{}, "--config", config)

	require.Equal(t, 0, code, stderr)

	project, err := os.ReadFile(filepath.Join(config, initProject)) //nolint:gosec // a test reads a file below its own temporary folder
	require.NoError(t, err)
	assert.Contains(t, string(project), `base_branch = "side"`)
}

func TestInit_AWorktreeFileWithNoGitdirLineIsRefused(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "work")
	writeConfig(t, repo, ".git", "nonsense\n")
	t.Chdir(repo)

	code, _, stderr := runInit(t, map[string]string{}, "--config", t.TempDir())

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "--base-branch")
}

func TestInit_ConfigFlagWinsOverTheVariable(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	flagDir := filepath.Join(t.TempDir(), "flag")
	variableDir := filepath.Join(t.TempDir(), "variable")
	initRepo(t, "ref: refs/heads/main\n")

	code, _, stderr := runInit(t, map[string]string{"BEEHAVE_CONFIG": variableDir}, "--config", flagDir)

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{initProject}, filesBelow(t, flagDir))
	assert.NoDirExists(t, variableDir)
}

func TestInit_FlagsReachTheProjectFile(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	config := t.TempDir()
	repo := initRepo(t, "ref: refs/heads/main\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", config, "--name", "other", "--base-branch", "trunk", "--gate", "just test", "--test-report", "out/*.xml")

	require.Equal(t, 0, code, stderr)

	project, err := os.ReadFile(filepath.Join(config, "projects", "other.star")) //nolint:gosec // a test reads a file below its own temporary folder
	require.NoError(t, err)
	assert.Equal(t, "project(\n    source = \""+repo+"\",\n    base_branch = \"trunk\",\n    sandbox = \"on\",\n    gate = \"just test\",\n    test_report = \"out/*.xml\",\n)\n", string(project))
}

func TestInit_AQuoteInTheGateStaysAValidFile(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	config := t.TempDir()
	writeConfig(t, config, "runners.star", validRunners)
	initRepo(t, "ref: refs/heads/main\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", config, "--gate", "make \"check\"\nnow")
	require.Equal(t, 0, code, stderr)

	code, stdout, stderr := runCheckConfig(t, map[string]string{}, "--config", config)

	require.Equal(t, 0, code, stdout+stderr)
}

func TestInit_RefusesAnUnusableName(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	for name, value := range map[string]string{ //nolint:paralleltest // the subtests change the folder of the process
		"a_path_out_of_the_folder": "../escape",
		"a_folder_inside":          "a/b",
		"a_leading_dot":            ".hidden",
		"a_space":                  "my repo",
	} {
		t.Run(name, func(t *testing.T) {
			config := t.TempDir()
			initRepo(t, "ref: refs/heads/main\n")

			code, _, stderr := runInit(t, map[string]string{}, "--config", config, "--name", value)

			require.NotEqual(t, 0, code)
			assert.Contains(t, stderr, "--name")
			assert.Empty(t, filesBelow(t, config))
		})
	}
}

func TestInit_RefusesAFolderNameThatIsNotAProjectName(t *testing.T) {
	config := t.TempDir()
	repo := filepath.Join(t.TempDir(), "my repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o700))
	t.Chdir(repo)

	code, _, stderr := runInit(t, map[string]string{}, "--config", config)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "--name")
	assert.Empty(t, filesBelow(t, config))
}

func TestInit_RefusesADetachedHead(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	config := t.TempDir()
	initRepo(t, "0123456789abcdef0123456789abcdef01234567\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", config)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "--base-branch")
	assert.Empty(t, filesBelow(t, config))
}

func TestInit_ARepositoryWithNoHeadFileIsRefusedAndNamesTheFlag(t *testing.T) {
	config := t.TempDir()
	repo := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o700))
	t.Chdir(repo)

	code, _, stderr := runInit(t, map[string]string{}, "--config", config)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "--base-branch")
}

func TestInit_ADetachedHeadIsAcceptedWhenTheBranchIsNamed(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	config := t.TempDir()
	initRepo(t, "0123456789abcdef0123456789abcdef01234567\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", config, "--base-branch", "main")

	require.Equal(t, 0, code, stderr)
}

func TestInit_BaseBranchDefaultsToTheCurrentBranch(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	config := t.TempDir()
	initRepo(t, "ref: refs/heads/master\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", config)

	require.Equal(t, 0, code, stderr)

	project, err := os.ReadFile(filepath.Join(config, initProject)) //nolint:gosec // a test reads a file below its own temporary folder
	require.NoError(t, err)
	assert.Contains(t, string(project), `base_branch = "master"`)
}

func TestInit_ReportsAWriteFailureAndTheFilesAlreadyWritten(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	config := t.TempDir()
	repo := initRepo(t, "ref: refs/heads/main\n")
	writeConfig(t, repo, "prompts", "a file where the prompts folder belongs\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", config)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "already written")
	assert.Contains(t, stderr, filepath.Join(config, initProject))
}

func TestInit_ReportsAConfigFolderThatCannotBeMade(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	initRepo(t, "ref: refs/heads/main\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", blocker)

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "make the folder")
	assert.NotContains(t, stderr, "already written")
}

func TestInit_AWorktreeFileWithARelativeGitdirFindsTheBranch(t *testing.T) {
	config := t.TempDir()
	repo := filepath.Join(t.TempDir(), "work")
	writeConfig(t, repo, ".git", "gitdir: gitdir\n")
	writeConfig(t, repo, "gitdir/HEAD", "ref: refs/heads/rel\n")
	t.Chdir(repo)

	code, _, stderr := runInit(t, map[string]string{}, "--config", config)

	require.Equal(t, 0, code, stderr)

	project, err := os.ReadFile(filepath.Join(config, initProject)) //nolint:gosec // a test reads a file below its own temporary folder
	require.NoError(t, err)
	assert.Contains(t, string(project), `base_branch = "rel"`)
}

func TestInit_AFileNameThatTheSystemRefusesIsReportedAfterNothingWasWritten(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	config := t.TempDir()
	initRepo(t, "ref: refs/heads/main\n")

	code, _, stderr := runInit(t, map[string]string{}, "--config", config, "--name", strings.Repeat("n", 300))

	require.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "create ")
	assert.NotContains(t, stderr, "already written")
}

func TestInit_WithNoHomeFolderAndNoConfigIsRefused(t *testing.T) {
	t.Setenv("HOME", "")
	initRepo(t, "ref: refs/heads/main\n")

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

func TestInit_AFailedWriteToTheOutputIsReported(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	for writes := range 2 { //nolint:paralleltest // the subtests change the folder of the process
		t.Run(strconv.Itoa(writes)+"_writes_succeed", func(t *testing.T) {
			initRepo(t, "ref: refs/heads/main\n")

			var stderr bytes.Buffer

			code := cli.Execute(context.Background(), []string{"init", "--config", t.TempDir()}, cli.BuildInfo{Registry: registry(), Version: "x"}, map[string]string{}, &failAfter{n: writes}, &stderr)

			require.NotEqual(t, 0, code)
			assert.Contains(t, stderr.String(), errFake.Error())
		})
	}
}

func TestInit_TakesNoArgument(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	initRepo(t, "ref: refs/heads/main\n")

	code, _, _ := runInit(t, map[string]string{}, "--config", t.TempDir(), "extra")

	require.NotEqual(t, 0, code)
}

func TestInit_HelpExitsZero(t *testing.T) { //nolint:paralleltest // the test changes the folder of the process
	code, stdout, _ := runInit(t, map[string]string{}, "--help")

	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "--base-branch")
}

func TestTemplate_EqualsExamplesLocal(t *testing.T) {
	t.Parallel()

	example := filepath.Join("..", "..", "examples", "local")
	copied := "initfiles"

	want := filesBelow(t, copied)
	expected := sortedCopy(append([]string{initWorkflow}, initPrompts...))
	assert.Equal(t, expected, want, "the template holds the workflow and the four prompts")

	for _, file := range want {
		original, err := os.ReadFile(filepath.Join(example, file)) //nolint:gosec // a test reads a file below its own temporary folder
		require.NoError(t, err)

		template, err := os.ReadFile(filepath.Join(copied, file)) //nolint:gosec // a test reads a file below its own temporary folder
		require.NoError(t, err)
		assert.Equal(t, string(original), string(template), "internal/cli/initfiles/%s differs from examples/local/%s: copy the example again", file, file)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)

	return out
}
