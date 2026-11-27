package flow

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

func ownedBy(owner string) func(string) model.Project {
	return func(project string) model.Project {
		return model.Project{
			Name: project, Repo: "o/n", BaseBranch: "main", Gate: "make check",
			Vars: map[string]any{"people": map[string]any{"owner": owner}, "limits": []any{"never run rm on a glob"}, "traps": []any{"the cache hides a stale build"}},
		}
	}
}

const factsPrompt = "owner={{ .facts.vars.people.owner }} base={{ .facts.base_branch }} repo={{ .facts.repo }}\n{{ range .facts.vars.limits }}limit={{ . }}\n{{ end }}{{ range .facts.vars.traps }}trap={{ . }}\n{{ end }}tokens={{ range index .facts.vars \"token_rules\" }}x{{ end }}.\n"

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
	assert.Contains(t, seen[0].Prompt, "tokens=.", "a setting that vars leaves out reads as nothing to range over, and the template does not fail")
}

func TestFacts_TheFactsHoldTheTenFieldsOfThePlumbingTheVarsAndTheRunnersAndNothingElse(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = func(project string) model.Project {
		return model.Project{Name: project, Vars: map[string]any{"board": map[string]any{"number": json.Number("3")}}}
	}
	h.flowIs(t, "def main(item):\n    f = facts()\n    fail(\"FIELDS=\" + \",\".join(sorted(dir(f))) + \" N=%d\" % f.vars[\"board\"][\"number\"])\n")

	outcome := receive(t, runInBackground(h.engine(t), testItem))

	require.ErrorContains(t, outcome.err, "FIELDS=base_branch,build_host,forge,gate,gate_steps,repo,runners,source,tracker,vars N=3")
}

func TestFacts_AProjectThatTheConfigDoesNotHoldHasEmptyFacts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    f = facts()\n    fail(\"N=%d/%d/%r\" % (len(f.vars), len(f.gate_steps), f.base_branch))\n")

	outcome := receive(t, runInBackground(h.engine(t), testItem))

	require.ErrorContains(t, outcome.err, "N=0/0/\"\"")
}

func TestFacts_TheRunRefusesAnArgument(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    facts(\"owner\")\n")

	outcome := receive(t, runInBackground(h.engine(t), testItem))

	require.ErrorContains(t, outcome.err, "facts")
}

// A step that cannot record the facts fails the call, and the file gets no facts. The config loader refuses NaN, so a real project never holds it; here a NaN that JSON cannot encode stands in for a failed write of the record.
func TestFacts_AVarThatTheRecordCannotHoldFailsTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    f = facts()\n    fail(\"GOT FACTS\")\n")
	h.facts = func(project string) model.Project {
		return model.Project{Name: project, Vars: map[string]any{"ratio": math.NaN()}}
	}

	outcome := receive(t, runInBackground(h.engine(t), testItem))

	require.Error(t, outcome.err)
	assert.NotContains(t, outcome.err.Error(), "GOT FACTS", "the call failed before the file used the facts")
	assert.Contains(t, outcome.err.Error(), "NaN", "the error names the value that the record cannot hold")
}

// A crash after facts() and before the answer, then a resume under a config that changed: the replay returns the facts the run started with.
func TestFacts_AReplayKeepsTheFactsOfTheFirstRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = ownedBy("ann")
	file := writeFlow(t, "def main(item):\n    f = facts()\n    wait(name = \"ask\", question = \"go?\")\n    fail(\"OWNER \" + f.vars[\"people\"][\"owner\"])\n")

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	ended := make(chan error, 1)

	go func() {
		_, runErr := first.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	waitOn(t, ownerAsks(t, h))

	steps, err := dbos.GetWorkflowSteps(first.ctx, testRun)
	require.NoError(t, err)
	require.True(t, hasStep(steps, "facts#1"), "facts() is a recorded step")

	stop()
	require.NoError(t, first.Shutdown())
	require.Error(t, receive(t, ended))

	h.facts = ownedBy("bob")
	next := h.engine(t)
	done := make(chan runOutcome, 1)

	go func() {
		result, runErr := next.RunFile(testItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: runErr}
	}()

	require.NoError(t, h.client(t, testVersion).Reply(testItem, Reply{Text: "yes"}))

	outcome := receive(t, done)
	require.ErrorContains(t, outcome.err, "OWNER ann")
}
