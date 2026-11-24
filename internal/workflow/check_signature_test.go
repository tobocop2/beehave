package workflow_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

func TestCheck_ReportsEachBuiltinCallThatBreaksItsSignature(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, call, want string }{
		{"agent unknown keyword", `agent(prompt = "p", model = "m", stop_on_code = 1)`, "agent takes no argument stop_on_code. Use one of: prompt, model,"},
		{"now with an argument", `now(zone = "utc")`, "now takes no argument, and the call gives zone."},
		{"agent missing prompt", `agent(model = "m")`, "agent needs the `prompt` keyword"},
		{"agent missing model", `agent(prompt = "p")`, "agent needs the `model` keyword"},
		{"agent text for a list", `agent(prompt = "p", model = "m", outputs = "verdict")`, "outputs must be a list, not a text."},
		{"agent text for the allowed paths", `agent(prompt = "p", model = "m", allow_paths = "papers/**")`, "allow_paths must be a list, not a text."},
		{"agent text for the protected paths", `agent(prompt = "p", model = "m", protect_paths = "/own/checkout")`, "protect_paths must be a list, not a text."},
		{"agent text for the edits switch", `agent(prompt = "p", model = "m", edits = "no")`, "edits must be a bool, not a text."},
		{"agent text for a number", `agent(prompt = "p", model = "m", output_tries = "2")`, "output_tries must be an int, not a text."},
		{"agent skill that is a path", `agent(prompt = "p", model = "m", skill = "a/b")`, `"a/b" is not the name of a skill`},
		{"agent number for the skill", `agent(prompt = "p", model = "m", skill = 3)`, "skill must be a text, not an int."},
		{"merge unknown keyword", `merge(prs = item.id, base = "b", repo = "r", name = "n", limit = 2)`, "merge takes no argument limit"},
		{"merge missing base", `merge(prs = item.id, repo = "r", name = "n")`, "merge needs the `base` keyword"},
		{"command unknown keyword", `command(run = ["a"], stop_on_code = [1])`, "command takes no argument stop_on_code. Use one of: run, host, stdin, env, timeout, stop_on_rc, max_new_processes, slots, sandbox, name, retries, backoff, failure."},
		{"command cap as text", `command(run = ["a"], max_new_processes = "5")`, "max_new_processes must be an int, not a text."},
		{"command cap of zero", `command(run = ["a"], max_new_processes = 0)`, "command: max_new_processes must be a whole number from 1 to 100000."},
		{"command cap below zero", `command(run = ["a"], max_new_processes = -3)`, "command: max_new_processes must be a whole number from 1 to 100000."},
		{"command cap too large", `command(run = ["a"], max_new_processes = 100001)`, "command: max_new_processes must be a whole number from 1 to 100000."},
		{"command missing run", `command(name = "n")`, "command needs the `run` keyword"},
		{"command text for a list", `command(run = "ls")`, "run must be a list, not a text."},
		{"command list for a dict", `command(run = ["a"], env = ["A=b"])`, "env must be a dict, not a list."},
		{"env unknown keyword", `env(name = "A", value = "b")`, "env takes no argument value"},
		{"env missing name", `env()`, "env needs the `name` keyword"},
		{"gate unknown keyword", `gate(runs = ["a"])`, "gate takes no argument runs"},
		{"gate text for a list", `gate(run = "make check")`, "run must be a list, not a text."},
		{"queue unknown keyword", `queue(name = "n", limit = 2)`, "queue takes no argument limit. Use one of: name, hold, label."},
		{"queue items is no argument", `queue(name = "n", items = ["1"])`, "queue takes no argument items. Use one of: name, hold, label."},
		{"queue number for hold", `queue(name = "n", hold = 3)`, "hold must be a text, not an int."},
		{"gather unknown keyword", `gather([], wait = True)`, "gather takes no argument wait"},
		{"gather missing runs", `gather(fail = "raise")`, "gather needs the `runs` keyword"},
		{"gather bad fail", `gather([], fail = "ignore")`, `fail is "ignore". Use one of: return, raise.`},
		{"gather has no until", `gather([], until = "any")`, "gather takes no argument until"},
		{"first_of bad topics", `first_of(topics = "a")`, "topics must be a list"},
		{"first_of bad topic name", `first_of(topics = ["Bad Topic"])`, "topic"},
		{"first_of reserved topic", `first_of(topics = ["beehave.child"])`, "reserved"},
		{"first_of bad timeout", `first_of(timeout = "soon")`, "timeout"},
		{"first_of with no source", `first_of()`, "first_of needs"},
		{"gather number for fail", `gather([], fail = 3)`, "fail is 3. Use one of: return, raise."},
		{"gather negative number for fail", `gather([], fail = -3)`, "fail is -3. Use one of: return, raise."},
		{"gather list for fail", `gather([], fail = ["raise"])`, "fail is list. Use one of: return, raise."},
		{"item_comment unknown keyword", `item_comment("1", "t", "k", body = "b")`, "item_comment takes no argument body"},
		{"item_comment missing key", `item_comment("1", "t")`, "item_comment needs the `key` keyword"},
		{"item_create unknown keyword", `item_create("t", "k", label = "x")`, "item_create takes no argument label"},
		{"item_create missing key", `item_create(title = "t")`, "item_create needs the `key` keyword"},
		{"item_create text for shared", `item_create(title = "t", key = "k", shared = "yes")`, "shared must be a bool, not a text."},
		{"pr_state missing pr", `pr_state(name = "s")`, "pr_state needs the `pr` keyword"},
		{"pr_state text for pr", `pr_state(pr = "41")`, "pr must be an int, not a text."},
		{"item_status unknown keyword", `item_status("1", "open", state = "x")`, "item_status takes no argument state"},
		{"item_status missing status", `item_status(id = "1")`, "item_status needs the `status` keyword"},
		{"item_status bad status", `item_status("1", "done")`, `status is "done". Use one of: open, closed, hold.`},
		{"item_status number for status", `item_status("1", 3)`, "status is 3. Use one of: open, closed, hold."},
		{"preflight unknown keyword", `preflight(state = "open")`, "preflight takes no argument state"},
		{"preflight number for head", `preflight(head = 3)`, "head must be a text, not an int."},
		{"preflight list for head", `preflight(head = ["h"])`, "head must be a text, not a list."},
		{"preflight hold", `preflight(hold = "hold")`, "preflight takes no argument hold. The claim stops moved to the library function claim_stop: load(\"@beehave//claims.star\", \"claim_stop\") and pass hold to it."},
		{"preflight own", `preflight(own = {})`, "preflight takes no argument own. The claim stops moved to the library function claim_stop: load(\"@beehave//claims.star\", \"claim_stop\") and pass own to it."},
		{"preflight own_draft", `preflight(own_draft = 7)`, "preflight takes no argument own_draft. The draft stop moved to the library function claim_stop: load(\"@beehave//claims.star\", \"claim_stop\") and pass own_draft to it."},
		{"spawn unknown keyword", `spawn([], limit = 2)`, "spawn takes no argument limit"},
		{"spawn missing items", `spawn(name = "n")`, "spawn needs the `items` keyword"},
		{"spawn text for a list", `spawn("1")`, "items must be a list, not a text."},
		{"wait unknown keyword", `wait(ask = "q")`, "wait takes no argument ask"},
		{"wait number for text", `wait(question = 3)`, "question must be a text, not an int."},
		{"sleep missing duration", `sleep(name = "nap")`, "sleep needs the `duration` keyword"},
		{"sleep zero duration", `sleep("0s")`, "duration is \"0s\", which must be above zero"},
		{"sleep negative duration", `sleep("-5s")`, "duration is \"-5s\", which must be above zero"},
		{"sleep text that is no duration", `sleep("soon")`, "duration is \"soon\", which is not a duration"},
		{"sleep number for text", `sleep(5)`, "duration must be a text, not an int."},
		{"sleep unknown keyword", `sleep("1s", until = "x")`, "sleep takes no argument until"},
		{"patched missing name", `patched()`, "patched needs the `name` keyword"},
		{"patched empty name", `patched("")`, "name must not be empty"},
		{"patched name with a colon", `patched("a:b")`, `name "a:b" holds a character other than a letter, a digit, a dot, a dash or an underscore`},
		{"patched number for name", `patched(3)`, "name must be a text, not an int."},
		{"patched unknown keyword", `patched("a", version = 2)`, "patched takes no argument version"},
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
		`agent("p", "m", label = "fix 1/3", outputs = ["verdict"], output_tries = 2, inputs = {"a": 1}, allow_paths = ["papers/**"])`,
		`agent("p", "m", forbid_commands = ["protected-path"], protect_paths = ["/own/checkout"])`,
		`agent("p", "m", skill = "lint-pass")`,
		`agent("p", "m", skill = "")`,
		`merge(item.id, "b", "r", name = "n", gate_run = ["make"])`,
		`command(["a"], env = {"A": "b"}, stop_on_rc = [1], name = "n")`,
		`command(["a"], max_new_processes = 50)`,
		`command(["a"], max_new_processes = len(item))`,
		`command(["a"], max_new_processes = +3)`,
		`env("HOME")`,
		`gate(run = ["a"], head = "h")`,
		`gather([], "raise")`,
		`item_comment("1", "t", "k")`,
		`item_create("t", "k", body = "b")`,
		`item_status("1", "hold")`,
		`preflight(head = "h", base = "b", name = "intake")`,
		`preflight(id = "7", name = "before-fix-1")`,
		`queue(name = "n", hold = "hold", label = "beehave-round")`,
		`sleep("90s", name = "n")`,
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

func TestCheck_ChecksEachLiteralKindOfForbidCommands(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    agent(\"p\", \"m\", forbid_commands = [\"git-write\", \"nope\", 3])\n")

	require.Len(t, got, 2)
	assert.Contains(t, got[0], `forbid_commands has the unknown kind "nope". Use one of: bulk-staging,`)
	assert.Contains(t, got[1], "forbid_commands has the unknown kind 3. Use one of: bulk-staging,")
}

func TestCheck_ChecksAParenthesisedForbidCommands(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    agent(\"p\", \"m\", forbid_commands = (\"git-write\", \"nope\"))\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], `forbid_commands has the unknown kind "nope". Use one of: bulk-staging,`)
}

func TestCheck_LeavesANonLiteralForbidCommandsToTheRun(t *testing.T) {
	t.Parallel()

	got := checkText(t, "KINDS = [\"nope\"]\n\ndef main(item):\n    agent(\"p\", \"m\", forbid_commands = KINDS)\n    agent(\"p\", \"m\", forbid_commands = [\"git-write\"] + KINDS)\n    agent(\"p\", \"m\", forbid_commands = [KINDS[0], \"git-write\"])\n")

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
		run, env, codes, processes  starlark.Value
		timeout, host, stdin, label string
		slots, sandbox              starlark.Value
		retries                     int
		backoff, failure            string
	)

	into := map[string]any{
		"run": &run, "host": &host, "stdin": &stdin, "env": &env, "timeout": &timeout, "stop_on_rc": &codes, "max_new_processes": &processes, "slots": &slots, "sandbox": &sandbox, "name": &label,
		"retries": &retries, "backoff": &backoff, "failure": &failure,
	}

	kwargs := []starlark.Tuple{{starlark.String("name"), starlark.String("n")}}
	err := workflow.Unpack("command", starlark.Tuple{starlark.NewList(nil)}, kwargs, into)

	require.NoError(t, err)
	assert.Equal(t, "n", label)

	kwargs = []starlark.Tuple{{starlark.String("stop_on_code"), starlark.MakeInt(1)}}
	err = workflow.Unpack("command", starlark.Tuple{starlark.NewList(nil)}, kwargs, into)

	assert.ErrorContains(t, err, `unexpected keyword argument "stop_on_code"`)
}

func TestUnpack_BindsEachDestinationByItsKeywordNotByItsPlace(t *testing.T) {
	t.Parallel()

	var (
		run                                   starlark.Value
		host, timeout, head, base, pinBase, n string
	)

	kwargs := []starlark.Tuple{
		{starlark.String("timeout"), starlark.String("5m")},
		{starlark.String("head"), starlark.String("abc")},
		{starlark.String("base"), starlark.String("main")},
		{starlark.String("pin_base"), starlark.String("integration")},
	}
	err := workflow.Unpack("gate", nil, kwargs, map[string]any{"head": &head, "base": &base, "pin_base": &pinBase, "name": &n, "timeout": &timeout, "host": &host, "run": &run})

	require.NoError(t, err)
	assert.Equal(t, "5m", timeout)
	assert.Equal(t, "abc", head)
	assert.Equal(t, "main", base)
	assert.Equal(t, "integration", pinBase, "the pin base binds by its keyword and not into the base next to it")
}

func TestUnpack_RefusesDestinationsThatDifferFromTheSignature(t *testing.T) {
	t.Parallel()

	var one, two starlark.Value

	const want = "destinations that differ from the keywords of its signature"

	require.ErrorContains(t, workflow.Unpack("command", nil, nil, map[string]any{"run": &one}), want)
	require.ErrorContains(t, workflow.Unpack("wait", nil, nil, map[string]any{"question": &one, "timeout": &two, "other": &two}), want)
	assert.ErrorContains(t, workflow.Unpack("nope", nil, nil, nil), want)
}

func TestUnpack_RefusesADestinationOfAnotherTypeThanTheKindOfItsKeyword(t *testing.T) {
	t.Parallel()

	var (
		value starlark.Value
		text  string
		tries int
		edits bool
	)

	const want = "destinations that differ from the keywords of its signature"

	params, ok := workflow.Signature("agent")
	require.True(t, ok)

	// The destination of each keyword comes from the signature by the rule of Param: a Go text, number or flag for a typed keyword, else a Starlark value.
	typed := map[workflow.Kind]any{workflow.KindText: &text, workflow.KindInt: &tries, workflow.KindBool: &edits}

	agent := func(overrides map[string]any) error {
		into := map[string]any{}

		for _, param := range params {
			dest, found := typed[param.Kind]
			if !found || param.AsValue {
				dest = &value
			}

			into[param.Name] = dest
		}

		for name, dest := range overrides {
			into[name] = dest
		}

		return workflow.Unpack("agent", nil, nil, into)
	}

	assert.NotContains(t, agent(nil).Error(), want) // the right types reach the unpack, which then misses the required prompt
	require.ErrorContains(t, agent(map[string]any{"output_tries": &text}), want)
	require.ErrorContains(t, agent(map[string]any{"edits": &tries}), want)
	require.ErrorContains(t, agent(map[string]any{"prompt": &value}), want)
	require.ErrorContains(t, agent(map[string]any{"inputs": &text}), want)
}

func TestCheck_MergeTakesTheKeywordsOfABatchAndNoKeywordOfTheOldSingleMerge(t *testing.T) {
	t.Parallel()

	for _, keyword := range []string{`head = "abc"`, `tree = "t"`, `steps = []`, `approved = True`, `children = "refuse"`, `merge = False`, `by = "owner"`} {
		name, _, _ := strings.Cut(keyword, " ")

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    merge(name = \"land\", prs = [struct(item = \"1\", head = \"h\")], base = \"main\", repo = \"o/r\", "+keyword+")\n")

			require.NotEmpty(t, got)
			assert.Contains(t, got[0], "merge takes no argument "+name+". Use one of: prs, base, repo, hold, own, forbid_text, gate_run, integration, hand_off, name.")
		})
	}
}

func TestCheck_MergeNeedsTheNameKeywordAndAcceptsAMergeOfOnePullRequest(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    merge(prs = [struct(item = \"1\", head = \"h\")], base = \"main\", repo = \"o/r\")\n")

	require.NotEmpty(t, got)
	assert.Contains(t, got[0], "merge needs the `name` keyword")

	one := "def main(item):\n    out = merge(name = \"land\", prs = [struct(item = item.id, head = \"h\", approved = True)], base = \"main\", repo = \"o/r\")\n    print(out.merged)\n"
	assert.Empty(t, checkText(t, one), "a merge of one pull request is a merge")
}

func TestCheck_AFileThatCallsBatchMergeIsRefusedAsAnUndefinedName(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    batch_merge(name = \"land\", prs = [struct(item = \"1\", head = \"h\")], base = \"main\", repo = \"o/r\")\n")

	require.NotEmpty(t, got)
	assert.Contains(t, got[0], "batch_merge")
	assert.Contains(t, got[0], "undefined")
}

func TestCheck_TheResultOfMergeHasNoStateField(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    out = merge(name = \"land\", prs = [struct(item = \"1\", head = \"h\")], base = \"main\", repo = \"o/r\")\n    print(out.state)\n")

	require.NotEmpty(t, got)
	assert.Contains(t, got[0], "state is not a field of the result of merge")
}

func TestCheck_MergeTakesHandOffAsABool(t *testing.T) {
	t.Parallel()

	const call = "def main(item):\n    merge(name = \"land\", prs = [struct(item = \"1\", head = \"h\")], base = \"main\", repo = \"o/r\", hand_off = %s)\n"

	assert.Empty(t, checkText(t, fmt.Sprintf(call, "True")))

	got := checkText(t, fmt.Sprintf(call, `"yes"`))

	require.NotEmpty(t, got)
	assert.Contains(t, got[0], "hand_off")
}

func TestCheck_MergeWithHandOffRefusesEachKeywordOfALanding(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"hold":        `hold = "hold"`,
		"own":         `own = {"logins": ["me"]}`,
		"forbid_text": `forbid_text = ["x"]`,
		"gate_run":    `gate_run = ["check"]`,
		"integration": `integration = 5`,
	}

	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    merge(name = \"land\", prs = [struct(item = \"1\", head = \"h\")], base = \"main\", repo = \"o/r\", hand_off = True, "+extra+")\n")

			require.Len(t, got, 1)
			assert.Contains(t, got[0], "merge with `hand_off` lands nothing, so it takes no `"+name+"`")
		})
	}
}

func TestCheck_MergeWithHandOffFalseOrComputedTakesTheKeywordsOfALanding(t *testing.T) {
	t.Parallel()

	const call = "def main(item):\n    flag = item == \"x\"\n    merge(name = \"land\", prs = [struct(item = \"1\", head = \"h\")], base = \"main\", repo = \"o/r\", hand_off = %s, gate_run = [\"check\"])\n"

	assert.Empty(t, checkText(t, fmt.Sprintf(call, "False")), "a landing that is not handed off takes gate_run")
	assert.Empty(t, checkText(t, fmt.Sprintf(call, "flag")), "a flag that only the run knows is left to the run")
}
