package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheck_RefusesTheLiteralValuesThatTheRunRefuses(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, call, want string }{
		{"agent rounds zero", `agent("p", "m", rounds = 0)`, "agent: rounds must be a whole number of 1 or more"},
		{"agent rounds text", `agent("p", "m", rounds = "5")`, "agent: rounds must be a whole number of 1 or more"},
		{"agent rounds beyond 32 bits", `agent("p", "m", rounds = 1099511627776)`, "agent: rounds must be a whole number of 1 or more"},
		{"agent rounds beyond 64 bits", `agent("p", "m", rounds = 1180591620717411303424)`, "agent: rounds must be a whole number of 1 or more"},
		{"output_tries zero", `agent("p", "m", output_tries = 0)`, "agent: output_tries must be a whole number of 1 or more"},
		{"output_tries negative", `agent("p", "m", output_tries = -2)`, "agent: output_tries must be a whole number of 1 or more"},
		{"agent timeout", `agent("p", "m", timeout = "soon")`, `agent: timeout is "soon", which is not a duration`},
		{"agent idle_timeout", `agent("p", "m", idle_timeout = "10")`, `agent: idle_timeout is "10", which is not a duration`},
		{"agent max_budget_usd negative", `agent("p", "m", max_budget_usd = -1)`, "agent: max_budget_usd must be 0 (no cap) or more, in dollars"},
		{"agent max_budget_usd text", `agent("p", "m", max_budget_usd = "5")`, "agent: max_budget_usd must be a number of dollars"},
		{"agent max_budget_usd none", `agent("p", "m", max_budget_usd = None)`, "agent: max_budget_usd must be a number of dollars"},
		{"command timeout", `command(run = ["x"], timeout = "1 hour")`, `command: timeout is "1 hour", which is not a duration`},
		{"gate timeout", `gate(timeout = "later")`, `gate: timeout is "later", which is not a duration`},
		{"wait timeout text", `wait(question = "q", timeout = "soon")`, `wait: timeout is "soon", which is not a duration`},
		{"wait timeout zero", `wait(question = "q", timeout = "0s")`, `wait: timeout is "0s", which must be above zero`},
		{"wait timeout bare zero", `wait(question = "q", timeout = "0")`, `wait: timeout is "0", which must be above zero`},
		{"wait timeout negative", `wait(question = "q", timeout = "-5s")`, `wait: timeout is "-5s", which must be above zero`},
		{"wait timeout empty", `wait(question = "q", timeout = "")`, `wait: timeout is "", which must be above zero`},
		{"wait to empty", `wait(question = "q", to = [])`, "to is an empty list"},
		{"wait to element", `wait(question = "q", to = [1])`, "to must hold strings only"},
		{"wait to twice", `wait(question = "q", to = ["a", "a"])`, `to holds "a" twice`},
		{"wait to blank", `wait(question = "q", to = [""])`, `to holds "", which is empty`},
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
		{"outputs element", `agent("p", "m", outputs = [1])`, "agent: outputs must hold strings only"},
		{"require_skills element", `agent("p", "m", require_skills = [True])`, "agent: require_skills must hold strings only"},
		{"protect_paths element", `agent("p", "m", protect_paths = [1])`, "agent: protect_paths must hold strings only"},
		{"inputs key", `agent("p", "m", inputs = {1: "a"})`, "agent: a value cannot go into a prompt or a request"},
		{"max_new_processes with host", `command(run = ["x"], host = "h", max_new_processes = 5)`, "command: max_new_processes cannot be used together with host"},
		{"max_new_processes none", `command(run = ["x"], max_new_processes = None)`, "command: max_new_processes must be a whole number from 1 to 100000"},
		{"max_new_processes fraction", `command(run = ["x"], max_new_processes = 2.5)`, "command: max_new_processes must be a whole number from 1 to 100000"},
		{"timeout none", `command(run = ["x"], timeout = None)`, `command: for parameter "timeout": got NoneType, want string`},
		{"output_tries none", `agent("p", "m", output_tries = None)`, `agent: for parameter "output_tries": got NoneType, want int`},
		{"max_new_processes range", `command(run = ["x"], max_new_processes = 0)`, "command: max_new_processes must be a whole number from 1 to 100000"},
		{"command run element", `command(run = ["x", 1])`, "command: run must hold strings only"},
		{"gate run element", `gate(run = [1])`, "gate: run must hold strings only"},
		{"batch_merge forbid_text element", `batch_merge(prs = x, base = "b", repo = "r", name = "n", forbid_text = [1])`, "batch_merge: forbid_text must hold strings only"},
		{"batch_merge gate_run element", `batch_merge(prs = x, base = "b", repo = "r", name = "n", gate_run = [1])`, "batch_merge: gate_run must hold strings only"},
		{"merge forbid_text element", `merge(head = "h", forbid_text = [1])`, "merge: forbid_text must hold strings only"},
		{"spawn items element", `spawn(items = [1])`, "spawn: items must hold strings only"},
		{"command host none", `command(run = ["x"], host = None)`, `command: for parameter "host": got NoneType, want string`},
		{"gate host none", `gate(run = ["x"], host = None)`, `gate: for parameter "host": got NoneType, want string`},
		{"merge hold none", `merge(head = "h", hold = None)`, `merge: for parameter "hold": got NoneType, want string`},
		{"agent edits none", `agent("p", "m", edits = None)`, `agent: for parameter "edits": got NoneType, want bool`},
		{"output_tries beyond 64 bits", `agent("p", "m", output_tries = 1180591620717411303424)`, `agent: for parameter "output_tries": 1180591620717411303424 out of range`},
		{"output_tries negative beyond 64 bits", `agent("p", "m", output_tries = -1180591620717411303424)`, `agent: for parameter "output_tries": -1180591620717411303424 out of range`},
		{"integration beyond 64 bits", `batch_merge(prs = x, base = "b", repo = "r", name = "n", integration = 1180591620717411303424)`, `batch_merge: for parameter "integration": 1180591620717411303424 out of range`},
		{"merge steps element", `merge(head = "h", merge = True, steps = [1])`, "merge: `steps` must be the steps of a gate"},
		{"merge steps no status", `merge(head = "h", merge = True, steps = [{"name": "a"}])`, "merge: `steps` must be the steps of a gate"},
		{"merge steps none", `merge(head = "h", merge = True, steps = None)`, "merge: `steps` is empty"},
		{"merge steps failed", `merge(head = "h", merge = True, steps = [{"name": "a", "status": "fail"}])`, `merge: the gate step "a" is fail`},
		{"merge steps unknown", `merge(head = "h", merge = True, steps = [{"name": "a", "status": "pass"}, {"name": "b", "status": "unknown"}])`, `merge: the gate step "b" is unknown`},
		{"merge tree empty", `merge(head = "h", merge = True, steps = x, tree = "")`, "merge: `tree` is empty"},
		{"merge tree none", `merge(head = "h", merge = True, steps = x, tree = None)`, "merge: `tree` must be text, not NoneType"},
		{"batch_merge prs empty", `batch_merge(prs = [], base = "b", repo = "r", name = "n")`, "batch_merge: `prs` is empty"},
		{"batch_merge prs none", `batch_merge(prs = None, base = "b", repo = "r", name = "n")`, "batch_merge: `prs` is empty"},
		{"batch_merge prs element", `batch_merge(prs = [1], base = "b", repo = "r", name = "n")`, "batch_merge: `prs` holds a int"},
		{"batch_merge prs dict element", `batch_merge(prs = [{"item": "a", "head": "h"}], base = "b", repo = "r", name = "n")`, "batch_merge: `prs` holds a dict"},
		{"merge own logins element", `merge(head = "h", own = {"logins": [1]})`, "merge: own.logins must hold strings only"},
		{"merge own emails text", `merge(head = "h", own = {"emails": "a@b"})`, "merge: own.emails must be a list of strings"},
		{"batch_merge own element", `batch_merge(prs = x, base = "b", repo = "r", name = "n", own = {"emails": [None]})`, "batch_merge: own.emails must hold strings only"},
		{"preflight own element", `preflight(own = {"logins": [True]})`, "preflight: own.logins must hold strings only"},
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
		"    agent(\"p\", \"m\", output_tries = 3, timeout = \"90s\", idle_timeout = \"10m\", allow_paths = [\"a/**\", \"./b\"], outputs = [\"o\"], require_skills = [\"s\"], inputs = {\"k\": [1, 2.5, None]})\n"+
		"    agent(\"p\", \"m\", output_tries = 1, timeout = D, allow_paths = [D])\n"+
		"    agent(\"p\", \"m\", max_budget_usd = 2.5)\n"+
		"    agent(\"p\", \"m\", max_budget_usd = 0)\n"+
		"    agent(\"p\", \"m\", rounds = 5)\n"+
		"    command(run = [\"x\"], stop_on_rc = [1, 255], max_new_processes = 5, timeout = \"\")\n"+
		"    wait(question = \"q\", timeout = \"48h\")\n"+
		"    wait(question = \"q\", to = [\"a\", \"b\"], quorum = 2, choices = [\"yes\", \"no\"], timeout = \"1h\")\n"+
		"    wait(question = \"q\", choices = [\"yes\"])\n"+
		"    wait(question = \"q\", to = who, quorum = 3)\n"+
		"    command(run = [\"x\"], host = \"h\")\n"+
		"    command(run = [\"x\"], max_new_processes = 5, host = \"\")\n"+
		"    merge(head = \"h\", merge = True, steps = [{\"name\": \"a\", \"status\": \"pass\"}], tree = \"abc\", own = {\"logins\": [\"me\"], \"emails\": [\"a@b\"]})\n"+
		"    merge(head = \"h\", merge = False, steps = [1], tree = \"\", own = None)\n"+
		"    merge(head = \"h\", hold = \"h\")\n"+
		"    batch_merge(prs = [struct(item = \"a\", head = \"h\")], base = \"b\", repo = \"r\", name = \"n\", integration = 7, own = {})\n"+
		"    preflight(own = {\"logins\": [\"me\"]})\n")

	assert.Empty(t, got)
}

func TestCheck_ReportsOneErrorForOneArgument(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    command(run = [\"x\"], max_new_processes = -1, host = \"h\")\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "command: max_new_processes cannot be used together with host")
}
