package claude_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/agent/claude"
)

func plan() agent.SandboxPlan {
	return agent.SandboxPlan{
		Work: "/work/tree", GitDirs: []string{"/repo/.git"}, Read: []string{"/opt/tools"},
		Deny: []string{"/home/o/.ssh", "/home/o/.aws"}, Network: []string{"api.example.org"}, AllowWrite: []string{"/work/tree/out"},
	}
}

// settingsOf returns the arguments before --settings and the parsed JSON after it.
func settingsOf(t *testing.T, args []string) ([]string, map[string]any) {
	t.Helper()

	require.GreaterOrEqual(t, len(args), 2)
	require.Equal(t, "--settings", args[len(args)-2])

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed))

	return args[:len(args)-2], parsed
}

func TestSandbox_IsolatesTheSettingSourcesAndTheMCPServersAndRemovesTheWebTools(t *testing.T) {
	t.Parallel()

	flags, _ := settingsOf(t, claude.New().Sandbox(plan(), "", "/run").Args)

	assert.Equal(t, []string{"--setting-sources", "", "--strict-mcp-config", "--disallowedTools", "WebFetch", "WebSearch"}, flags)
}

func TestSandbox_WritesTheDenyByDefaultBaseTheCredentialLayerAndTheNetworkList(t *testing.T) {
	t.Parallel()

	_, settings := settingsOf(t, claude.New().Sandbox(plan(), "", "/run").Args)

	want := `{
	  "sandbox": {
	    "enabled": true,
	    "failIfUnavailable": true,
	    "allowUnsandboxedCommands": false,
	    "filesystem": {
	      "denyRead": ["~/", "/home/o/.ssh", "/home/o/.aws"],
	      "allowRead": ["/work/tree", "/repo/.git", "/opt/tools"],
	      "denyWrite": ["/home/o/.ssh", "/home/o/.aws"],
	      "allowWrite": ["/work/tree/out"]
	    },
	    "network": {"allowedDomains": ["api.example.org"], "strictAllowlist": true}
	  },
	  "permissions": {
	    "blockReadsOutsideWorkingDirectories": true,
	    "deny": ["WebFetch", "WebSearch",
	             "Read(//home/o/.ssh/**)", "Edit(//home/o/.ssh/**)",
	             "Read(//home/o/.aws/**)", "Edit(//home/o/.aws/**)"]
	  }
	}`

	got, err := json.Marshal(settings)
	require.NoError(t, err)
	assert.JSONEq(t, want, string(got))
}

func TestSandbox_DeniesThePatternsInTheSandboxAndInTheRulesOfTheFileTools(t *testing.T) {
	t.Parallel()

	p := plan()
	p.DenyPatterns = []string{"/work/tree/.env*", "/work/tree/**/.env*"}

	args := claude.New().Sandbox(p, "", "/run").Args

	var parsed struct {
		Sandbox struct {
			Filesystem struct{ DenyRead, DenyWrite []string }
		}
		Permissions struct{ Deny []string }
	}

	require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed))

	for _, list := range [][]string{parsed.Sandbox.Filesystem.DenyRead, parsed.Sandbox.Filesystem.DenyWrite} {
		assert.Contains(t, list, "/work/tree/**/.env*")
		assert.Contains(t, list, "/work/tree/.env*")
	}

	for _, rule := range []string{"Read(//work/tree/**/.env*)", "Edit(//work/tree/**/.env*)", "Read(//work/tree/.env*)", "Edit(//work/tree/.env*)"} {
		assert.Contains(t, parsed.Permissions.Deny, rule)
	}
}

func TestSandbox_DeniesWritesToTheClosedPathsInTheSandboxAndTheEditRules(t *testing.T) {
	t.Parallel()

	p := plan()
	p.DenyWrite = []string{"/work/tree/.husky", "/work/tree/.git"}

	args := claude.New().Sandbox(p, "", "/run").Args

	var parsed struct {
		Sandbox struct {
			Filesystem struct{ DenyRead, DenyWrite []string }
		}
		Permissions struct{ Deny []string }
	}

	require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed))

	assert.Contains(t, parsed.Sandbox.Filesystem.DenyWrite, "/work/tree/.husky")
	assert.Contains(t, parsed.Sandbox.Filesystem.DenyWrite, "/work/tree/.git")
	assert.NotContains(t, parsed.Sandbox.Filesystem.DenyRead, "/work/tree/.husky", "the closed paths stay readable")
	assert.Contains(t, parsed.Permissions.Deny, "Edit(//work/tree/.husky/**)")
	assert.Contains(t, parsed.Permissions.Deny, "Edit(//work/tree/.git/**)")
	assert.NotContains(t, parsed.Permissions.Deny, "Read(//work/tree/.husky/**)")
}

func TestSandbox_AnEmptyPlanWritesEmptyListsAndNeverNull(t *testing.T) {
	t.Parallel()

	args := claude.New().Sandbox(agent.SandboxPlan{Work: "/w"}, "", "/run").Args
	text := args[len(args)-1]

	assert.Contains(t, text, `"allowedDomains":[]`)
	assert.Contains(t, text, `"allowWrite":[]`)
	assert.Contains(t, text, `"denyWrite":[]`)
	assert.NotContains(t, text, "null")
}

func TestSandbox_NeverSetsTheKeysThatWidenTheSandbox(t *testing.T) {
	t.Parallel()

	args := claude.New().Sandbox(plan(), "/bin/obee check command", "/run").Args
	text := args[len(args)-1]

	for _, key := range []string{"excludedCommands", "allowLocalBinding", "allowAllUnixSockets", "allowAppleEvents"} {
		assert.NotContains(t, text, key)
	}

	// The shared git directory is readable, and it is not writable by an explicit entry.
	_, settings := settingsOf(t, args)
	box, ok := settings["sandbox"].(map[string]any)
	require.True(t, ok)

	files, ok := box["filesystem"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, files["allowWrite"], "/repo/.git")
}

func TestSandbox_CarriesTheHookOfTheGuardInTheSameSettings(t *testing.T) {
	t.Parallel()

	args := claude.New().Sandbox(plan(), "/bin/obee check command", "/run").Args

	assert.Equal(t, 1, countOf(args, "--settings"), "one settings object holds the hook and the sandbox")

	_, settings := settingsOf(t, args)
	require.Contains(t, settings, "hooks")
	require.Contains(t, settings, "sandbox")

	hook, err := json.Marshal(settings["hooks"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/obee check command","onFailure":"block"}]}]}`, string(hook))
}

func TestSandbox_WithoutAGuardWritesNoHook(t *testing.T) {
	t.Parallel()

	_, settings := settingsOf(t, claude.New().Sandbox(plan(), "", "/run").Args)

	assert.NotContains(t, settings, "hooks")
}

func TestProfile_ClaudeHasTheSandboxCapabilityAndAMinimumVersion(t *testing.T) {
	t.Parallel()

	profile := claude.New().Profile()

	assert.True(t, profile.Sandbox)
	assert.Equal(t, "2.1.285", profile.MinVersion, "the oldest version that knows every key of the sandbox settings")
}

func countOf(list []string, item string) int {
	n := 0

	for _, entry := range list {
		if entry == item {
			n++
		}
	}

	return n
}
