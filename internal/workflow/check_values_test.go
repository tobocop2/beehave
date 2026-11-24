package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheck_RefusesTheLiteralValuesThatTheRunRefuses(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, call, want string }{
		{"output_tries zero", `agent("p", "m", output_tries = 0)`, "agent: output_tries must be a whole number of 1 or more"},
		{"output_tries negative", `agent("p", "m", output_tries = -2)`, "agent: output_tries must be a whole number of 1 or more"},
		{"agent timeout", `agent("p", "m", timeout = "soon")`, `agent: timeout is "soon", which is not a duration`},
		{"agent idle_timeout", `agent("p", "m", idle_timeout = "10")`, `agent: idle_timeout is "10", which is not a duration`},
		{"agent max_budget_usd negative", `agent("p", "m", max_budget_usd = -1)`, "agent: max_budget_usd must be 0 (no cap) or more, in dollars"},
		{"agent max_budget_usd text", `agent("p", "m", max_budget_usd = "5")`, "agent: max_budget_usd must be a number of dollars"},
		{"agent max_budget_usd none", `agent("p", "m", max_budget_usd = None)`, "agent: max_budget_usd must be a number of dollars"},
		{"agent max_context_percent over 100", `agent("p", "m", max_context_percent = 101)`, "agent: max_context_percent must be a whole number from 0 (no limit) to 100"},
		{"agent max_context_percent negative", `agent("p", "m", max_context_percent = -1)`, "agent: max_context_percent must be a whole number from 0 (no limit) to 100"},
		{"command timeout", `command(run = ["x"], timeout = "1 hour")`, `command: timeout is "1 hour", which is not a duration`},
		{"gate timeout", `gate(timeout = "later")`, `gate: timeout is "later", which is not a duration`},
		{"wait timeout text", `wait(question = "q", timeout = "soon")`, `wait: timeout is "soon", which is not a duration`},
		{"wait timeout zero", `wait(question = "q", timeout = "0s")`, `wait: timeout is "0s", which must be above zero`},
		{"wait timeout bare zero", `wait(question = "q", timeout = "0")`, `wait: timeout is "0", which must be above zero`},
		{"recv topic", `recv("Bad Topic", timeout = "1m")`, `recv: topic "Bad Topic" is not allowed`},
		{"recv topic reserved", `recv("beehave.answer", timeout = "1m")`, `recv: topic "beehave.answer" is reserved for beehave`},
		{"recv timeout zero", `recv("go", timeout = "0s")`, `recv: timeout is "0s", which must be above zero`},
		{"send topic", `send(to = "parent", topic = "A", value = 1)`, `send: topic "A" is not allowed`},
		{"send to other text", `send(to = "boss", topic = "t", value = 1)`, `send: to is "boss". Give "parent", "item:<id>" with the id of an item, or a run that spawn returned`},
		{"send to an item with no id", `send(to = "item:", topic = "t", value = 1)`, `send: to is "item:". Give "parent", "item:<id>"`},
		{"send to an item with a bad id", `send(to = "item:not an item", topic = "t", value = 1)`, `send: to is "item:not an item". Give "parent", "item:<id>" with the id of an item`},
		{"wait timeout negative", `wait(question = "q", timeout = "-5s")`, `wait: timeout is "-5s", which must be above zero`},
		{"wait timeout empty", `wait(question = "q", timeout = "")`, `wait: timeout is "", which must be above zero`},
		{"wait to empty", `wait(question = "q", to = [])`, "to is an empty list"},
		{"wait to element", `wait(question = "q", to = [1])`, "to must hold strings only"},
		{"wait to twice", `wait(question = "q", to = ["a", "a"])`, `to holds "a" twice`},
		{"wait to blank", `wait(question = "q", to = [""])`, `to holds "", which is empty`},
		{"wait remind zero", `wait(question = "q", to = ["a"], timeout = "1h", remind = [0])`, "wait: remind holds 0, which is not a share of the window"},
		{"wait remind one", `wait(question = "q", to = ["a"], timeout = "1h", remind = [0.5, 1.0])`, "remind holds 1.0, which is not a share of the window"},
		{"wait remind percent", `wait(question = "q", to = ["a"], timeout = "1h", remind = [50])`, "remind holds 50, which is not a share of the window"},
		{"wait remind text", `wait(question = "q", to = ["a"], timeout = "1h", remind = ["half"])`, `remind holds "half", which is not a share of the window`},
		{"wait remind order", `wait(question = "q", to = ["a"], timeout = "1h", remind = [0.9, 0.5])`, "remind holds 0.5 after 0.9. List the shares from the smallest to the largest"},
		{"wait remind twice", `wait(question = "q", to = ["a"], timeout = "1h", remind = [0.5, 0.5])`, "remind holds 0.5 twice"},
		{"wait remind empty", `wait(question = "q", to = ["a"], timeout = "1h", remind = [])`, "remind is an empty list"},
		{"wait remind not a list", `wait(question = "q", to = ["a"], timeout = "1h", remind = "half")`, "remind must be a list, not a text"},
		{"wait remind without timeout", `wait(question = "q", to = ["a"], remind = [0.5])`, "wait: remind needs `timeout`, the window that the shares divide"},
		{"wait remind without to", `wait(question = "q", timeout = "1h", remind = [0.5])`, "wait: remind needs `to`, the people who get the reminders"},
		{"wait choices empty", `wait(question = "q", choices = [])`, "choices is an empty list"},
		{"wait choices twice", `wait(question = "q", choices = ["y", "y"])`, `choices holds "y" twice`},
		{"wait quorum zero", `wait(question = "q", to = ["a"], quorum = 0)`, "wait: quorum must be a whole number of 1 or more"},
		{"wait quorum text", `wait(question = "q", to = ["a"], quorum = "2")`, "quorum must be an int, not a text"},
		{"wait quorum without to", `wait(question = "q", quorum = 2)`, "wait: quorum needs `to`"},
		{"wait quorum above to", `wait(question = "q", to = ["a", "b"], quorum = 3)`, "wait: quorum is 3, and `to` names 2 people"},
		{"stop_on_rc zero", `command(run = ["x"], stop_on_rc = [0])`, "command: stop_on_rc must hold whole numbers from 1 to 255"},
		{"stop_on_rc text", `command(run = ["x"], stop_on_rc = ["2"])`, "command: stop_on_rc must hold whole numbers from 1 to 255"},
		{"stop_on_rc large", `command(run = ["x"], stop_on_rc = [256])`, "command: stop_on_rc must hold whole numbers from 1 to 255"},
		{"allow_paths empty", `agent("p", "m", allow_paths = [])`, "agent: allow_paths is an empty list"},
		{"allow_paths number", `agent("p", "m", allow_paths = [1])`, "agent: allow_paths must hold strings only"},
		{"allow_paths glob", `agent("p", "m", allow_paths = ["[a"])`, `agent: allow_paths has the pattern "[a", which does not parse`},
		{"paths_outside allow absolute", `paths_outside(allow = ["/x"], paths = [])`, `paths_outside: allow has the absolute pattern "/x"`},
		{"paths_outside allow empty", `paths_outside(allow = [], paths = [])`, "paths_outside: allow is an empty list"},
		{"paths_outside allow none", `paths_outside(allow = None, paths = [])`, "paths_outside: allow is None, which names no scope. To check no scope, leave out the call of paths_outside"},
		{"outputs element", `agent("p", "m", outputs = [1])`, "agent: outputs must hold strings only"},
		{"protect_paths element", `agent("p", "m", protect_paths = [1])`, "agent: protect_paths must hold strings only"},
		{"inputs key", `agent("p", "m", inputs = {1: "a"})`, "agent: a value cannot go into a prompt or a request"},
		{"data list", `agent("p", "m", data = ["a"])`, "data must be a dict, not a list"},
		{"data key", `agent("p", "m", data = {1: "a"})`, "agent: a value cannot go into a prompt or a request"},
		{"max_new_processes with host", `command(run = ["x"], host = "h", max_new_processes = 5)`, "command: max_new_processes cannot be used together with host"},
		{"max_new_processes none", `command(run = ["x"], max_new_processes = None)`, "command: max_new_processes must be a whole number from 1 to 100000"},
		{"max_new_processes fraction", `command(run = ["x"], max_new_processes = 2.5)`, "command: max_new_processes must be a whole number from 1 to 100000"},
		{"slots without host", `command(run = ["x"], slots = "mutation")`, "command: slots cannot be used without host"},
		{"slots with an empty host", `command(run = ["x"], slots = "mutation", host = "")`, "command: slots cannot be used without host"},
		{"slots blank", `command(run = ["x"], slots = " ", host = "h")`, "command: slots must be the name of a slot class"},
		{"slots none", `command(run = ["x"], slots = None, host = "h")`, "command: slots must be the name of a slot class"},
		{"timeout none", `command(run = ["x"], timeout = None)`, `command: for parameter "timeout": got NoneType, want string`},
		{"output_tries none", `agent("p", "m", output_tries = None)`, `agent: for parameter "output_tries": got NoneType, want int`},
		{"max_new_processes range", `command(run = ["x"], max_new_processes = 0)`, "command: max_new_processes must be a whole number from 1 to 100000"},
		{"command run element", `command(run = ["x", 1])`, "command: run must hold strings only"},
		{"gate run element", `gate(run = [1])`, "gate: run must hold strings only"},
		{"merge forbid_text element", `merge(prs = x, base = "b", repo = "r", name = "n", forbid_text = [1])`, "merge: forbid_text must hold strings only"},
		{"merge gate_run element", `merge(prs = x, base = "b", repo = "r", name = "n", gate_run = [1])`, "merge: gate_run must hold strings only"},
		{"merge forbid_text pattern", `merge(prs = x, base = "b", repo = "r", name = "n", forbid_text = ["("])`, `merge: the forbid_text pattern "(" is not valid`},
		{"item_create forbid_text element", `item_create(title = "t", key = "k", forbid_text = [1])`, "item_create: forbid_text must hold strings only"},
		{"item_create forbid_text pattern", `item_create(title = "t", key = "k", forbid_text = ["("])`, `item_create: the forbid_text pattern "(" is not valid`},
		{"item_comment forbid_text element", `item_comment(id = "1", text = "t", key = "k", forbid_text = [1])`, "item_comment: forbid_text must hold strings only"},
		{"item_comment forbid_text pattern", `item_comment(id = "1", text = "t", key = "k", forbid_text = ["("])`, `item_comment: the forbid_text pattern "(" is not valid`},
		{"pr_comment forbid_text element", `pr_comment(text = "t", key = "k", forbid_text = [1])`, "pr_comment: forbid_text must hold strings only"},
		{"pr_comment forbid_text pattern", `pr_comment(text = "t", key = "k", forbid_text = ["("])`, `pr_comment: the forbid_text pattern "(" is not valid`},
		{"pr_open forbid_text element", `pr_open(title = "t", base = "b", forbid_text = [1])`, "pr_open: forbid_text must hold strings only"},
		{"pr_open forbid_text pattern", `pr_open(title = "t", base = "b", forbid_text = ["("])`, `pr_open: the forbid_text pattern "(" is not valid`},
		{"spawn items element", `spawn(items = [1])`, "spawn: items must hold strings only"},
		{"command host none", `command(run = ["x"], host = None)`, `command: for parameter "host": got NoneType, want string`},
		{"gate host none", `gate(run = ["x"], host = None)`, `gate: for parameter "host": got NoneType, want string`},
		{"merge hold none", `merge(prs = x, base = "b", repo = "r", name = "n", hold = None)`, `merge: for parameter "hold": got NoneType, want string`},
		{"agent edits none", `agent("p", "m", edits = None)`, `agent: for parameter "edits": got NoneType, want bool`},
		{"output_tries beyond 64 bits", `agent("p", "m", output_tries = 1180591620717411303424)`, `agent: for parameter "output_tries": 1180591620717411303424 out of range`},
		{"output_tries negative beyond 64 bits", `agent("p", "m", output_tries = -1180591620717411303424)`, `agent: for parameter "output_tries": -1180591620717411303424 out of range`},
		{"integration beyond 64 bits", `merge(prs = x, base = "b", repo = "r", name = "n", integration = 1180591620717411303424)`, `merge: for parameter "integration": 1180591620717411303424 out of range`},
		{"merge prs empty", `merge(prs = [], base = "b", repo = "r", name = "n")`, "merge: `prs` is empty"},
		{"merge prs none", `merge(prs = None, base = "b", repo = "r", name = "n")`, "merge: `prs` is empty"},
		{"merge prs element", `merge(prs = [1], base = "b", repo = "r", name = "n")`, "merge: prs[0]: the member has the type int, not struct"},
		{"merge prs dict element", `merge(prs = [{"item": "a", "head": "h"}], base = "b", repo = "r", name = "n")`, "merge: prs[0]: the member has the type dict, not struct"},
		{"merge own logins element", `merge(prs = x, base = "b", repo = "r", name = "n", own = {"logins": [1]})`, "merge: own.logins must hold strings only"},
		{"merge own emails text", `merge(prs = x, base = "b", repo = "r", name = "n", own = {"emails": "a@b"})`, "merge: own.emails must be a list of strings"},
		{"merge own element", `merge(prs = x, base = "b", repo = "r", name = "n", own = {"emails": [None]})`, "merge: own.emails must hold strings only"},
		{"command retries negative", `command(run = ["x"], retries = -1)`, "command: retries is -1. Use a whole number from 0 to 10"},
		{"command retries above 10", `command(run = ["x"], retries = 11)`, "command: retries is 11. Use a whole number from 0 to 10"},
		{"command backoff zero", `command(run = ["x"], retries = 1, backoff = "0s")`, `command: backoff is "0s", which must be above zero`},
		{"command backoff text", `command(run = ["x"], retries = 1, backoff = "soon")`, `command: backoff is "soon", which is not a duration`},
		{"command backoff alone", `command(run = ["x"], backoff = "1s")`, "command: backoff is set, but the call has no retries"},
		{"command backoff with no retry", `command(run = ["x"], retries = 0, backoff = "1s")`, "command: backoff is set, but the call has no retries"},
		{"command retries with slots", `command(run = ["x"], host = "h", slots = "big", retries = 1)`, "command: retries cannot be used with slots"},
		{"command failure", `command(run = ["x"], failure = "ignore")`, "failure"},
		{"write retries", `write(run = ["x"], retries = 11)`, "write: retries is 11"},
		{"write backoff alone", `write(run = ["x"], backoff = "1s")`, "write: backoff is set, but the call has no retries"},
		{"spawn retries", `spawn(items = ["a"], retries = 11)`, "spawn: retries is 11"},
		{"agent retries", `agent("p", "m", retries = 1)`, "agent takes no argument retries"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "x = []\n\ndef main(item):\n    "+tc.call+"\n")

			require.NotEmpty(t, got)
			assert.Contains(t, got[0], tc.want)
		})
	}
}

func TestCheck_AcceptsTheLiteralValuesThatTheRunAccepts(t *testing.T) {
	t.Parallel()

	got := checkText(t, "D = \"5s\"\nwho = [\"a\"]\n\ndef main(item):\n"+
		"    agent(\"p\", \"m\", output_tries = 3, timeout = \"90s\", idle_timeout = \"10m\", allow_paths = [\"a/**\", \"./b\"], outputs = [\"o\"], inputs = {\"k\": [1, 2.5, None]})\n"+
		"    agent(\"p\", \"m\", output_tries = 1, timeout = D, allow_paths = [D])\n"+
		"    agent(\"p\", \"m\", max_budget_usd = 2.5)\n"+
		"    agent(\"p\", \"m\", max_budget_usd = 0)\n"+
		"    agent(\"p\", \"m\", max_context_percent = 0)\n"+
		"    agent(\"p\", \"m\", max_context_percent = 80)\n"+
		"    agent(\"p\", \"m\", max_context_percent = 100)\n"+
		"    agent(\"p\", \"m\", label = \"fix 1/5\")\n"+
		"    agent(\"p\", \"m\", data = {\"base_reds\": [\"a\"]})\n"+
		"    agent(\"p\", \"m\", data = None)\n"+
		"    command(run = [\"x\"], stop_on_rc = [1, 255], max_new_processes = 5, timeout = \"\")\n"+
		"    wait(question = \"q\", timeout = \"48h\")\n"+
		"    wait(question = \"q\", to = [\"a\", \"b\"], quorum = 2, choices = [\"yes\", \"no\"], timeout = \"1h\")\n"+
		"    wait(question = \"q\", choices = [\"yes\"])\n"+
		"    wait(question = \"q\", to = [\"a\"], timeout = \"1h\", remind = [0.5, 0.9])\n"+
		"    wait(question = \"q\", to = who, timeout = D, remind = (0.25,))\n"+
		"    wait(question = \"q\", to = who, quorum = 3)\n"+
		"    command(run = [\"x\"], host = \"h\")\n"+
		"    command(run = [\"x\"], max_new_processes = 5, host = \"\")\n"+
		"    command(run = [\"x\"], host = \"h\", slots = \"mutation\")\n"+
		"    command(run = [\"x\"], host = who[0], slots = D)\n"+
		"    command(run = [\"x\"], retries = 10, backoff = \"2s\", failure = \"return\")\n"+
		"    command(run = [\"x\"], retries = 0, failure = \"raise\")\n"+
		"    command(run = [\"x\"], retries = len(who), backoff = D)\n"+
		"    command(run = [\"x\"], host = \"h\", slots = \"mutation\", retries = 0)\n"+
		"    write(run = [\"x\"], retries = 2, backoff = \"1s\")\n"+
		"    spawn(items = [\"a\"], retries = 3)\n"+
		"    merge(prs = [struct(item = \"a\", head = \"h\")], base = \"b\", repo = \"r\", name = \"n\", integration = 7, own = {})\n"+
		"    preflight(head = \"h\", base = \"b\")\n")

	assert.Empty(t, got)
}

func TestCheck_AcceptsEachTargetOfSend(t *testing.T) {
	t.Parallel()

	got := checkText(t, "who = \"item:3\"\n\ndef main(item):\n"+
		"    k = spawn(items = [\"a\"])\n"+
		"    send(to = \"parent\", topic = \"t\", value = 1)\n"+
		"    send(to = \"item:21\", topic = \"t\", value = 1)\n"+
		"    send(to = \"item:fix-login\", topic = \"t\", value = 1)\n"+
		"    send(to = who, topic = \"t\", value = 1)\n"+
		"    send(to = \"item:\" + who, topic = \"t\", value = 1)\n"+
		"    send(to = k.runs[0], topic = \"t\", value = 1)\n")

	assert.Empty(t, got)
}

func TestCheck_ASlotsValueOfTheWrongKindGetsTheKindErrorAndNoSecondOne(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    command(run = [\"x\"], host = \"h\", slots = 5)\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "slots must be a text, not an int")
}

func TestCheck_ReportsOneErrorForOneArgument(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    command(run = [\"x\"], max_new_processes = -1, host = \"h\")\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "command: max_new_processes cannot be used together with host")
}
