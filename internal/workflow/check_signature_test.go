package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/workflow"
)

func TestCheck_ReportsEachBuiltinCallThatBreaksItsSignature(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, call, want string }{
		{"agent unknown keyword", `agent(prompt = "p", model = "m", stop_on_code = 1)`, "agent takes no argument stop_on_code. Use one of: prompt, model,"},
		{"agent missing prompt", `agent(model = "m")`, "agent needs the `prompt` keyword"},
		{"agent missing model", `agent(prompt = "p")`, "agent needs the `model` keyword"},
		{"agent bad follow_ups", `agent(prompt = "p", model = "m", follow_ups = "maybe")`, `follow_ups is "maybe". Use one of: allow, forbid.`},
		{"agent text for a list", `agent(prompt = "p", model = "m", outputs = "verdict")`, "outputs must be a list, not a text."},
		{"agent text for the allowed paths", `agent(prompt = "p", model = "m", allow_paths = "papers/**")`, "allow_paths must be a list, not a text."},
		{"agent text for a number", `agent(prompt = "p", model = "m", output_tries = "2")`, "output_tries must be an int, not a text."},
		{"batch_merge unknown keyword", `batch_merge(prs = [], base = "b", repo = "r", name = "n", limit = 2)`, "batch_merge takes no argument limit"},
		{"batch_merge missing base", `batch_merge(prs = [], repo = "r", name = "n")`, "batch_merge needs the `base` keyword"},
		{"command unknown keyword", `command(run = ["a"], stop_on_code = [1])`, "command takes no argument stop_on_code. Use one of: run, host, stdin, env, timeout, stop_on_rc, name."},
		{"command missing run", `command(name = "n")`, "command needs the `run` keyword"},
		{"command text for a list", `command(run = "ls")`, "run must be a list, not a text."},
		{"command list for a dict", `command(run = ["a"], env = ["A=b"])`, "env must be a dict, not a list."},
		{"env unknown keyword", `env(name = "A", value = "b")`, "env takes no argument value"},
		{"env missing name", `env()`, "env needs the `name` keyword"},
		{"gate unknown keyword", `gate(runs = ["a"])`, "gate takes no argument runs"},
		{"gate text for a list", `gate(run = "make check")`, "run must be a list, not a text."},
		{"queue unknown keyword", `queue(name = "n", limit = 2)`, "queue takes no argument limit. Use one of: name, hold."},
		{"queue number for hold", `queue(name = "n", hold = 3)`, "hold must be a text, not an int."},
		{"gather unknown keyword", `gather([], wait = True)`, "gather takes no argument wait"},
		{"gather missing runs", `gather(fail = "raise")`, "gather needs the `runs` keyword"},
		{"gather bad fail", `gather([], fail = "ignore")`, `fail is "ignore". Use one of: return, raise.`},
		{"gather bad until", `gather([], until = "first")`, `until is "first". Use one of: all, any.`},
		{"gather number for until", `gather([], until = 1)`, "until is 1. Use one of: all, any."},
		{"gather number for fail", `gather([], fail = 3)`, "fail is 3. Use one of: return, raise."},
		{"gather list for fail", `gather([], fail = ["raise"])`, "fail is list. Use one of: return, raise."},
		{"item_comment unknown keyword", `item_comment("1", "t", "k", body = "b")`, "item_comment takes no argument body"},
		{"item_comment missing key", `item_comment("1", "t")`, "item_comment needs the `key` keyword"},
		{"item_create unknown keyword", `item_create("t", "k", label = "x")`, "item_create takes no argument label"},
		{"item_create missing key", `item_create(title = "t")`, "item_create needs the `key` keyword"},
		{"item_status unknown keyword", `item_status("1", "open", state = "x")`, "item_status takes no argument state"},
		{"item_status missing status", `item_status(id = "1")`, "item_status needs the `status` keyword"},
		{"item_status bad status", `item_status("1", "done")`, `status is "done". Use one of: open, closed, hold.`},
		{"item_status number for status", `item_status("1", 3)`, "status is 3. Use one of: open, closed, hold."},
		{"merge unknown keyword", `merge("h", force = True)`, "merge takes no argument force"},
		{"merge missing head", `merge(repo = "r")`, "merge needs the `head` keyword"},
		{"merge bad children", `merge("h", children = "drop")`, `children is "drop". Use one of: refuse, retarget.`},
		{"merge text for a bool", `merge("h", merge = "yes")`, "merge must be a bool, not a text."},
		{"preflight unknown keyword", `preflight(state = "open")`, "preflight takes no argument state"},
		{"preflight number for head", `preflight(head = 3)`, "head must be a text, not an int."},
		{"preflight list for hold", `preflight(hold = ["hold"])`, "hold must be a text, not a list."},
		{"spawn unknown keyword", `spawn([], limit = 2)`, "spawn takes no argument limit"},
		{"spawn missing items", `spawn(name = "n")`, "spawn needs the `items` keyword"},
		{"spawn text for a list", `spawn("1")`, "items must be a list, not a text."},
		{"wait unknown keyword", `wait(ask = "q")`, "wait takes no argument ask"},
		{"wait number for text", `wait(question = 3)`, "question must be a text, not an int."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    "+tc.call+"\n")

			require.NotEmpty(t, got)
			assert.Contains(t, got[0], tc.want)
		})
	}
}

func TestCheck_AcceptsACallThatKeepsItsSignature(t *testing.T) {
	t.Parallel()

	calls := []string{
		`agent("p", "m", follow_ups = "forbid", outputs = ["verdict"], output_tries = 2, inputs = {"a": 1}, allow_paths = ["papers/**"])`,
		`batch_merge([], "b", "r", name = "n", gate_run = ["make"])`,
		`command(["a"], env = {"A": "b"}, stop_on_rc = [1], name = "n")`,
		`env("HOME")`,
		`gate(run = ["a"], head = "h")`,
		`gather([], "raise")`,
		`item_comment("1", "t", "k")`,
		`item_create("t", "k", body = "b")`,
		`item_status("1", "hold")`,
		`merge("h", children = "retarget", approved = True, own = {"logins": []})`,
		`preflight(head = "h", base = "b", hold = "hold", name = "intake")`,
		`spawn(["1"], name = "n")`,
		`wait()`,
	}

	for _, call := range calls {
		assert.Empty(t, checkText(t, "def main(item):\n    "+call+"\n"), call)
	}
}

func TestCheck_LeavesAValueThatIsNotALiteralToTheRun(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    run = [\"a\"]\n    status = \"open\"\n    command(run = run, env = struct(a = 1))\n    item_status(\"1\", status)\n    item_status(\"1\", status + \"x\")\n    command(*run)\n    gate(**{\"run\": run})\n    command(**{})\n")

	assert.Empty(t, got)
}

func TestCheck_LeavesACallOfAShadowingFunctionAlone(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def merge(a, b, c):\n    pass\n\ndef main(item):\n    merge(1, 2, 3)\n    merge(a = 1)\n")

	assert.Empty(t, got)
}

func TestSignature_ListsEveryBuiltinThatTakesKeywords(t *testing.T) {
	t.Parallel()

	for _, name := range workflow.Builtins() {
		_, signed := workflow.Signature(name)
		assert.Equal(t, name != "struct" && name != "json", signed, name)
	}

	assert.Len(t, workflow.Signed(), len(workflow.Builtins())-2)

	_, signed := workflow.Signature("nope")
	assert.False(t, signed)
}

func TestUnpack_ReadsTheKeywordsOfTheSignature(t *testing.T) {
	t.Parallel()

	var (
		run, env, codes             starlark.Value
		timeout, host, stdin, label string
	)

	kwargs := []starlark.Tuple{{starlark.String("name"), starlark.String("n")}}
	err := workflow.Unpack("command", starlark.Tuple{starlark.NewList(nil)}, kwargs, map[string]any{"run": &run, "host": &host, "stdin": &stdin, "env": &env, "timeout": &timeout, "stop_on_rc": &codes, "name": &label})

	require.NoError(t, err)
	assert.Equal(t, "n", label)

	kwargs = []starlark.Tuple{{starlark.String("stop_on_code"), starlark.MakeInt(1)}}
	err = workflow.Unpack("command", starlark.Tuple{starlark.NewList(nil)}, kwargs, map[string]any{"run": &run, "host": &host, "stdin": &stdin, "env": &env, "timeout": &timeout, "stop_on_rc": &codes, "name": &label})

	assert.ErrorContains(t, err, `unexpected keyword argument "stop_on_code"`)
}

func TestUnpack_BindsEachDestinationByItsKeywordNotByItsPlace(t *testing.T) {
	t.Parallel()

	var (
		run                    starlark.Value
		host, timeout, head, n string
	)

	kwargs := []starlark.Tuple{{starlark.String("timeout"), starlark.String("5m")}, {starlark.String("head"), starlark.String("abc")}}
	err := workflow.Unpack("gate", nil, kwargs, map[string]any{"head": &head, "name": &n, "timeout": &timeout, "host": &host, "run": &run})

	require.NoError(t, err)
	assert.Equal(t, "5m", timeout)
	assert.Equal(t, "abc", head)
}

func TestUnpack_RefusesDestinationsThatDifferFromTheSignature(t *testing.T) {
	t.Parallel()

	var one, two starlark.Value

	const want = "destinations that differ from the keywords of its signature"

	require.ErrorContains(t, workflow.Unpack("command", nil, nil, map[string]any{"run": &one}), want)
	require.ErrorContains(t, workflow.Unpack("wait", nil, nil, map[string]any{"question": &one, "label": &two}), want)
	assert.ErrorContains(t, workflow.Unpack("nope", nil, nil, nil), want)
}
