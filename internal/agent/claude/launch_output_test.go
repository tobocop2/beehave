package claude_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
)

const outputFile = "/state/outputs/7.review/output.json"

// allowRules decodes the allow rules of the --settings JSON, the last argument of a launch.
func allowRules(t *testing.T, args []string) []string {
	t.Helper()

	var parsed struct {
		Permissions struct{ Allow []string }
	}

	require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed))

	return parsed.Permissions.Allow
}

func TestLaunch_AStepWithNoSandboxOpensItsOutputFileToTheFileTools(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Guard("/bin/beehave check command", "/run", []string{outputFile}, nil).Args

	assert.Equal(t, []string{"--add-dir", "/state/outputs/7.review"}, args[2:4], "the folder of the one output file is a working directory")
	assert.Equal(t, "--settings", args[4])
	assert.Equal(t, []string{"Read(//state/outputs/7.review/output.json)", "Edit(//state/outputs/7.review/output.json)"}, allowRules(t, args))
	assert.Contains(t, args[len(args)-1], `"matcher":"Bash"`, "the hook stays in the same settings")
}

func TestLaunch_AStepWithNoGuardAndNoSandboxStillOpensItsOutputFileToTheFileTools(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Guard("", "/run", []string{outputFile}, nil).Args

	assert.Equal(t, []string{"--add-dir", "/state/outputs/7.review"}, args[2:4])
	assert.Equal(t, []string{"Read(//state/outputs/7.review/output.json)", "Edit(//state/outputs/7.review/output.json)"}, allowRules(t, args))
	assert.NotContains(t, args[len(args)-1], "hooks", "no guard, so no hook")
}

func TestLaunch_AStepWithNoOutputFileAndNoGuardAddsOnlyTheAttributionSetting(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Guard("", "/run", nil, nil)

	assert.Equal(t, []string{"--setting-sources", "", "--settings", `{"attribution":{"commit":"","pr":"","sessionUrl":false}}`}, launch.Args)
	assert.Empty(t, launch.Env)
	assert.Empty(t, launch.Files)
}

func TestLaunch_TheGuardAndTheSandboxOpenTheOutputFileByTheSameRules(t *testing.T) {
	t.Parallel()

	adapter := claude.New(nil)
	guarded := allowRules(t, adapter.Guard("g", "/run", []string{outputFile}, nil).Args)
	boxed := allowRules(t, adapter.Sandbox(plan(), "g", "/run", []string{outputFile}, nil).Args)

	assert.Equal(t, guarded, boxed[:len(guarded)], "one function writes the rules for every mode")
}
