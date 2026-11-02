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
		{"command timeout", `command(run = ["x"], timeout = "1 hour")`, `command: timeout is "1 hour", which is not a duration`},
		{"gate timeout", `gate(timeout = "later")`, `gate: timeout is "later", which is not a duration`},
		{"wait timeout text", `wait(question = "q", timeout = "soon")`, `wait: timeout is "soon", which is not a duration`},
		{"wait timeout zero", `wait(question = "q", timeout = "0s")`, `wait: timeout is "0s", which must be above zero`},
		{"wait timeout bare zero", `wait(question = "q", timeout = "0")`, `wait: timeout is "0", which must be above zero`},
		{"wait timeout negative", `wait(question = "q", timeout = "-5s")`, `wait: timeout is "-5s", which must be above zero`},
		{"wait timeout empty", `wait(question = "q", timeout = "")`, `wait: timeout is "", which must be above zero`},
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

	got := checkText(t, "D = \"5s\"\n\ndef main(item):\n"+
		"    agent(\"p\", \"m\", output_tries = 3, timeout = \"90s\", idle_timeout = \"10m\", allow_paths = [\"a/**\", \"./b\"], outputs = [\"o\"], require_skills = [\"s\"], inputs = {\"k\": [1, 2.5, None]})\n"+
		"    agent(\"p\", \"m\", output_tries = 1, timeout = D, allow_paths = [D])\n"+
		"    command(run = [\"x\"], stop_on_rc = [1, 255], max_new_processes = 5, timeout = \"\")\n"+
		"    wait(question = \"q\", timeout = \"48h\")\n"+
		"    command(run = [\"x\"], host = \"h\")\n"+
		"    command(run = [\"x\"], max_new_processes = 5, host = \"\")\n")

	assert.Empty(t, got)
}

func TestCheck_ReportsOneErrorForOneArgument(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    command(run = [\"x\"], max_new_processes = -1, host = \"h\")\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "command: max_new_processes cannot be used together with host")
}
