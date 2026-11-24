package flow

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	forgepkg "github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// prClosingFlow is a workflow file whose main passes what the call `call` read to the command seen: whether a pull request was found, the issues, and whether the project has a forge.
func prClosingFlow(call string) string {
	return "def main(item):\n    c = " + call + "\n    command(name = \"seen\", run = [\"seen\", str(c.found), \",\".join(c.issues) + \".\", str(c.forge)])\n"
}

func TestPRClosing_ReturnsTheIssuesAPullRequestCloses(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.closing = [][]int{{13, 12}}
	h.forge.closingFar = [][]model.IssueRef{{"other-org/other-repo#3"}}
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, prClosingFlow(`pr_closing(name = "c")`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"True", "#12,#13,other-org/other-repo#3.", "True"}, seenBy(t, h), "found, the issues sorted with the issue of another repository as a full reference, and a forge")
	assert.Contains(t, stepNames(t, engine), "pr_closing:c#1")
}

func TestPRClosing_ReadsThePullRequestOfTheItemItNamesAndTheItemOfTheRunByDefault(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.closingOf = map[string][][]int{"9": {{5}}}
	h.forge.closing = [][]int{{7}}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prClosingFlow(`pr_closing(item = "9", name = "c")`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"True", "#5.", "True"}, seenBy(t, h), "the named item answers, not the item of the run")

	other := newHarness(t)
	other.forge.closingOf = map[string][][]int{"9": {{5}}}
	other.forge.closing = [][]int{{7}}

	_, err = other.engine(t).RunFile(testItem, testProject, writeFlow(t, prClosingFlow(`pr_closing(name = "c")`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"True", "#7.", "True"}, seenBy(t, other), "a call with no item reads the item of the run")
}

func TestPRClosing_AnItemWithNoPullRequestGivesFoundFalseAndNoIssues(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.noPull = true

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prClosingFlow(`pr_closing(name = "c")`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"False", ".", "True"}, seenBy(t, h))
}

func TestPRClosing_AProjectWithNoForgeGivesForgeFalseAndFoundFalseAndNoIssues(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.closingErr = forgepkg.ErrNoForge

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prClosingFlow(`pr_closing(name = "c")`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"False", ".", "False"}, seenBy(t, h))
}

func TestPRClosing_AReadThatFailsFailsTheStepAndNeverGivesAnEmptySet(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.closingErr = errFake

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prClosingFlow(`pr_closing(name = "c")`)), modeOff())

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the issues that the pull request of item 12 closes")
	assert.Empty(t, h.commands.ranBy(testItem), "the run ends at the read")
}

func TestPRClosing_ABuildWithNoForgeReaderFailsTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.noForge = true

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prClosingFlow(`pr_closing(name = "c")`)), modeOff())

	require.ErrorIs(t, err, errNoClosingReader)
}

func TestPRClosing_AnItemThatIsNotTextIsRefusedBeforeAnyRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prClosingFlow(`pr_closing(item = 9)`)), modeOff())

	require.ErrorContains(t, err, "item must be a text, not an int.")
	assert.Zero(t, h.forge.closingReads)
}

func TestPRClosing_ACallWithAnArgumentOfTheWrongTypeIsRefusedByTheBuiltin(t *testing.T) {
	t.Parallel()

	s := &script{e: &Engine{deps: Deps{Forge: &fakeForge{}}}}

	_, err := s.prClosing(starlark.NewBuiltin(prClosingKind, nil), nil, []starlark.Tuple{{starlark.String("item"), starlark.MakeInt(9)}})

	require.ErrorContains(t, err, "pr_closing")
}

func TestPRClosing_AReplayReturnsTheFirstReadNotAFreshOne(t *testing.T) {
	t.Parallel()

	file := writeFlow(t, "def main(item):\n    c = pr_closing(name = \"c\")\n    wait(name = \"ask\", question = \"go?\")\n    command(name = \"seen\", run = [\"seen\", \",\".join(c.issues)])\n")
	h := newHarness(t)
	h.forge.closing = [][]int{{12}, {99}}
	interrupted(t, h, file)

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground2(next, file)

	waitOn(t, ownerAsks(t, h))
	require.NoError(t, client.Reply(testItem, Reply{Text: "yes"}))
	require.NoError(t, receive(t, done).err)

	assert.Equal(t, []string{"#12"}, seenBy(t, h), "the issues of the first read come back, whatever the forge says now")
	assert.Equal(t, 1, h.forge.closingReads, "the replay reads the forge no second time")
}

func TestDryRun_APRClosingReadGivesAPullRequestThatClosesNoIssue(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	body := "def main(item):\n    c = pr_closing(name = \"c\")\n    if not c.found or not c.forge or c.issues != []:\n        fail(\"WRONG READ: %r\" % c)\n"
	require.NoError(t, DryRun(t.Context(), dryFile(t, body), "12", map[string]string{}, &out), out.String())

	assert.Contains(t, out.String(), "pr_closing:c#1")
}
