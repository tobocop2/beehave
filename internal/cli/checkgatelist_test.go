package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ci/githubactions"
	"github.com/tobocop2/beehave/internal/cli"
)

const (
	ciWorkflow = `name: CI
permissions:
  contents: read
jobs:
  check:
    steps:
      - uses: actions/checkout@v7
      - name: Format and vet
        run: make format-check vet
      - run: |
          go install gotest.tools/gotestsum@v1.13.0
          make test
  docs:
    steps:
      - name: Build the docs
        run: mkdocs build --strict
`

	jobsHead = "permissions: read-all\njobs:\n  ship:\n    steps:\n      - name: Test\n        run: make test\n      - "
)

// runGateList runs beehave check gate-list over the real GitHub Actions reader and returns the exit code and the two outputs.
func runGateList(t *testing.T, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	info := cli.BuildInfo{Registry: registry(), Version: "x", GateList: githubactions.Derive}
	code := cli.Execute(context.Background(), append([]string{"check", "gate-list"}, args...), info, map[string]string{}, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func ciFile(t *testing.T, name, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)

	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

func TestCheckGateList_TheStepsAreDerivedFromTheCIWorkflowFile(t *testing.T) {
	t.Parallel()

	code, out, errOut := runGateList(t, ciFile(t, "ci.yaml", ciWorkflow))

	assert.Equal(t, 0, code)
	assert.Equal(t, "step check:format-and-vet: make format-check vet\n"+
		"step check:run-3: go install gotest.tools/gotestsum@v1.13.0; make test\n"+
		"step docs:build-the-docs: mkdocs build --strict\n"+
		"check-gatelist: OK (steps 3, refused 0)\n", out)
	assert.Empty(t, errOut)
}

func TestCheckGateList_TheStepsOfSeveralFilesAreOneListWithNoNameTwice(t *testing.T) {
	t.Parallel()

	code, out, _ := runGateList(t, ciFile(t, "a.yaml", jobsHead+"run: make a\n"), ciFile(t, "b.yaml", jobsHead+"run: make b\n"))

	assert.Equal(t, 0, code)
	assert.Contains(t, out, "step ship:test: make test\n")
	assert.Contains(t, out, "step ship:run-2: make a\n")
	assert.Contains(t, out, "step ship:test-2: make test\n")
	assert.Contains(t, out, "step ship:run-2-2: make b\n")
	assert.Contains(t, out, "check-gatelist: OK (steps 4, refused 0)\n")
}

func TestCheckGateList_AStepThatTheLabelRefusesIsListedWithItsReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, step, reason string
	}{
		{"a_release_by_action", "uses: softprops/action-gh-release@v2", "uses softprops/action-gh-release@v2, an action that is neither a checkout nor a setup"},
		{"a_push_by_command", "run: cd out && git push origin main", "runs `git push`"},
		{"a_shell_that_is_not_sh", "shell: pwsh\n        run: echo hi", "runs in the shell pwsh"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			code, out, errOut := runGateList(t, ciFile(t, "ci.yaml", jobsHead+test.step+"\n"))

			assert.Equal(t, 0, code)
			assert.Empty(t, errOut)
			assert.Contains(t, out, "step ship:test: make test\n")
			assert.Contains(t, out, "refused step ship:run-2: ")
			assert.Contains(t, out, test.reason)
			assert.Contains(t, out, "check-gatelist: OK (steps 1, refused 1)\n")
		})
	}
}

func TestCheckGateList_TheMapMustNameEveryDerivedCommandExactly(t *testing.T) {
	t.Parallel()

	file := ciFile(t, "ci.yaml", jobsHead+"run: make lint\n")
	steps := []string{"--steps", "test", "--steps", "lint", "--map"}

	code, out, errOut := runGateList(t, append(steps, `{"make test": ["test"], "make lint": ["lint"]}`, file)...)

	assert.Equal(t, 0, code)
	assert.Contains(t, out, "check-gatelist: OK (steps 2, refused 0)")
	assert.Empty(t, errOut)

	for name, held := range map[string]string{
		"no key":           `{"make test": ["test"]}`,
		"a substring key":  `{"make test": ["test"], "make lin": ["lint"]}`,
		"a trailing space": `{"make test": ["test"], "make lint ": ["lint"]}`,
	} {
		code, out, errOut = runGateList(t, append(steps, held, file)...)

		assert.Equal(t, 2, code, name)
		assert.Contains(t, out, "step ship:run-2: make lint\n", name)
		assert.Contains(t, out, "unmapped ship:run-2: make lint\n", name)
		assert.Contains(t, errOut, "the gate_map of the project does not match the CI commands", name)
	}
}

func TestCheckGateList_AMapThatNamesAnUnknownGateStepOrAStaleCommandIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	file := ciFile(t, "ci.yaml", jobsHead+"run: make lint\n")

	code, out, _ := runGateList(t, "--steps", "test", "--map", `{"make test": ["test"], "make lint": ["lint"]}`, file)

	assert.Equal(t, 2, code)
	assert.Contains(t, out, "unknown-step lint\n")
	assert.NotContains(t, out, "unmapped ")

	code, out, _ = runGateList(t, "--steps", "test", "--map", `{"make test": ["test"], "make lint": [], "make old": []}`, file)

	assert.Equal(t, 2, code)
	assert.Contains(t, out, "stale make old\n")
	assert.NotContains(t, out, "unknown-step ")
}

func TestCheckGateList_AMapWithNoGateStepsIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	code, out, errOut := runGateList(t, "--map", `{"make test": ["test"]}`, ciFile(t, "ci.yaml", jobsHead+"run: make lint\n"))

	assert.Equal(t, 2, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "the project has no gate_steps")
}

func TestCheckGateList_AStepsFlagWithNoMapHoldsEachCommandToAnEmptyMap(t *testing.T) {
	t.Parallel()

	code, out, _ := runGateList(t, "--steps", "test", ciFile(t, "ci.yaml", jobsHead+"run: make lint\n"))

	assert.Equal(t, 2, code)
	assert.Contains(t, out, "unmapped ship:test: make test\n")
	assert.Contains(t, out, "unmapped ship:run-2: make lint\n")
}

func TestCheckGateList_AMapThatIsNotAnObjectOfListsOfTextIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	for _, held := range []string{`["make test"]`, `{"make test": "test"}`, `{"make test": [1]}`, `{`} {
		code, out, errOut := runGateList(t, "--steps", "test", "--map", held, ciFile(t, "ci.yaml", ciWorkflow))

		assert.Equal(t, 2, code, held)
		assert.Empty(t, out, held)
		assert.Contains(t, errOut, "invalid --map: ", held)
	}
}

func TestCheckGateList_AMapThatNamesACommandTwiceHoldsANullListOrIsGivenTwiceIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	file := ciFile(t, "ci.yaml", jobsHead+"run: make lint\n")
	good := `{"make test": ["test"], "make lint": []}`

	for name, args := range map[string][]string{
		"a key twice": {"--map", `{"make test": ["nope"], "make test": ["test"], "make lint": []}`},
		"a null list": {"--map", `{"make test": null, "make lint": []}`},
	} {
		code, out, errOut := runGateList(t, append(append([]string{"--steps", "test"}, args...), file)...)

		assert.Equal(t, 2, code, name)
		assert.Empty(t, out, name)
		assert.Contains(t, errOut, "invalid --map: ", name)
	}

	code, out, errOut := runGateList(t, "--steps", "test", "--map", `{"make test": ["test"]}`, "--map", `{"make lint": []}`, file)

	assert.Equal(t, 2, code, "two maps")
	assert.Empty(t, out)
	assert.Contains(t, errOut, "--map is given twice")

	code, _, errOut = runGateList(t, "--steps", "test", "--map", good, file)

	assert.Equal(t, 0, code, "the control: the same map once and whole")
	assert.Empty(t, errOut)
}

func TestCheckGateList_AFlagThatDoesNotExistIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	code, out, errOut := runGateList(t, "--nope", ciFile(t, "ci.yaml", ciWorkflow))

	assert.Equal(t, 2, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "unknown flag: --nope")
}

func TestCheckGateList_AFileThatGivesNoUsableStepIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"not_yaml":                "jobs: [unclosed\n",
		"no_run_step":             "permissions: read-all\njobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n",
		"a_run_that_is_not_shell": "permissions: read-all\njobs:\n  a:\n    steps:\n      - run: if then fi )\n",
		"only_a_push":             "permissions: read-all\njobs:\n  a:\n    steps:\n      - run: git push origin main\n",
		"only_a_job_that_writes":  "jobs:\n  a:\n    steps:\n      - run: make test\n",
		"no_jobs":                 "name: CI\n",
	}

	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code, out, errOut := runGateList(t, ciFile(t, "ci.yaml", text))

			assert.Equal(t, 2, code)
			assert.Empty(t, out)
			assert.Contains(t, errOut, "beehave check gate-list: FAIL: ")
		})
	}
}

func TestCheckGateList_AMissingFileAndAFileWithANulByteAreExitCodeTwo(t *testing.T) {
	t.Parallel()

	code, out, errOut := runGateList(t, filepath.Join(t.TempDir(), "none.yaml"))

	assert.Equal(t, 2, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "cannot read ")
	assert.Contains(t, errOut, "Check that the file exists and that you can read it.\n")

	code, out, errOut = runGateList(t, ciFile(t, "nul.yaml", "jobs:\x00\n"))

	assert.Equal(t, 2, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "holds a NUL byte. Rewrite it as plain text.\n")
}

func TestCheckGateList_HelpIsExitCodeZeroAndShowsTheUsage(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"--help", "-h"} {
		code, out, _ := runGateList(t, flag)

		assert.Equal(t, 0, code, flag)
		assert.Contains(t, out, "gate-list [--steps <name>]... [--map <json>] <ci-file>...", flag)
	}
}

func TestCheckGateList_AFlagMayFollowTheFile(t *testing.T) {
	t.Parallel()

	file := ciFile(t, "ci.yaml", jobsHead+"run: make lint\n")

	code, out, errOut := runGateList(t, file, "--steps", "test", "--map", `{"make test": ["test"], "make lint": []}`)

	assert.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "check-gatelist: OK")
}

func TestCheckGateList_ALongFlagWithASingleDashIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	code, out, errOut := runGateList(t, "-steps", "test", ciFile(t, "ci.yaml", ciWorkflow))

	assert.Equal(t, 2, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "unknown shorthand flag")
}

func TestCheckGateList_ACallWithNoCIFileShowsTheUsageAndIsExitCodeTwo(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{}, {"--steps", "x"}} {
		code, out, errOut := runGateList(t, args...)

		assert.Equal(t, 2, code, args)
		assert.Empty(t, out, args)
		assert.Equal(t, "usage: beehave check gate-list [--steps <name>]... [--map <json>] <ci-file>...\n", errOut, args)
	}
}

func TestCheckGateList_ABuildWithNoGateListReaderLeavesTheCommandOut(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"check", "--help"}, cli.BuildInfo{Registry: registry(), Version: "x"}, map[string]string{}, &stdout, &stderr)

	assert.Equal(t, 0, code)
	assert.Contains(t, stdout.String(), "workflow")
	assert.NotContains(t, stdout.String(), "gate-list")

	stdout.Reset()

	code = cli.Execute(context.Background(), []string{"check", "--help"}, cli.BuildInfo{Registry: registry(), Version: "x", GateList: githubactions.Derive}, map[string]string{}, &stdout, &stderr)

	assert.Equal(t, 0, code)
	assert.Contains(t, stdout.String(), "gate-list")
}
