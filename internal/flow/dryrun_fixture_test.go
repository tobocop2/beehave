package flow

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reviewLoop reviews until the verdict is approve, and fixes after each other verdict. Eight rounds is the most it walks.
const reviewLoop = "def main(item):\n    for _ in range(8):\n        r = agent(name = \"review\", prompt = \"p.md\", model = \"sonnet\", outputs = [\"verdict\"])\n        if r.verdict == \"approve\":\n            return\n        agent(name = \"fix\", prompt = \"p.md\", model = \"sonnet\")\n    fail(\"NOT APPROVED\")\n"

// fixtureOf writes the text to a file and loads it, as the command does.
func fixtureOf(t *testing.T, text string) Fixture {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fixture.json")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	loaded, err := LoadFixture(path)
	require.NoError(t, err)

	return loaded
}

// dryWithFixture dry-runs the workflow text over the fixture text and returns what the run printed and its error.
func dryWithFixture(t *testing.T, flowText, fixtureText string) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := DryRun(t.Context(), dryFileWithPrompt(t, flowText), "12", nil, &out, WithFixture(fixtureOf(t, fixtureText)))

	return out.String(), err
}

// calls counts the lines of the form "agent:<label>#<n>/render", one for each call of the label.
func calls(out, name string) int {
	n := 0

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, " "+name+"#") && strings.Contains(line, "/render") {
			n++
		}
	}

	return n
}

const rejectTwice = `{
  "agent:review": [{"outputs": {"verdict": "changes"}}, {"outputs": {"verdict": "changes"}}, {"outputs": {"verdict": "approve"}}],
  "agent:fix": {}
}`

func TestDryRun_AScriptedStepResultSteersTheRun(t *testing.T) {
	t.Parallel()

	out, err := dryWithFixture(t, reviewLoop, rejectTwice)

	require.NoError(t, err, out)
	assert.Equal(t, 3, calls(out, "agent:review"), out)
	assert.Equal(t, 2, calls(out, "agent:fix"), out)
}

func TestDryRun_ThreeRejectionsWalkThreeFixRoundsBeforeTheApproval(t *testing.T) {
	t.Parallel()

	out, err := dryWithFixture(t, reviewLoop, `{
  "agent:review": [{"outputs": {"verdict": "changes"}}, {"outputs": {"verdict": "changes"}}, {"outputs": {"verdict": "changes"}}, {"outputs": {"verdict": "approve"}}],
  "agent:fix": {}
}`)

	require.NoError(t, err, out)
	assert.Equal(t, 4, calls(out, "agent:review"), out)
	assert.Equal(t, 3, calls(out, "agent:fix"), out)
}

func TestDryRun_AListOfResultsIsExactAndAPastTheEndCallIsRefused(t *testing.T) {
	t.Parallel()

	out, err := dryWithFixture(t, reviewLoop, `{
  "agent:review": [{"outputs": {"verdict": "changes"}}, {"outputs": {"verdict": "changes"}}],
  "agent:fix": {}
}`)

	require.Error(t, err)
	assert.Contains(t, out, "fixture ")
	assert.Contains(t, out, "agent:review call 3")
	assert.Contains(t, out, "lists 2")
}

func TestDryRun_AListEntryNoCallUsedIsRefusedAtTheEndOfACleanRun(t *testing.T) {
	t.Parallel()

	out, err := dryWithFixture(t, reviewLoop, `{"agent:review": [{"outputs": {"verdict": "approve"}}, {"outputs": {"verdict": "approve"}}]}`)

	require.Error(t, err)
	assert.Contains(t, out, "agent:review: results in the file: 2; calls in the run: 1")
	assert.Contains(t, out, "result 2 was never made")
}

func TestDryRun_AnObjectResultNoCallUsedIsRefusedAtTheEndOfACleanRun(t *testing.T) {
	t.Parallel()

	out, err := dryWithFixture(t, reviewLoop, `{"agent:review": [{"outputs": {"verdict": "approve"}}], "agent:fix": {}}`)

	require.Error(t, err)
	assert.Contains(t, out, "agent:fix")
	assert.Contains(t, out, "the run never made a call")
}

func TestDryRun_AnObjectResultAnswersEveryCallOfItsKey(t *testing.T) {
	t.Parallel()

	const twoReviews = "def main(item):\n    a = agent(name = \"review\", prompt = \"p.md\", model = \"sonnet\", outputs = [\"verdict\"])\n    b = agent(name = \"review\", prompt = \"p.md\", model = \"sonnet\", outputs = [\"verdict\"])\n    if a.verdict != \"seen\" or b.verdict != \"seen\":\n        fail(\"WRONG VERDICTS: %s %s\" % (a.verdict, b.verdict))\n"

	out, err := dryWithFixture(t, twoReviews, `{"agent:review": {"outputs": {"verdict": "seen"}}}`)

	require.NoError(t, err, out)
	assert.Equal(t, 2, calls(out, "agent:review"), out)
}

func TestDryRun_AnOutputTheResultDoesNotNameKeepsTheBaseDefault(t *testing.T) {
	t.Parallel()

	const flow = "def main(item):\n    r = agent(name = \"review\", prompt = \"p.md\", model = \"sonnet\", outputs = [\"verdict\", \"count\"])\n    if r.verdict != \"changes\" or r.count != \"0\":\n        fail(\"WRONG OUTPUTS: %s %s\" % (r.verdict, r.count))\n"

	out, err := dryWithFixture(t, flow, `{"agent:review": {"outputs": {"verdict": "changes"}}}`)

	require.NoError(t, err, out)
}

func TestDryRun_AnAgentCallWhoseKeyTheFileDoesNotListIsRefusedWithTheKeysOfTheFile(t *testing.T) {
	t.Parallel()

	out, err := dryWithFixture(t, reviewLoop, `{"agent:reviewer": [{"outputs": {"verdict": "approve"}}]}`)

	require.Error(t, err)
	assert.Contains(t, out, "agent:review has no entry")
	assert.Contains(t, out, "[agent:reviewer]")
	assert.Contains(t, out, "add the key agent:review to the fixture file")
}

func TestDryRun_AnOutputTheCallDoesNotDeclareIsRefusedWithTheKeyAndTheDeclaredNames(t *testing.T) {
	t.Parallel()

	out, err := dryWithFixture(t, reviewLoop, `{"agent:review": [{"outputs": {"verdit": "changes"}}]}`)

	require.Error(t, err)
	assert.Contains(t, out, `agent:review call 1 sets output "verdit"`)
	assert.Contains(t, out, "the declared outputs: [verdict ")
}

func TestDryRun_ARunWithNoFixtureLeavesEveryAgentCallToTheBaseDefault(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryFileWithPrompt(t, reviewLoop), "12", nil, &out), out.String())
	assert.Equal(t, 1, calls(out.String(), "agent:review"), out.String())
	assert.Zero(t, calls(out.String(), "agent:fix"), out.String())
}

func TestLoadFixture_AnUnknownFieldAnUnknownBuiltinAndAWrongTypeAreRefusedWithTheFile(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ text, want string }{
		"an unknown field":           {`{"agent:review": [{"verdict": "changes"}]}`, `unknown field "verdict"`},
		"an unknown builtin":         {`{"frobnicate:x": [{}]}`, `frobnicate:x`},
		"the merge key":              {`{"merge": [{}]}`, `merge`},
		"a field of no wait":         {`{"wait:ask": {"verdict": "go"}}`, `unknown field "verdict"`},
		"a key twice":                {`{"agent:review": [{}], "gate:check": {}, "agent:review": [{}]}`, `the key "agent:review" is in the file more than once`},
		"a wrong type":               {`{"agent:review": [{"outputs": {"verdict": 7}}]}`, `agent:review`},
		"a top level that is a list": {`[]`, `a JSON object`},
		"text that is no JSON":       {`not json`, `fixture.json`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "fixture.json")
			require.NoError(t, os.WriteFile(path, []byte(test.text), 0o600))

			_, err := LoadFixture(path)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "fixture.json")
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestLoadFixture_ADuplicateKeyIsFoundOnlyAtTheTopLevelAndDataThatEndsEarlyHasNone(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ text, want string }{
		"a key twice after a list":       {`{"a": [1, {"a": 2}], "b": {"a": 1}, "a": 3}`, "a"},
		"a name that repeats inside":     {`{"a": {"x": 1, "x": 2}, "b": [{"x": 1}, {"x": 2}]}`, ""},
		"no key":                         {`{}`, ""},
		"no data":                        {``, ""},
		"a key with no value":            {`{"a"`, ""},
		"a name that is no text":         {`{1: 2}`, ""},
		"a value that ends too early":    {`{"a": [`, ""},
		"a first key and a second twice": {`{"a": 1, "b": 2, "b": 3}`, "b"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, duplicateKey([]byte(test.text)))
		})
	}
}

func TestLoadFixture_AFileThatCannotBeReadIsRefusedWithItsName(t *testing.T) {
	t.Parallel()

	_, err := LoadFixture(filepath.Join(t.TempDir(), "no-such-file.json"))

	require.ErrorContains(t, err, "no-such-file.json")
}
