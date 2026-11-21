package opencode_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/opencode"
)

const pluginPath = "config/plugins/beehave-guard.js"

// closedEnv is what a run with a guard sets so that no file of the owner or of the checkout adds a hook.
func closedEnv(runDir string) map[string]string {
	return map[string]string{
		"OPENCODE_CONFIG_DIR":              filepath.Join(runDir, "config"),
		"XDG_CONFIG_HOME":                  filepath.Join(runDir, "xdg"),
		"OPENCODE_DISABLE_PROJECT_CONFIG":  "1",
		"OPENCODE_DISABLE_CLAUDE_CODE":     "1",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS": "1",
		"OPENCODE_DISABLE_AUTOUPDATE":      "1",
		"OPENCODE_DISABLE_MODELS_FETCH":    "1",
		"OPENCODE_DISABLE_LSP_DOWNLOAD":    "1",
	}
}

func TestGuard_WritesOnePluginAndTheClosedRunEnvironment(t *testing.T) {
	t.Parallel()

	launch := opencode.New().Guard("/bin/beehave check command", "/run", nil, nil)

	assert.Empty(t, launch.Args)
	assert.Equal(t, closedEnv("/run"), launch.Env)
	require.Len(t, launch.Files, 1)
	assert.Contains(t, launch.Files, pluginPath)
	assert.Contains(t, launch.Files[pluginPath], `"/bin/beehave check command"`)
	assert.Contains(t, launch.Files[pluginPath], "tool.execute.before")
}

func TestGuard_DoesNotLetOpenCodePureSwitchOffThePlugin(t *testing.T) {
	t.Parallel()

	assert.NotContains(t, opencode.New().Guard("g", "/run", nil, nil).Env, "OPENCODE_PURE")
}

func TestSandbox_CarriesTheSameClosedRunLaunchAsGuard(t *testing.T) {
	t.Parallel()

	adapter := opencode.New()

	assert.Equal(t, adapter.Guard("g", "/run", nil, nil), adapter.Sandbox(agent.SandboxPlan{Work: "/w"}, "g", "/run", nil, nil))
}

func TestSandbox_WithNoGuardStillClosesTheRunAndWritesNoPlugin(t *testing.T) {
	t.Parallel()

	launch := opencode.New().Sandbox(agent.SandboxPlan{Work: "/w"}, "", "/run", nil, nil)

	assert.Equal(t, closedEnv("/run"), launch.Env)
	assert.Empty(t, launch.Files)
}

func TestGuard_OpensTheFolderOfEachOutputFileThroughTheInlineConfigInEveryMode(t *testing.T) {
	t.Parallel()

	const want = `{"permission":{"external_directory":{"/state/outputs/7.review/*":"allow"}}}`

	adapter := opencode.New()
	write := []string{"/state/outputs/7.review/output.json"}

	assert.JSONEq(t, want, adapter.Guard("g", "/run", write, nil).Env["OPENCODE_CONFIG_CONTENT"], "guarded")
	assert.JSONEq(t, want, adapter.Sandbox(agent.SandboxPlan{Work: "/w"}, "", "/run", write, nil).Env["OPENCODE_CONFIG_CONTENT"], "sandbox")

	open := adapter.Guard("", "/run", write, nil)
	assert.JSONEq(t, want, open.Env["OPENCODE_CONFIG_CONTENT"], "no guard")
	assert.NotContains(t, open.Env, "OPENCODE_CONFIG_DIR", "a run with no guard stays open: only the output folder is added")
	assert.Empty(t, open.Files)
	assert.NotContains(t, open.Env, "OPENCODE_PERMISSION", "the variable of a step with no edits is not replaced")
}

func TestGuard_AStepWithNoGuardAndNoOutputFileAddsNothing(t *testing.T) {
	t.Parallel()

	assert.True(t, opencode.New().Guard("", "/run", nil, nil).Empty())
}

func TestGuard_TheGuardCommandStaysOneJavaScriptString(t *testing.T) {
	t.Parallel()

	hostile := []string{
		`a"; process.exit(0); "`,
		"a$(touch /x)`id`",
		`a\"; throw 1; //`,
		"line\nbreak",
		"__GUARD_COMMAND__",
		"u v",
	}

	for _, command := range hostile {
		literal, err := json.Marshal(command)
		require.NoError(t, err)

		text := opencode.New().Guard(command, "/run", nil, nil).Files[pluginPath]

		assert.Contains(t, text, "const GUARD = "+string(literal)+"\n", command)
		// The rest of the plugin is the same as for a harmless command, so nothing left the string.
		plain := opencode.New().Guard("g", "/run", nil, nil).Files[pluginPath]
		assert.Equal(t,
			strings.Replace(plain, `const GUARD = "g"`, "const GUARD = "+string(literal), 1),
			text, command)
	}
}

// decisions are the lines of the plugin that each make one decision of the guard. TestGuard_ThePluginRunOnNode runs the plugin where Node is installed; this
// table reads the same lines as text, so a host without Node checks them too. Each must be in the plugin exactly once, and the lines of the verdict must keep this order.
// verdict is true for a line of the verdict function, whose order matters: a pass before a refusal would let the call through.
var decisions = []struct {
	name, line string
	verdict    bool
}{
	{"the plugin writes its marker", `writeFileSync(LOADED, "loaded\n")`, false},
	{"the verdict runs the guard command", `const run = spawnSync(GUARD, { shell: true, input: event, encoding: "utf8", timeout: 30000 })`, true},
	{"a check that did not run refuses", `if (run.error) return "the check of the command did not run"`, true},
	{"exit code 2 refuses with the text of the check", `if (run.status === 2) return (run.stderr || "").trim() || "the command is not allowed"`, true},
	{"any other exit code refuses", `if (run.status !== 0) return "the check of the command failed"`, true},
	{"only exit code 0 lets the call through", `return ""`, true},
	{"a throw refuses, and never with an empty text", `return String(err && err.message ? err.message : err) || "the check of the command threw"`, true},
	{"only the bash tool is checked", `if (input.tool !== "bash") return`, false},
	{"the hook asks the verdict about the command of the call", `const why = verdict(directory, output && output.args ? output.args.command : undefined)`, false},
	{"a refusal throws", `if (why !== "") throw new Error("beehave refused: " + why)`, false},
}

// planOf returns the lines of the plugin, trimmed.
func planOf(text string) []string {
	var lines []string

	for _, line := range strings.Split(text, "\n") {
		lines = append(lines, strings.TrimSpace(line))
	}

	return lines
}

// decisionProblems returns the name of each decision that the plugin text does not make exactly once, and one name when the verdict lines are out of order.
func decisionProblems(text string) []string {
	lines := planOf(text)

	var problems []string

	last := -1
	ordered := true

	for _, decision := range decisions {
		at := slices.Index(lines, decision.line)
		if at < 0 || slices.Index(lines[at+1:], decision.line) >= 0 {
			problems = append(problems, decision.name)

			continue
		}

		if decision.verdict {
			ordered = ordered && at > last
			last = at
		}
	}

	if !ordered {
		problems = append(problems, "the verdict lines keep their order")
	}

	return problems
}

func TestGuard_EveryDecisionOfThePluginIsInIt(t *testing.T) {
	t.Parallel()

	assert.Empty(t, decisionProblems(opencode.New().Guard("g", "/run", nil, nil).Files[pluginPath]))
}

func TestGuard_AMutantOfThePluginBreaksADecision(t *testing.T) {
	t.Parallel()

	text := opencode.New().Guard("g", "/run", nil, nil).Files[pluginPath]

	mutants := []struct{ name, from, to string }{
		{"a failed check passes", `    if (run.status !== 0) return "the check of the command failed"` + "\n", ""},
		{"a spawn error passes", `    if (run.error) return "the check of the command did not run"` + "\n", ""},
		{"a throw passes", `return String(err && err.message ? err.message : err) || "the check of the command threw"`, `return ""`},
		{"a throw with an empty text passes", ` || "the check of the command threw"`, ``},
		{"every tool is skipped", `if (input.tool !== "bash") return`, `if (input.tool !== "bash" || true) return`},
		{"the hook skips the verdict", `const why = verdict(directory, output && output.args ? output.args.command : undefined)`, `const why = ""`},
		{"the verdict never runs the guard command", `const run = spawnSync(GUARD, { shell: true, input: event, encoding: "utf8", timeout: 30000 })`, `const run = { status: 0 }`},
		{"a refusal does not throw", `if (why !== "") throw new Error("beehave refused: " + why)`, `if (why !== "") return`},
		{"the marker is not written", `  writeFileSync(LOADED, "loaded\n")` + "\n", ""},
		{"exit code 2 passes", `if (run.status === 2) return (run.stderr || "").trim() || "the command is not allowed"`, `if (run.status === 2) return ""`},
	}

	for _, mutant := range mutants {
		changed := strings.Replace(text, mutant.from, mutant.to, 1)

		require.NotEqual(t, text, changed, mutant.name+": the mutation found nothing to change")
		assert.NotEmpty(t, decisionProblems(changed), mutant.name)
	}
}

func TestGuard_TheMarkerPathIsInsideTheRunFolderAndStaysOneJavaScriptString(t *testing.T) {
	t.Parallel()

	launch := opencode.New().Guard("__LOADED_PATH__", `/run "x"`, nil, nil)

	assert.Equal(t, "config/beehave-guard-loaded", launch.Loaded)
	assert.Contains(t, launch.Files[pluginPath], `const GUARD = "__LOADED_PATH__"`, "the text that a command inserts is not read as a place to fill")
	assert.Contains(t, launch.Files[pluginPath], `const LOADED = "/run \"x\"/config/beehave-guard-loaded"`)
}
