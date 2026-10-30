package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	exampleForbidList = regexp.MustCompile(`(?m)^FORBID = \[(.*)\]$`)
	quotedName        = regexp.MustCompile(`"([^"]+)"`)
)

// exampleForbid reads the kinds that `workflow.star` of the remediation example lists in FORBID, and checks that the agent step uses the list.
func exampleForbid(t *testing.T) []string {
	t.Helper()

	text, err := os.ReadFile("../../examples/remediation/workflow.star")
	require.NoError(t, err)
	require.Contains(t, string(text), "forbid_commands = FORBID")

	list := exampleForbidList.FindStringSubmatch(string(text))
	require.Len(t, list, 2, "workflow.star holds one FORBID list")

	var kinds []string

	for _, match := range quotedName.FindAllStringSubmatch(list[1], -1) {
		kinds = append(kinds, match[1])
	}

	return kinds
}

func jsonEvent(command string) string {
	return `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":` + quoted(command) + `}}`
}

func quoted(text string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text) + `"`
}

func TestExample_TheHookOfTheExampleRefusesEachRuleOfTheCommandRows(t *testing.T) {
	t.Parallel()

	forbid := strings.Join(exampleForbid(t), ",")

	rows := map[string]string{
		"S1-L-94: a commit author":                   "git commit --author='A <a@x>' -m x",
		"S1-L-94: a configured author":               "git -c user.name=me commit -m x",
		"S1-M-34: no-verify":                         "git commit --no-verify -m x",
		"S1-M-34: stash":                             "git stash",
		"S1-M-34: a config write":                    "git config core.editor vi",
		"S1-M-34: a force-push":                      "git push --force origin b",
		"S1-S-09: a start file":                      "echo 'x' >> ~/.zshenv",
		"S1-S-09: a search path":                     "PATH=/x:$PATH make",
		"S1-S-14: a file with a key":                 "cat ~/.netrc",
		"S1-S-14: the whole config of git":           "git config --list",
		"S1-S-08: bulk staging stays refused":        "git add -A",
		"S1-S-08: a hidden command stays refused":    "$X -rf $Y",
		"S1-S-08: a delete of the laptop is refused": "rm -rf *",
	}

	for name, command := range rows {
		code, stderr := hook(t, jsonEvent(command), "--forbid", forbid)

		assert.Equal(t, 2, code, "%s: %s", name, stderr)
	}

	for _, clean := range []string{"git commit -m x", "git push origin b", "git config --get core.editor", "cat main.go", "make check"} {
		code, stderr := hook(t, jsonEvent(clean), "--forbid", forbid)

		assert.Zero(t, code, "%s: %s", clean, stderr)
	}
}
