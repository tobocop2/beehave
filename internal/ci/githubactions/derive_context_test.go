package githubactions_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ci"
	"github.com/tobocop2/beehave/internal/ci/githubactions"
)

const (
	// keptJob is a job that reads no secret, so that a file whose other jobs are left out still gives a step.
	keptJob = "  k:\n    steps:\n      - run: make test\n"
	// aliasBoundRefusal is the refusal of a file that holds too many nodes with its aliases expanded.
	aliasBoundRefusal = "ci.yaml: not a workflow file: with its YAML aliases expanded, it holds more than 100000 nodes"
)

func commands(got ci.Derived) []string {
	out := make([]string, len(got.Steps))
	for i, step := range got.Steps {
		out[i] = step.Command
	}

	return out
}

func TestDerive_ASecretThroughAYAMLAliasOrMergeKeyIsRead(t *testing.T) {
	t.Parallel()

	anchorJob := "  a:\n    env:\n      T: &tok ${{ secrets.X }}\n    steps:\n      - run: go vet ./...\n"
	anchorEnvs := "  a:\n    env: &envs\n      T: ${{ secrets.X }}\n    steps:\n      - run: go vet ./...\n"

	for name, jobs := range map[string]string{
		"an alias of a value in another job": anchorJob + "  b:\n    env:\n      T: *tok\n    steps:\n      - run: curl -H \"Auth=$T\" https://api.example.com/deploy\n",
		"an alias of a value in a step":      anchorJob + "  b:\n    steps:\n      - env:\n          T: *tok\n        run: curl -d \"$T\" https://api.example.com/hook\n",
		"a merge key of a mapping":           anchorEnvs + "  b:\n    env:\n      <<: *envs\n    steps:\n      - run: curl -d \"$T\" https://api.example.com/hook\n",
		"an alias of the whole job":          "  a: &job\n    steps:\n      - env:\n          T: ${{ secrets.X }}\n        run: go vet ./...\n  b: *job\n",
	} {
		got := derivedAll(t, readOnlyFile+"jobs:\n"+jobs+keptJob)

		assert.Equal(t, []string{"job ci.yaml:a", "job ci.yaml:b"}, refusedNames(got), name)
		assert.Equal(t, []string{"k:run-1"}, stepNames(got), name)
	}
}

func TestDerive_APermissionsAliasIsReadThroughToItsValues(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, "permissions: write-all\njobs:\n  a:\n    permissions: &ro\n      contents: read\n    steps:\n      - run: go vet ./...\n  b:\n    permissions: *ro\n    steps:\n      - run: make test\n")

	assert.Equal(t, []string{"a:run-1", "b:run-1"}, stepNames(got))
	assert.Empty(t, got.Refused)
}

func TestDerive_AnAliasThatHoldsItselfIsRefused(t *testing.T) {
	t.Parallel()

	assert.Contains(t, refusedDerive(t, readOnlyFile+"jobs:\n  a: &x\n    steps:\n      - run: make test\n        env: *x\n"), "ci.yaml: not a workflow file: a YAML alias refers to a node that holds it")
}

func TestDerive_AnExpressionThatReadsTheWholeGithubContextCountsAsTheToken(t *testing.T) {
	t.Parallel()

	jobWith := func(value string) string {
		return readOnlyFile + "jobs:\n  j:\n    steps:\n      - env:\n          ALL: " + value + "\n        run: go vet ./...\n" + keptJob
	}

	for name, value := range map[string]string{
		"join of the object filter":   "${{ join(github.*, ',') }}",
		"json of the object filter":   "${{ toJSON(github.*) }}",
		"a filter then a field":       "${{ github.*.token }}",
		"the context in a comparison": "${{ github == null }}",
		"over two lines":              "\"${{ format('{0}',\n            github) }}\"",
	} {
		got := derivedAll(t, jobWith(value))

		assert.Equal(t, []string{"job ci.yaml:j"}, refusedNames(got), name)
		assert.Equal(t, []string{"k:run-1"}, stepNames(got), name)
	}

	for name, value := range map[string]string{
		"another field of the context":   "${{ github.event.number }}",
		"an input named github":          "${{ inputs.github }}",
		"the word outside an expression": "github, then a comma",
	} {
		got := derivedAll(t, jobWith(value))

		assert.Empty(t, got.Refused, name)
		assert.Equal(t, []string{"j:run-1", "k:run-1"}, stepNames(got), name)
	}
}

func TestDerive_ASecretInOneStepLeavesOutTheWholeJob(t *testing.T) {
	t.Parallel()

	for name, steps := range map[string]string{
		"carried through GITHUB_ENV": "      - run: echo \"T=${{ secrets.X }}\" >> \"$GITHUB_ENV\"\n      - run: curl -H \"Auth=$T\" https://api.example.com/deploy\n",
		"a checkout given a PAT":     "      - uses: actions/checkout@v7\n        with:\n          token: ${{ secrets.PAT }}\n      - run: go run ./cmd/release\n",
	} {
		got := derivedAll(t, readOnlyFile+"jobs:\n  j:\n    steps:\n"+steps+keptJob)

		assert.Equal(t, []string{"job ci.yaml:j"}, refusedNames(got), name)
		assert.Equal(t, "reads a secret", got.Refused[0].Reason, name)
		assert.Equal(t, []string{"k:run-1"}, stepNames(got), name)
	}
}

func TestDerive_TheCommandOfAStepHoldsTheContextThatCIRunsItIn(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, readOnlyFile+"env:\n  MODE: x\ndefaults:\n  run:\n    working-directory: ../..\njobs:\n"+
		"  j:\n    container: golang:1.22\n    env:\n      B: y\n    steps:\n"+
		"      - run: make test\n        shell: bash\n        working-directory: /\n        continue-on-error: true\n        env:\n          A: z\n          MODE: w\n"+
		"      - run: make vet\n"+
		"  k:\n    container:\n      image: golang\n      ports: [80]\n    env: ${{ fromJSON(vars.E) }}\n    steps:\n      - run: make lint\n")

	assert.Equal(t, []string{
		"make test [shell bash] [working-directory /] [env A B MODE] [container golang:1.22] [continue-on-error true]",
		"make vet [working-directory ../..] [env B MODE] [container golang:1.22]",
		"make lint [working-directory ../..] [env ${{ fromJSON(vars.E) }} MODE] [container {image: golang, ports: [80]}]",
	}, commands(got))

	merged := derivedAll(t, readOnlyFile+"jobs:\n  j:\n    env: &base\n      C: x\n    steps:\n      - run: make vet\n"+
		"  k:\n    env:\n      <<: [*base, {D: y}]\n      E: z\n    steps:\n      - run: make test\n        env:\n          <<: *base\n")
	assert.Equal(t, []string{"make vet [env C]", "make test [env C D E]"}, commands(merged), "a merge key gives the names it merges")

	plain := derivedAll(t, readOnlyFile+"jobs:\n  j:\n    env:\n    steps:\n      - run: make test\n")
	assert.Equal(t, []string{"make test"}, commands(plain), "a step with no context is its run text")
}

func TestDerive_TheRunTextKeepsTheSpacesInsideQuotes(t *testing.T) {
	t.Parallel()

	for run, want := range map[string]string{
		`grep -c "a  b" f`:         `grep -c "a  b" f`,
		`printf '%s   %s' x  y`:    `printf '%s   %s' x y`,
		`echo "it\"s  here"   now`: `echo "it\"s  here" now`,
		`echo a\  b   c`:           `echo a\  b c`,
		"echo 'x  y'\t\tz":         "echo 'x  y' z",
	} {
		got := derivedAll(t, readOnlyFile+"jobs:\n  j:\n    steps:\n      - run: |\n          "+run+"\n")

		assert.Equal(t, []string{want}, commands(got), run)
	}
}

func TestDerive_TheCommandsOfTheHeldListAreExactCIText(t *testing.T) {
	t.Parallel()

	got, err := githubactions.Derive([]ci.File{{Path: "ci.yaml", Text: readOnlyFile + "jobs:\n  j:\n    steps:\n      - run: grep -c \"a  b\" f\n"}})
	require.NoError(t, err)

	assert.Empty(t, ci.Hold(got.Steps, []string{"test"}, ci.Map{`grep -c "a  b" f`: {"test"}}), "a key copied from the CI file matches")
}

func TestDerive_AnIfWithNoBracesIsReadForTheTokenAndSecrets(t *testing.T) {
	t.Parallel()

	for name, jobs := range map[string]string{
		"the field in the if of a job":          "  j:\n    if: startsWith(github.token, 'ghs_')\n    steps:\n      - run: make vet\n",
		"the whole context in the if of a step": "  j:\n    steps:\n      - if: contains(toJSON(github), 'x')\n        run: make vet\n",
		"an index of the context in a step if":  "  j:\n    steps:\n      - if: github['token'] != ''\n        run: make vet\n",
		"braces in the if of a job":             "  j:\n    if: ${{ github.token != '' }}\n    steps:\n      - run: make vet\n",
		"the stored values in the if of a step": "  j:\n    steps:\n      - if: secrets.DEPLOY != ''\n        run: make vet\n",
	} {
		got := derivedAll(t, readOnlyFile+"jobs:\n"+jobs+keptJob)

		assert.Equal(t, []string{"job ci.yaml:j"}, refusedNames(got), name)
		assert.Equal(t, []string{"k:run-1"}, stepNames(got), name)
	}

	for name, jobs := range map[string]string{
		"another field in the if of a job": "  j:\n    if: github.ref == 'refs/heads/main'\n    steps:\n      - run: make vet\n",
		"an input in the if of a step":     "  j:\n    steps:\n      - if: inputs.github == 'x'\n        run: make vet\n",
	} {
		got := derivedAll(t, readOnlyFile+"jobs:\n"+jobs+keptJob)

		assert.Empty(t, got.Refused, name)
		assert.Equal(t, []string{"j:run-1", "k:run-1"}, stepNames(got), name)
	}
}

func TestDerive_TheIfOfAStepJoinsItsCommand(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, readOnlyFile+"jobs:\n  j:\n    steps:\n      - run: make test\n        if: false\n"+
		"      - run: make vet\n        if: github.ref == 'refs/heads/main'\n        continue-on-error: true\n      - run: make lint\n")

	assert.Equal(t, []string{
		"make test [if false]",
		"make vet [continue-on-error true] [if github.ref == 'refs/heads/main']",
		"make lint",
	}, commands(got))
}

// nestedAliases gives a file whose job container is levels of aliases, each level a list of width aliases of the level below.
func nestedAliases(levels, width int) string {
	lines := []string{readOnlyFile + "x:\n  l0: &a0 v\n"}

	for level := 1; level <= levels; level++ {
		below := "*a" + strconv.Itoa(level-1)
		lines = append(lines, "  l"+strconv.Itoa(level)+": &a"+strconv.Itoa(level)+" ["+strings.Repeat(below+", ", width-1)+below+"]\n")
	}

	return strings.Join(lines, "") + "jobs:\n  j:\n    container: *a" + strconv.Itoa(levels) + "\n    steps:\n      - run: make test\n"
}

func TestDerive_AFileWhoseAliasesExpandPastTheBoundIsRefused(t *testing.T) {
	t.Parallel()

	assert.Contains(t, refusedDerive(t, nestedAliases(6, 10)), aliasBoundRefusal)
	assert.Contains(t, refusedDerive(t, nestedAliases(5, 11)), aliasBoundRefusal)

	got := derivedAll(t, nestedAliases(4, 10))
	assert.Equal(t, []string{"j:run-1"}, stepNames(got), "the control: an expansion under the bound is read")
}

func TestDerive_AFileOfTwelveAliasLevelsIsRefusedWithinASecond(t *testing.T) {
	t.Parallel()

	refused := make(chan error, 1)

	go func() {
		_, err := githubactions.Derive([]ci.File{{Path: "ci.yaml", Text: nestedAliases(12, 10)}})
		refused <- err
	}()

	select {
	case err := <-refused:
		require.ErrorContains(t, err, aliasBoundRefusal)
	case <-time.After(time.Second):
		require.Fail(t, "no refusal within a second: a node shared by aliases is read once for each alias")
	}
}
