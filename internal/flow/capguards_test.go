package flow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/review"
)

// outputLine finds the path that a prompt of the examples names for the output file of the agent.
var outputLine = regexp.MustCompile(`Write (?:it|the [a-z-]+) to (\S+) as JSON`)

// cappedAt counts the agent calls of a run and makes the one with the given number end at a cap.
type cappedAt struct {
	mu    sync.Mutex
	calls int
	at    int
}

func (c *cappedAt) next() (call int, capped bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.calls++

	return c.calls, c.calls == c.at
}

// nthCall is a capRig predicate that is true for the agent call with the number n (the first call is 1).
func nthCall(n int) func(AgentRequest) bool {
	count := &cappedAt{at: n}

	return func(AgentRequest) bool {
		_, capped := count.next()

		return capped
	}
}

// callCount is the number of agent calls that the run made.
func (c *cappedAt) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.calls
}

// answerLikeAnAgent writes the output file that the prompt asks for. Every review says `changes`, so a run goes on to the next fix round.
func answerLikeAnAgent(rig *agentRig, request AgentRequest) {
	match := outputLine.FindStringSubmatch(request.Prompt)
	if match == nil {
		return
	}

	switch {
	case strings.HasPrefix(match[0], "Write the sub-questions"):
		rig.put(match[1], `{"questions":"What is X?"}`)
	case strings.HasPrefix(match[0], "Write the summary"):
		rig.put(match[1], `{"summary":"X is Y."}`)
	default:
		rig.put(match[1], `{"verdict":"changes","open":"fix it","findings":[{"class":"blocking","text":"fix it"}]}`)
	}
}

// guardRig runs an example, copied with its prompts. A child entry of the example (queue and swarm start their agent in a child) becomes the entry of the run.
// The agent call with the number `at` ends at the cap `outcome`. It writes its output file first unless the guard sets noFile. A capped run reaches the guard of the step either way.
func guardRig(t *testing.T, guard capGuard) (*agentRig, *cappedAt, string) {
	t.Helper()

	example, child, env, at, outcome := guard.example, guard.child, guard.env, guard.at, guard.outcome

	folder := t.TempDir()
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", example))))

	file := filepath.Join(folder, "workflow.star")
	if child != "" {
		text := string(mustRead(t, file))
		require.Equal(t, 1, strings.Count(text, "def main(item):"))
		text = strings.Replace(text, "def main(item):", "def parent(item):", 1) + "\ndef main(item):\n    " + child + "(item)\n"
		require.NoError(t, os.WriteFile(file, []byte(text), 0o600))
	}

	rig := newAgentRig(t)
	rig.h.opts.Env = env
	rig.h.preflight = &fakePreflight{}
	rig.h.gate.results = []GateResult{{Passed: true, Head: rig.head, Tree: exampleTree}}
	rig.h.forge.state = review.Standing{Number: testPull, Head: rig.head}

	count := &cappedAt{at: at}
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		_, capped := count.next()

		if !capped || !guard.noFile {
			answerLikeAnAgent(rig, request)
		}

		if capped {
			return agent.Result{Outcome: outcome, Session: "sess-1", Transcript: "/t.jsonl", Text: "cut short"}
		}

		return agent.Result{Outcome: agent.OutcomeExited, Session: "sess-1", Transcript: "/t.jsonl", Text: "all done"}
	}

	return rig, count, file
}

// capGuard is one agent step that a workflow guards with a REFUSE. `at` is the number of the agent call of that step in a run whose reviews all say `changes`.
type capGuard struct {
	step    string
	example string
	child   string
	env     map[string]string
	at      int
	outcome agent.Outcome
	want    string
	// noFile makes the capped run write no output file.
	noFile bool
}

var researchEnv = map[string]string{"RESEARCH_TOOLS": "/tools", "RESEARCH_OUT": "/out"}

var capGuards = []capGuard{
	{step: "queue implement", example: "queue", child: "work", env: map[string]string{"BASE": "main", "OWNER_LOGIN": "owner"}, at: 1, outcome: agent.OutcomeTurnCap, want: "the implement step stopped at the turns cap"},
	{step: "swarm implement", example: "swarm", child: "task", env: map[string]string{"BASE": "main", "OWNER_LOGIN": "owner"}, at: 1, outcome: agent.OutcomeBudgetCap, want: "the implement step stopped at the budget cap"},
	{step: "local plan", example: "local", at: 1, outcome: agent.OutcomeTurnCap, want: "the plan step stopped at the turns cap"},
	{step: "local implement", example: "local", at: 2, outcome: agent.OutcomeBudgetCap, want: "the implement step stopped at the budget cap"},
	{step: "local review", example: "local", at: 3, outcome: agent.OutcomeTurnCap, want: "the review step stopped at the turns cap", noFile: true},
	{step: "local fix", example: "local", at: 4, outcome: agent.OutcomeBudgetCap, want: "the fix step stopped at the budget cap"},
	{step: "research scope", example: "research", env: researchEnv, at: 1, outcome: agent.OutcomeTurnCap, want: "the scope step stopped at the turns cap", noFile: true},
	{step: "research research", example: "research", env: researchEnv, at: 2, outcome: agent.OutcomeBudgetCap, want: "the research step stopped at the budget cap"},
	{step: "research summary", example: "research", env: researchEnv, at: 3, outcome: agent.OutcomeTurnCap, want: "the summary step stopped at the turns cap", noFile: true},
	{step: "research review", example: "research", env: researchEnv, at: 4, outcome: agent.OutcomeBudgetCap, want: "the review step stopped at the budget cap", noFile: true},
	{step: "research revise", example: "research", env: researchEnv, at: 5, outcome: agent.OutcomeTurnCap, want: "the revise step stopped at the turns cap"},
}

func TestExamples_ACappedAgentStepStopsTheItemWithARefuseThatNamesTheStepAndTheCapAndStartsNoLaterAgent(t *testing.T) {
	t.Parallel()

	for _, guard := range capGuards {
		t.Run(guard.step, func(t *testing.T) {
			t.Parallel()

			rig, count, file := guardRig(t, guard)

			_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "REFUSE")
			assert.Contains(t, err.Error(), guard.want)
			assert.Equal(t, guard.at, count.callCount(), "no agent starts after the capped step")
		})
	}
}

func TestExample_ACappedPlanStopsTheItemWithARefuseBeforeTheImplement(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "approve" }, func(r AgentRequest) bool { return r.Model == "opus" }, agent.OutcomeTurnCap)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "the plan step stopped at the turns cap")
	assert.Equal(t, []string{"opus"}, rig.models(), "no implement starts after the capped plan")
}

func TestExample_ACappedReviewStopsTheItemWithARefuseBeforeAnyFix(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "changes" }, nthCall(3), agent.OutcomeBudgetCap)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "the review step stopped at the budget cap")
	assert.Equal(t, []string{"opus", "sonnet", "opus"}, rig.models(), "no fix starts after the capped review")
}

func TestExample_ACappedSettleStopsTheItemWithARefuse(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "changes" }, nthCall(14), agent.OutcomeTurnCap)
	engine := rig.h.engine(t)
	done := runInBackground2(engine, file)

	waitForTheOwner(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))
	outcome := <-done

	require.Error(t, outcome.err)
	assert.Contains(t, outcome.err.Error(), "REFUSE")
	assert.Contains(t, outcome.err.Error(), "the settle step stopped at the turns cap")
	assert.Len(t, rig.models(), 14, "the settle is the last agent")
}

func TestReviewFix_ACappedReviewStopsTheLoopWithARefuseThatNamesTheCap(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "changes" }, func(AgentRequest) bool { return true }, agent.OutcomeTurnCap)
	require.NoError(t, os.WriteFile(file, []byte(libraryLoop("")), 0o600))

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), "the review step stopped at the turns cap")
	assert.Equal(t, []string{"opus"}, rig.models(), "no fix starts after the capped review")
}
