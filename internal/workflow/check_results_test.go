package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

func TestCheck_ReportsAFieldThatTheResultOfABuiltinDoesNotHave(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, call, want string }{
		{"agent", `agent("p", "m")`, "3:11 haed is not a field of the result of agent. It has: text, session, transcript, head_sha, violations, stop, cost_usd, rewrote."},
		{"batch_merge", `batch_merge(item.id, "b", "r", name = "n")`, "3:11 haed is not a field of the result of batch_merge. It has: merged, rejected, deferred, tip, tree, gates, findings, note, unproven."},
		{"command", `command(["a"])`, "3:11 haed is not a field of the result of command. It has: stdout, stderr, rc."},
		{"gate", `gate()`, "3:11 haed is not a field of the result of gate. It has: passed, head, tree, report, steps."},
		{"gather", `gather([])`, "3:11 haed is not a field of the result of gather. It has: results."},
		{"item_comment", `item_comment("1", "t", "k")`, "3:11 haed is not a field of the result of item_comment. It has: id."},
		{"item_create", `item_create("t", "k")`, "3:11 haed is not a field of the result of item_create. It has: id, created."},
		{"item_status", `item_status("1", "open")`, "3:11 haed is not a field of the result of item_status. It has: id, status."},
		{"merge", `merge("h")`, "3:11 haed is not a field of the result of merge. It has: state, reason."},
		{"board_sync", `board_sync(board = 3, status = "Done")`, "3:11 haed is not a field of the result of board_sync. It has: number, status."},
		{"pr_open", `pr_open(title = "t", base = "b")`, "3:11 haed is not a field of the result of pr_open. It has: number, created, stack."},
		{"preflight", `preflight()`, "3:11 haed is not a field of the result of preflight. It has: ok, stop, code, reason."},
		{"queue", `queue()`, "3:11 haed is not a field of the result of queue. It has: rows, free."},
		{"spawn", `spawn(["1"])`, "3:11 haed is not a field of the result of spawn. It has: runs."},
		{"wait", `wait()`, "3:11 haed is not a field of the result of wait. It has: answer, answers, timed_out."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    x = "+tc.call+"\n    y = x.haed\n    z = x.haed\n")

			require.NotEmpty(t, got)
			assert.Contains(t, got[0], tc.want)
		})
	}
}

func TestCheck_AcceptsEachFieldThatABuiltinReturns(t *testing.T) {
	t.Parallel()

	got := checkText(t, `def main(item):
    a = agent("p", "m", outputs = ["verdict", "findings"])
    b = batch_merge(item.id, "b", "r", name = "n")
    c = command(["a"])
    g = gate()
    h = gather([])
    i = item_create("t", "k")
    m = merge("h")
    s = spawn(["1"])
    w = wait()
    command(["x", a.text, a.verdict, a.findings, a.session, a.transcript, a.head_sha, a.violations, a.stop, a.cost_usd, a.rewrote, b.merged, b.tip, b.tree, b.gates, c.stdout, c.rc])
    command(["x", g.passed, g.head, g.tree, g.report, h.results, i.id, i.created, m.state, m.reason, s.runs, w.answer, b.rejected, b.deferred])
`)

	assert.Empty(t, got)
}

func TestCheck_AnAgentWithLiteralOutputsHasThemAsFields(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    out = agent(\"p\", \"m\", outputs = [\"verdict\"])\n    command([out.verdikt])\n    command([out.verdict])\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "3:18 verdikt is not a field of the result of agent. It has: text, session, transcript, head_sha, violations, stop, cost_usd, rewrote, verdict.")
}

func TestCheck_AnAgentWhoseOutputsAreNotLiteralHasItsOwnFieldsLeftToTheRun(t *testing.T) {
	t.Parallel()

	got := checkText(t, `def main(item):
    names = ["verdict"]
    a = agent("p", "m", outputs = names)
    b = agent("p", "m", outputs = ["verdict", names[0]])
    c = agent("p", "m", **{"outputs": names})
    d = agent("p", "m", outputs = names + ["x"])
    command([a.verdikt, b.verdikt, c.verdikt, d.verdikt])
`)

	assert.Empty(t, got)
}

func TestCheck_AnAgentWithATupleOfOutputsIsRead(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    a = agent(\"p\", \"m\", outputs = (\"verdict\",))\n    command([a.verdict, a.nope])\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "nope is not a field of the result of agent")
}

func TestCheck_ANameThatTheCheckCannotKnowIsLeftToTheRun(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"reassigned":            "def main(item):\n    x = command([\"a\"])\n    x = struct(haed = 1)\n    command([x.haed])\n",
		"reassigned by loop":    "def main(item):\n    x = command([\"a\"])\n    for x in [struct(haed = 1)]:\n        command([x.haed])\n",
		"reassigned by augment": "def main(item):\n    x = command([\"a\"])\n    x += struct(haed = 1)\n    command([x.haed])\n",
		"parameter":             "def helper(x):\n    return x.haed\n\ndef main(item):\n    helper(command([\"a\"]))\n",
		"parameter bound":       "def helper(x):\n    x = command([\"a\"])\n    return x.haed\n\ndef main(item):\n    helper(1)\n",
		"comprehension":         "def main(item):\n    x = command([\"a\"])\n    command([x.haed for x in [struct(haed = 1)]])\n",
		"nested function":       "def main(item):\n    x = command([\"a\"])\n    def inner(x):\n        return x.haed\n    inner(1)\n",
		"lambda parameter":      "def main(item):\n    x = command([\"a\"])\n    f = lambda x: x.haed\n    f(1)\n",
		"not a builtin":         "def main(item):\n    x = struct(a = 1)\n    command([x.haed])\n",
		"env is text":           "def main(item):\n    x = env(\"HOME\")\n    command([x.haed])\n",
		"from a function":       "def make():\n    return command([\"a\"])\n\ndef main(item):\n    x = make()\n    command([x.haed])\n",
		"a method result":       "def main(item):\n    x = \"a\".upper()\n    command([x.haed])\n",
		"a call result":         "def main(item):\n    command([command([\"a\"]).haed])\n",
		"tuple target":          "def main(item):\n    x, y = command([\"a\"]), 1\n    command([x.haed])\n",
		"shadowed builtin":      "def command(a):\n    return struct(haed = 1)\n\ndef main(item):\n    x = command([\"a\"])\n    gate(run = [x.haed])\n",
		"local shadow":          "def main(item):\n    gate = lambda run: struct(haed = 1)\n    x = gate(run = [\"a\"])\n    command([x.haed])\n",
	}

	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Empty(t, checkText(t, text))
		})
	}
}

func TestCheck_ALoadedNameIsLeftToTheRun(t *testing.T) {
	t.Parallel()

	found, _ := workflow.Check(folder(t, map[string]string{
		"workflow.star": "load(\"//lib.star\", \"x\")\n\ndef main(item):\n    command([x.haed])\n",
		"lib.star":      "x = struct(haed = 1)\n",
	}, "workflow.star"))

	assert.Empty(t, problems(found))
}

func TestCheck_ATopLevelStatementThatIsNotAFunctionIsSkipped(t *testing.T) {
	t.Parallel()

	got := checkText(t, "LIMIT = 3\n\ndef main(item):\n    x = command([\"a\"])\n    command([x.haed])\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "haed is not a field of the result of command")
}

func TestCheck_AFieldThatFactsDoesNotReturnIsRefused(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    f = facts()\n    command([f.limits[0], f.limit])\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "limit is not a field of the result of facts")
}

func TestResults_ListsTheBuiltinsWithFixedFields(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"agent", "batch_merge", "board_sync", "command", "facts", "gate", "gather", "host_teardown", "host_up", "item_assign", "item_comment", "item_create", "item_status", "merge", "pr_assign", "pr_comment", "pr_open", "preflight", "queue", "spawn", "wait", "write"}, workflow.Results())
}

func TestResultFields_ReturnACopyAndNoFieldsForAnotherName(t *testing.T) {
	t.Parallel()

	_, ok := workflow.ResultFields("nope")
	assert.False(t, ok)

	fields, ok := workflow.ResultFields("command")
	assert.True(t, ok)

	fields[0] = "changed"

	again, _ := workflow.ResultFields("command")
	assert.Equal(t, []string{"stdout", "stderr", "rc"}, again)
}
