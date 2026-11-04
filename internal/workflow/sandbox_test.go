package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

const sandboxHead = "def main(item):\n    "

func problemsOf(t *testing.T, body string) []string {
	t.Helper()

	found, _ := workflow.Check(folder(t, map[string]string{"workflow.star": sandboxHead + body + "\n"}, "workflow.star"))

	var texts []string
	for _, problem := range found {
		texts = append(texts, problem.Text)
	}

	return texts
}

func notesOf(t *testing.T, body string) []string {
	t.Helper()

	var texts []string
	for _, note := range workflow.Notes(folder(t, map[string]string{"workflow.star": sandboxHead + body + "\n"}, "workflow.star")) {
		texts = append(texts, note.Text)
	}

	return texts
}

func TestCheck_AcceptsASandboxCallANoneAndALeftOutKeyword(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		`agent(prompt = "p", model = "m", sandbox = sandbox(network = ["api.example.org"], allow_write = ["out"]))`,
		`agent(prompt = "p", model = "m", sandbox = sandbox())`,
		`agent(prompt = "p", model = "m", sandbox = None)`,
		`agent(prompt = "p", model = "m")`,
		`agent(prompt = "p", model = "m", sandbox = str(item))`,
	} {
		assert.Empty(t, problemsOf(t, body), body)
	}
}

func TestCheck_ReportsASandboxValueThatIsNotASandboxCallOrNone(t *testing.T) {
	t.Parallel()

	for _, value := range []string{`"on"`, `True`, `1`, `["api.example.org"]`, `{"network": []}`} {
		got := problemsOf(t, `agent(prompt = "p", model = "m", sandbox = `+value+`)`)

		require.Len(t, got, 1, value)
		assert.Contains(t, got[0], "sandbox must be sandbox(...) or None, not", value)
	}
}

func TestCheck_LeavesANonLiteralSandboxListAndASplatCallToTheRun(t *testing.T) {
	t.Parallel()

	assert.Empty(t, problemsOf(t, `sandbox(network = list(str(item)), allow_write = ["out"])`))
	assert.Empty(t, problemsOf(t, `sandbox(**dict(network = []))`))
	assert.Empty(t, problemsOf(t, `sandbox(network = [str(item), "ok.example.org"])`))
	assert.Empty(t, problemsOf(t, `sandbox(["api.example.org"])`), "a positional list is checked like a keyword list")
	assert.NotEmpty(t, problemsOf(t, `sandbox(["https://x.org"])`))
}

func TestCheck_ReportsASandboxCallWithAnUnknownKeywordOrTheWrongKind(t *testing.T) {
	t.Parallel()

	assert.Contains(t, problemsOf(t, `sandbox(read = [])`)[0], "sandbox takes no argument read. Use one of: network, allow_write.")
	assert.Contains(t, problemsOf(t, `sandbox(network = "api.example.org")`)[0], "network must be a list, not a text.")
	assert.Contains(t, problemsOf(t, `sandbox(allow_write = "out")`)[0], "allow_write must be a list, not a text.")
}

func TestCheck_ReportsALiteralNetworkEntryThatIsNoDomainAndALiteralPathWithAParentReference(t *testing.T) {
	t.Parallel()

	got := problemsOf(t, `sandbox(network = ["https://example.org", "ok.example.org"], allow_write = ["../up", "out"])`)

	require.Len(t, got, 2)
	assert.Contains(t, got[0], `sandbox network lists "https://example.org", which is not a domain name`)
	assert.Contains(t, got[1], `sandbox allow_write lists "../up"`)
}

func TestCheck_ALocalFunctionNamedSandboxIsNotTheBuiltin(t *testing.T) {
	t.Parallel()

	found, _ := workflow.Check(folder(t, map[string]string{
		"workflow.star": "def sandbox(a):\n    return a\n\ndef main(item):\n    sandbox(network = \"x\")\n    agent(prompt = \"p\", model = \"m\", sandbox = sandbox(1))\n",
	}, "workflow.star"))

	assert.Empty(t, found)
}

func TestNotes_ASandboxOfNoneGetsANoteAtItsPlaceAndNoProblem(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": sandboxHead + `agent(prompt = "p", model = "m", sandbox = None)` + "\n"}, "workflow.star")

	found, _ := workflow.Check(file)
	notes := workflow.Notes(file)

	assert.Empty(t, found)
	require.Len(t, notes, 1)
	assert.Equal(t, 2, notes[0].Line)
	assert.Contains(t, notes[0].Text, "sandbox is None, so the shell of this step runs with no sandbox")
}

func TestNotes_SeveralNotesComeInTheOrderOfTheFile(t *testing.T) {
	t.Parallel()

	none := `agent(prompt = "p", model = "m", sandbox = None)`
	file := folder(t, map[string]string{"workflow.star": sandboxHead + none + "\n    " + none + "; " + none + "\n"}, "workflow.star")

	notes := workflow.Notes(file)

	require.Len(t, notes, 3)
	assert.Equal(t, 2, notes[0].Line)
	assert.Equal(t, 3, notes[1].Line)
	assert.Equal(t, 3, notes[2].Line)
	assert.Less(t, notes[1].Col, notes[2].Col)
}

func TestNotes_AStepThatTurnsTheSandboxOnOrLeavesItAtTheDefaultGetsNoNote(t *testing.T) {
	t.Parallel()

	assert.Empty(t, notesOf(t, `agent(prompt = "p", model = "m", sandbox = sandbox())`))
	assert.Empty(t, notesOf(t, `agent(prompt = "p", model = "m")`))
}

func TestSignature_TheAgentTakesTheSandboxKeywordAndTheSandboxBuiltinHasItsOwnSignature(t *testing.T) {
	t.Parallel()

	agent, ok := workflow.Signature("agent")
	require.True(t, ok)

	var names []string
	for _, param := range agent {
		names = append(names, param.Name)
	}

	assert.Contains(t, names, "sandbox")

	box, ok := workflow.Signature("sandbox")
	require.True(t, ok)
	require.Len(t, box, 2)
	assert.Equal(t, "network", box[0].Name)
	assert.Equal(t, "allow_write", box[1].Name)
}
