package flow

import (
	"context"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func ownedBy(owner string) func(string) model.Project {
	return func(project string) model.Project {
		return model.Project{
			Name: project, Repo: "o/n", BaseBranch: "main", Gate: "make check",
			People: map[string]string{"owner": owner}, Limits: []string{"never run rm on a glob"}, Traps: []string{"the cache hides a stale build"},
		}
	}
}

const factsPrompt = "owner={{ .facts.people.owner }} base={{ .facts.base_branch }} repo={{ .facts.repo }}\n{{ range .facts.limits }}limit={{ . }}\n{{ end }}{{ range .facts.traps }}trap={{ . }}\n{{ end }}tokens={{ len .facts.token_rules }}\n"

func TestFacts_TheFactsOfTheProjectReachThePromptInputOfAnAgent(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.facts = ownedBy("ann")
	rig.reply("", exited)

	_, err := rig.run(t, bodyFlow("agent(model = \"m\", prompt = \"prompts/a.md\", inputs = {\"facts\": facts()}, name = \"a\")\n"), factsPrompt)
	require.NoError(t, err)

	seen := rig.h.agents.seen()
	require.Len(t, seen, 1)
	assert.Contains(t, seen[0].Prompt, "owner=ann base=main repo=o/n")
	assert.Contains(t, seen[0].Prompt, "limit=never run rm on a glob")
	assert.Contains(t, seen[0].Prompt, "trap=the cache hides a stale build")
	assert.Contains(t, seen[0].Prompt, "tokens=0", "a fact the file leaves out is an empty list, not a missing key")
}

func TestFacts_TheBoardOfTheProjectIsReadAsFourFieldsAndIsEmptyWhenTheProjectHasNone(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = withBoard("acme")
	h.flowIs(t, "def main(item):\n    f = facts()\n    fail(\"B=%d/%s/%s/%s\" % (f.board_number, f.board_owner, f.board_started, f.board_merged))\n")

	outcome := <-runInBackground(h.engine(t), testItem)

	require.ErrorContains(t, outcome.err, "B=3/acme/In Progress/Done")

	none := newHarness(t)
	none.flowIs(t, "def main(item):\n    f = facts()\n    fail(\"B=%d/%r\" % (f.board_number, f.board_owner))\n")

	outcome = <-runInBackground(none.engine(t), testItem)

	require.ErrorContains(t, outcome.err, "B=0/\"\"")
}

func TestFacts_AProjectThatTheConfigDoesNotHoldHasEmptyFacts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    f = facts()\n    fail(\"N=%d/%d/%r\" % (len(f.limits), len(f.people), f.base_branch))\n")

	outcome := <-runInBackground(h.engine(t), testItem)

	require.ErrorContains(t, outcome.err, "N=0/0/\"\"")
}

func TestFacts_TheRunRefusesAnArgument(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    facts(\"owner\")\n")

	outcome := <-runInBackground(h.engine(t), testItem)

	require.ErrorContains(t, outcome.err, "facts")
}

// A crash after facts() and before the answer, then a resume under a config that changed: the replay returns the facts the run started with.
func TestFacts_AReplayKeepsTheFactsOfTheFirstRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = ownedBy("ann")
	file := writeFlow(t, "def main(item):\n    f = facts()\n    wait(name = \"ask\", question = \"go?\")\n    fail(\"OWNER \" + f.people[\"owner\"])\n")

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	ended := make(chan error, 1)

	go func() {
		_, runErr := first.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	waitForTheOwner(t, h)

	steps, err := dbos.GetWorkflowSteps(first.ctx, testRun)
	require.NoError(t, err)
	require.True(t, hasStep(steps, "facts#1"), "facts() is a recorded step")

	stop()
	require.NoError(t, first.Shutdown())
	require.Error(t, <-ended)

	h.facts = ownedBy("bob")
	next := h.engine(t)
	done := make(chan runOutcome, 1)

	go func() {
		result, runErr := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: runErr}
	}()

	require.NoError(t, h.client(t, testVersion).Answer(testItem, "yes"))

	select {
	case outcome := <-done:
		require.ErrorContains(t, outcome.err, "OWNER ann")
	case <-time.After(30 * time.Second):
		t.Fatal("the resumed run did not end")
	}
}
