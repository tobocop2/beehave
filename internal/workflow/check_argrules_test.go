package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheck_RefusesTheArgumentsOfTheRunThatAreText(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, call, want string }{
		{"command env value", `command(run = ["x"], env = {"A": 1})`, "command: env must hold strings only"},
		{"command env key", `command(run = ["x"], env = {1: "a"})`, "command: env must hold strings only"},
		{"command env reserved", `command(run = ["x"], env = {"BEEHAVE_X": "1"})`, "command: env cannot set `BEEHAVE_X`"},
		{"command env hands back the SSH agent", `command(run = ["x"], env = {"SSH_AUTH_SOCK": "/tmp/agent.sock"})`, "command: env cannot set `SSH_AUTH_SOCK`"},
		{"command env hands back the SSH agent from the environment", `command(run = ["x"], env = {"SSH_AUTH_SOCK": env("SSH_AUTH_SOCK")})`, "command: env cannot set `SSH_AUTH_SOCK`"},
		{"write env hands back the SSH agent", `write(run = ["x"], env = ({"SSH_AUTH_SOCK": str(item)}))`, "write: env cannot set `SSH_AUTH_SOCK`"},
		{"write env value", `write(run = ["x"], env = {"A": None})`, "write: env must hold strings only"},
		{"env beside a computed entry", `command(run = ["x"], env = {"A": str(item), "B": 1})`, "command: env must hold strings only"},
		{"env after a computed entry", `command(run = ["x"], env = {"B": 1, "A": str(item)})`, "command: env must hold strings only"},
		{"sandbox network element", `sandbox(network = [1])`, "sandbox: network must hold strings only"},
		{"sandbox network tuple", `sandbox(network = (1,))`, "sandbox: network must hold strings only"},
		{"sandbox network tuple beside a computed element", `sandbox(network = (str(item), 1))`, "sandbox: network must hold strings only"},
		{"sandbox network list in parentheses beside a computed element", `sandbox(network = ([str(item), 1]))`, "sandbox: network must hold strings only"},
		{"sandbox network beside a computed element", `sandbox(network = [str(item), 1])`, "sandbox: network must hold strings only"},
		{"sandbox network domain beside a computed element", `sandbox(network = [str(item), "https://x.org"])`, `sandbox network lists "https://x.org"`},
		{"item_create title", `item_create(title = "", key = "k")`, "item_create: title must not be empty"},
		{"item_create key", `item_create(title = "t", key = "")`, "item_create: key must not be empty"},
		{"item_status id", `item_status(id = "", status = "open")`, "item_status: id must not be empty"},
		{"item_comment id", `item_comment(id = "", text = "t", key = "k")`, "item_comment: id must not be empty"},
		{"item_comment text", `item_comment(id = "i", text = "", key = "k")`, "item_comment: text must not be empty"},
		{"item_comment key", `item_comment(id = "i", text = "t", key = "")`, "item_comment: key must not be empty"},
		{"merge base", `merge(prs = x, base = "", repo = "r", name = "n")`, "merge: base must not be empty"},
		{"merge repo", `merge(prs = x, base = "b", repo = "", name = "n")`, "merge: repo must not be empty"},
		{"outputs beside a computed element", `agent("p", "m", outputs = [str(item), 1])`, "agent: outputs must hold strings only"},
		{"stop_on_rc beside a computed element", `command(run = ["x"], stop_on_rc = [str(item), 0])`, "command: stop_on_rc must hold whole numbers from 1 to 255"},
		{"prs beside a computed element", `merge(prs = [x, 1], base = "b", repo = "r", name = "n")`, "merge: prs[1]: the member has the type int, not struct"},
		{"own logins beside a computed element", `merge(prs = x, base = "b", repo = "r", name = "n", own = {"logins": [str(item), 1]})`, "merge: own.logins must hold strings only"},
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

func TestCheck_AcceptsTheArgumentsThatTheRunAccepts(t *testing.T) {
	t.Parallel()

	got := checkText(t, "x = []\n\ndef main(item):\n"+
		"    command(run = [\"x\"], env = {\"A\": \"1\", \"B\": str(item)})\n"+
		"    command(run = [\"x\"], env = {str(item): 1})\n"+
		"    write(run = [\"x\"], env = {})\n"+
		"    sandbox(network = [str(item), \"api.example.org\"])\n"+
		"    item_create(title = \"t\", key = \"k\", body = \"\")\n"+
		"    item_create(title = str(item), key = str(item))\n"+
		"    item_status(id = \"i\", status = \"open\")\n"+
		"    item_comment(id = \"i\", text = \"t\", key = \"k\")\n"+
		"    merge(prs = [x, struct(item = \"a\", head = \"h\")], base = \"b\", repo = \"r\", name = \"n\")\n"+
		"    merge(prs = x, base = str(item), repo = \"r\", name = \"m\")\n"+
		"    command(run = [\"x\"], stop_on_rc = [str(item), 2])\n")

	assert.Empty(t, got)
}
