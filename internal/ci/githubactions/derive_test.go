package githubactions_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ci"
	"github.com/tobocop2/beehave/internal/ci/githubactions"
)

func derivedAll(t *testing.T, text string) ci.Derived {
	t.Helper()

	got, err := githubactions.Derive([]ci.File{{Path: "ci.yaml", Text: text}})
	require.NoError(t, err)

	return got
}

func derived(t *testing.T, text string) []ci.Step {
	t.Helper()

	return derivedAll(t, text).Steps
}

func refusedDerive(t *testing.T, text string) string {
	t.Helper()

	_, err := githubactions.Derive([]ci.File{{Path: "ci.yaml", Text: text}})

	var refusal *ci.Refusal

	require.ErrorAs(t, err, &refusal)

	return refusal.Message
}

// stepNames gives the names of the derived steps.
func stepNames(got ci.Derived) []string {
	names := make([]string, len(got.Steps))
	for i, step := range got.Steps {
		names[i] = step.Name
	}

	return names
}

// refusedNames gives the names of the refused steps and jobs.
func refusedNames(got ci.Derived) []string {
	names := make([]string, len(got.Refused))
	for i, r := range got.Refused {
		names[i] = r.Name
	}

	return names
}

func TestDerive_AStepIsNamedByItsJobAndItsNameOrItsPlaceInTheJob(t *testing.T) {
	t.Parallel()

	steps := derived(t, readOnlyFile+"jobs:\n  b:\n    steps:\n      - run: one\n      - name: '***'\n        run: two\n      - name: Run  the Tests!\n        run: three\n")

	assert.Equal(t, []ci.Step{{Name: "b:run-1", Command: "one"}, {Name: "b:run-2", Command: "two"}, {Name: "b:run-the-tests", Command: "three"}}, steps)
}

func TestDerive_TheJobsAreInTheOrderOfTheFileAndTheirNamesAreNotSortedByTheMap(t *testing.T) {
	t.Parallel()

	steps := derived(t, readOnlyFile+"jobs:\n  zeta:\n    steps:\n      - run: z\n  alpha:\n    steps:\n      - run: a\n")

	assert.Equal(t, []ci.Step{{Name: "zeta:run-1", Command: "z"}, {Name: "alpha:run-1", Command: "a"}}, steps)
}

func TestDerive_ACommandOfSeveralLinesIsOneStepWithoutBlankLinesOrComments(t *testing.T) {
	t.Parallel()

	steps := derived(t, readOnlyFile+"jobs:\n  a:\n    steps:\n      - run: |\n          # install\n          go install x \\\n            -v\n\n          make   test\n")

	assert.Equal(t, []ci.Step{{Name: "a:run-1", Command: "go install x -v; make test"}}, steps)
}

func TestDerive_ARunStepWithNoCommandAndAStepWithNoRunAddNoStep(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, readOnlyFile+"jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n      - run: \"\"\n      - run: make test\n")

	assert.Equal(t, []ci.Step{{Name: "a:run-3", Command: "make test"}}, got.Steps)
	assert.Empty(t, got.Refused)
}

func TestDerive_AnExpressionOfTheWorkflowInACommandDoesNotStopTheRead(t *testing.T) {
	t.Parallel()

	steps := derived(t, readOnlyFile+"jobs:\n  a:\n    steps:\n      - run: make test SLICE=${{ matrix.slice }}\n")

	assert.Equal(t, []ci.Step{{Name: "a:run-1", Command: "make test SLICE=${{ matrix.slice }}"}}, steps)
}

func TestDerive_ATwoStepsOfOneNameGetANumber(t *testing.T) {
	t.Parallel()

	steps := derived(t, readOnlyFile+"jobs:\n  a:\n    steps:\n      - name: Test\n        run: one\n      - name: Test\n        run: two\n      - name: Test\n        run: three\n")

	assert.Equal(t, []string{"a:test", "a:test-2", "a:test-3"}, []string{steps[0].Name, steps[1].Name, steps[2].Name})
}

func TestDerive_AFileThatIsNotAWorkflowIsRefusedWithItsPathAndTheReason(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ text, want string }{
		"not_yaml":            {"jobs: [unclosed\n", "ci.yaml: not a workflow file"},
		"jobs_is_a_list":      {"jobs:\n  - a\n", "jobs must be a mapping of job ids to jobs"},
		"a_job_is_a_text":     {"jobs:\n  a: text\n", "job a:"},
		"a_step_is_a_text":    {"jobs:\n  a:\n    steps:\n      - text\n", "job a:"},
		"no_run_step":         {readOnlyFile + "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n", "give no run step"},
		"no_jobs":             {"name: CI\n", "give no run step"},
		"only_a_refused_step": {readOnlyFile + "jobs:\n  a:\n    steps:\n      - name: Ship\n        run: git push origin main\n", "step a:ship runs `git push`"},
	} {
		assert.Contains(t, refusedDerive(t, test.text), test.want, name)
	}
}

func TestDerive_AStepThatTheListCannotHoldIsNamedWithItsReasonAndTheOtherStepsStay(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, readOnlyFile+"jobs:\n  a:\n    steps:\n      - name: Bad\n        run: if then fi )\n      - name: Lint\n        uses: golangci/golangci-lint-action@v8\n      - run: make test\n")

	assert.Equal(t, []ci.Step{{Name: "a:run-3", Command: "make test"}}, got.Steps)
	require.Len(t, got.Refused, 2)
	assert.Equal(t, "step a:bad", got.Refused[0].Name)
	assert.Contains(t, got.Refused[0].Reason, "(unparsed)")
	assert.Equal(t, "step a:lint", got.Refused[1].Name)
	assert.Contains(t, got.Refused[1].Reason, "uses golangci/golangci-lint-action@v8, an action that is neither a checkout nor a setup")
}

func TestDerive_EveryFileIsReadAndEveryDefectIsNamed(t *testing.T) {
	t.Parallel()

	_, err := githubactions.Derive([]ci.File{
		{Path: "a.yaml", Text: "jobs: [x\n"},
		{Path: "b.yaml", Text: "jobs: [y\n"},
	})

	var refusal *ci.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, refusal.Message, "a.yaml: not a workflow file")
	assert.Contains(t, refusal.Message, "b.yaml: not a workflow file")
}

func TestDerive_AJobThatCanWriteOrReadsASecretIsLeftOutWhole(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"job_write":     "jobs:\n  rel:\n    permissions:\n      contents: write\n    steps:\n      - run: make test\n  t:\n    steps:\n      - run: make test\n",
		"job_write_all": "jobs:\n  rel:\n    permissions: write-all\n    steps:\n      - run: make test\n  t:\n    steps:\n      - run: make test\n",
		"job_env":       "jobs:\n  rel:\n    env:\n      TOKEN: ${{ secrets.TOKEN }}\n    steps:\n      - run: make test\n  t:\n    steps:\n      - run: make test\n",
	} {
		got, err := githubactions.Derive([]ci.File{{Path: "w.yaml", Text: readOnlyFile + text}})

		require.NoError(t, err, name)
		assert.Equal(t, []ci.Step{{Name: "t:run-1", Command: "make test"}}, got.Steps, name)
		require.Len(t, got.Refused, 1, name)
		assert.Equal(t, "job w.yaml:rel", got.Refused[0].Name, name)
	}
}

func TestDerive_AFileThatCanWriteGivesNoStep(t *testing.T) {
	t.Parallel()

	message := refusedDerive(t, "permissions:\n  id-token: write\njobs:\n  t:\n    steps:\n      - run: make test\n")

	assert.Contains(t, message, "job ci.yaml:t can write")
}

func TestDerive_ARepeatedStepNameFromAnotherFileStaysUnique(t *testing.T) {
	t.Parallel()

	got, err := githubactions.Derive([]ci.File{
		{Path: "a.yaml", Text: readOnlyFile + "jobs:\n  t:\n    steps:\n      - run: one\n"},
		{Path: "b.yaml", Text: readOnlyFile + "jobs:\n  t:\n    steps:\n      - run: two\n"},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"t:run-1", "t:run-1-2"}, []string{got.Steps[0].Name, got.Steps[1].Name})
}

func TestDerive_ASecretInTheWorkflowEnvOrDefaultsLeavesOutEveryJobOfTheFile(t *testing.T) {
	t.Parallel()

	jobs := "jobs:\n  a:\n    steps:\n      - run: go vet ./...\n  b:\n    steps:\n      - run: make test\n"

	assert.Equal(t, []string{"a:run-1", "b:run-1"}, stepNames(derivedAll(t, readOnlyFile+"env:\n  MODE: plain\n"+jobs)), "the control: a file env with no secret keeps both jobs")

	for name, top := range map[string]string{
		"file_env":      "env:\n  TOKEN: ${{ secrets.T }}\n",
		"file_defaults": "defaults:\n  run:\n    working-directory: ${{ secrets.DIR }}\n",
	} {
		message := refusedDerive(t, readOnlyFile+top+jobs)

		assert.Contains(t, message, "job ci.yaml:a reads a secret", name)
		assert.Contains(t, message, "job ci.yaml:b reads a secret", name)
	}
}

func TestDerive_TheTokenOfTheRunCountsAsASecret(t *testing.T) {
	t.Parallel()

	envStep := func(value string) string {
		return readOnlyFile + "jobs:\n  j:\n    steps:\n      - env:\n          T: " + value + "\n        run: go vet ./...\n" + keptJob
	}

	for name, value := range map[string]string{
		"dot form":          "${{ github.token }}",
		"index form":        "${{ github['token'] }}",
		"index with space":  `${{ github [ "token" ] }}`,
		"upper case":        "${{ GitHub.Token }}",
		"space at the dot":  "${{ github . token }}",
		"json of the store": "${{ toJSON(secrets) }}",
		"json of github":    "${{ toJSON(github) }}",
		"format of github":  "${{ format('{0}', github) }}",
	} {
		got := derivedAll(t, envStep(value))

		assert.Equal(t, []string{"k:run-1"}, stepNames(got), name)
		require.Equal(t, []string{"job ci.yaml:j"}, refusedNames(got), name)
		assert.Equal(t, "reads a secret", got.Refused[0].Reason, name)
	}

	for name, value := range map[string]string{
		"another field of github": "${{ github.ref }}",
		"a repository path":       "github.com/o/r",
	} {
		assert.Equal(t, []string{"j:run-1", "k:run-1"}, stepNames(derivedAll(t, envStep(value))), name)
	}

	message := refusedDerive(t, readOnlyFile+"env:\n  GH_TOKEN: ${{ github.token }}\njobs:\n  j:\n    steps:\n      - run: go vet ./...\n")
	assert.Contains(t, message, "job ci.yaml:j reads a secret", "the token in the file env leaves out the job")
}

func TestDerive_AJobWithNoPermissionsKeyCountsAsWriteUnlessTheFileIsReadOnly(t *testing.T) {
	t.Parallel()

	jobs := "jobs:\n  j:\n    steps:\n      - run: go vet ./...\n"

	for name, top := range map[string]string{
		"read-all":       "permissions: read-all\n",
		"an empty map":   "permissions: {}\n",
		"read and none":  "permissions:\n  contents: read\n  packages: none\n",
		"a quoted value": "permissions:\n  contents: 'read'\n",
	} {
		assert.Equal(t, []string{"j:run-1"}, stepNames(derivedAll(t, top+jobs)), name)
	}

	for name, top := range map[string]string{
		"no key":                       "",
		"write-all":                    "permissions: write-all\n",
		"one write":                    "permissions:\n  contents: read\n  packages: write\n",
		"an expression":                "permissions:\n  contents: ${{ inputs.p }}\n",
		"an unknown word":              "permissions: read-some\n",
		"an expression as a whole key": "permissions: ${{ inputs.p }}\n",
		"a list":                       "permissions: [read]\n",
		"a null value":                 "permissions:\n",
	} {
		assert.Contains(t, refusedDerive(t, top+jobs), "job ci.yaml:j can write", name)
	}
}

func TestDerive_AJobPermissionsKeyOverridesTheFile(t *testing.T) {
	t.Parallel()

	readUnderWrite := derivedAll(t, "permissions: write-all\njobs:\n  a:\n    permissions:\n      contents: read\n    steps:\n      - run: go vet ./...\n  b:\n    steps:\n      - run: make test\n")

	assert.Equal(t, []string{"a:run-1"}, stepNames(readUnderWrite), "the job key reads under a file that writes")
	assert.Equal(t, []string{"job ci.yaml:b"}, refusedNames(readUnderWrite), "the job with no key takes the file key")

	writeUnderRead := derivedAll(t, "permissions: read-all\njobs:\n  a:\n    permissions:\n      contents: write\n    steps:\n      - run: go vet ./...\n  b:\n    steps:\n      - run: make test\n")

	assert.Equal(t, []string{"b:run-1"}, stepNames(writeUnderRead), "the job with no key takes the file key")
	assert.Equal(t, []string{"job ci.yaml:a"}, refusedNames(writeUnderRead), "the job key writes under a file that reads")
}

func TestDerive_AShellOtherThanBashOrShFromTheStepOrTheDefaultsLeavesTheStepOut(t *testing.T) {
	t.Parallel()

	harmless := "      - run: echo hi\n"
	harmlessIn := func(shell string) string { return "      - shell: " + shell + "\n        run: echo hi\n" }
	// The last step names bash, so it stays a step under any default and the file gives a step.
	jobOf := func(top, defaults, step string) string {
		return readOnlyFile + top + "jobs:\n  j:\n" + defaults + "    steps:\n" + step + "      - shell: bash\n        run: make test\n"
	}
	jobDefaults := func(shell string) string { return "    defaults:\n      run:\n        shell: " + shell + "\n" }
	fileDefaults := func(shell string) string { return "defaults:\n  run:\n    shell: " + shell + "\n" }

	for name, text := range map[string]string{
		"step pwsh":          jobOf("", "", harmlessIn("pwsh")),
		"step custom string": jobOf("", "", harmlessIn("bash -e {0}")),
		"step expression":    jobOf("", "", harmlessIn("${{ matrix.shell }}")),
		"job defaults pwsh":  jobOf("", jobDefaults("pwsh"), harmless),
		"file defaults ruby": jobOf(fileDefaults("ruby {0}"), "", harmless),
	} {
		got := derivedAll(t, text)

		assert.Equal(t, []string{"j:run-2"}, stepNames(got), name)
		require.Equal(t, []string{"step j:run-1"}, refusedNames(got), name)
		assert.Contains(t, got.Refused[0].Reason, "runs in the shell ", name)
	}

	for name, text := range map[string]string{
		"step bash":                    jobOf("", "", harmlessIn("bash")),
		"step sh":                      jobOf("", "", harmlessIn("sh")),
		"step sh over job pwsh":        jobOf("", jobDefaults("pwsh"), harmlessIn("sh")),
		"job defaults bash over pwsh":  jobOf(fileDefaults("pwsh"), jobDefaults("bash"), harmless),
		"no shell at any level stands": jobOf("", "", harmless),
	} {
		got := derivedAll(t, text)

		assert.Equal(t, []string{"j:run-1", "j:run-2"}, stepNames(got), name)
		assert.Empty(t, got.Refused, name)
	}
}

func TestDerive_TheRunTextIsJudgedOnlyByTheExistingShellKinds(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ command, reason string }{
		"git-write":      {"git push origin main", "(git-write)"},
		"opaque-command": {"$CMD add -A", "(opaque-command)"},
		"unparsed":       {"if then fi )", "(unparsed)"},
	} {
		got := derivedAll(t, formRun(test.command))

		require.Equal(t, []string{"step j:run-1"}, refusedNames(got), name)
		assert.Contains(t, got.Refused[0].Reason, test.reason, name)
	}

	for _, command := range []string{"gh release create v1", "npm publish", "docker push ghcr.io/o/r:1", "curl -XPUT https://x/a"} {
		assert.Equal(t, []string{"j:run-1", "j:run-2"}, stepNames(derivedAll(t, formRun(command))), "no list of tools: %s", command)
	}

	for kind, command := range map[string]string{"local-delete": "rm -rf build", "env-override": "GIT_DIR=x make test"} {
		assert.Equal(t, []string{"j:run-1", "j:run-2"}, stepNames(derivedAll(t, formRun(command))), "the kind %s is not judged", kind)
	}
}
