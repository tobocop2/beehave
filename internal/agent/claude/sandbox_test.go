package claude_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
)

func plan() agent.SandboxPlan {
	return agent.SandboxPlan{
		Work: "/work/tree", GitDirs: []string{"/repo/.git"}, Read: []string{"/opt/tools"},
		Deny: []string{"/home/o/.ssh", "/home/o/.aws"}, Network: []string{"api.example.org"}, Temp: "/tmp/bh-1",
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

	flags, _ := settingsOf(t, claude.New(nil).Sandbox(plan(), "", "/run", nil, nil).Args)

	assert.Equal(t, []string{"--setting-sources", "", "--strict-mcp-config", "--disallowedTools", "WebFetch", "WebSearch"}, flags)
}

func TestSandbox_AsksTheCLIToScrubCredentialsFromTheEnvironmentOfItsSubprocesses(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Sandbox(plan(), "", "/run", nil, nil)

	assert.Equal(t, map[string]string{
		"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB": "1", "CLAUDE_ENV_FILE": "/run/sandbox-env.sh", "CLAUDE_CODE_TMPDIR": "/tmp/bh-1",
	}, launch.Env, "the CLI keeps its temporary files, and the TMPDIR of the shell, in the temporary folder of the run")
}

func TestSandbox_TheShellReadsTheGitVariablesOfTheEngineFromAnEnvFile(t *testing.T) {
	t.Parallel()

	p := plan()
	p.Identity = agent.GitIdentity{Name: "Ann O'Brien $HOME", Email: "ann@example.com"}
	runDir := t.TempDir()

	launch := claude.New(nil).Sandbox(p, "", runDir, nil, nil)

	envFile := filepath.Join(runDir, "sandbox-env.sh")
	require.Equal(t, envFile, launch.Env["CLAUDE_ENV_FILE"], "the CLI sources the file in its shell")
	require.Contains(t, launch.Files, "sandbox-env.sh")
	require.NoError(t, os.WriteFile(envFile, []byte(launch.Files["sandbox-env.sh"]), 0o600))

	// A POSIX shell that sources the file holds each git variable of the engine with its value unchanged.
	shell := exec.CommandContext(t.Context(), "sh", "-c", `. "$1" && env`, "sh", envFile) //nolint:gosec // a test sources its own temporary file
	shell.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := shell.Output()
	require.NoError(t, err)

	lines := strings.Split(string(out), "\n")
	for name, value := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Ann O'Brien $HOME", "GIT_AUTHOR_EMAIL": "ann@example.com",
		"GIT_COMMITTER_NAME": "Ann O'Brien $HOME", "GIT_COMMITTER_EMAIL": "ann@example.com",
	} {
		assert.Contains(t, lines, name+"="+value)
	}
}

func TestSandbox_TheEnvFileExportsTheSixGitVariablesAndTheHomeFolderAndNothingElse(t *testing.T) {
	t.Parallel()

	p := plan()
	p.Identity = agent.GitIdentity{Name: "Ann", Email: "ann@example.com"}

	launch := claude.New(nil).Sandbox(p, "", "/run", nil, nil)

	assert.Equal(t, map[string]string{"sandbox-env.sh": "export GIT_AUTHOR_EMAIL=ann@example.com\n" +
		"export GIT_AUTHOR_NAME=Ann\n" +
		"export GIT_COMMITTER_EMAIL=ann@example.com\n" +
		"export GIT_COMMITTER_NAME=Ann\n" +
		"export GIT_CONFIG_GLOBAL=/dev/null\n" +
		"export GIT_CONFIG_NOSYSTEM=1\n" +
		"export HOME=\"${TMPDIR:-/tmp}\"/beehave-home-run\n" +
		"mkdir -p \"$HOME\"\n"}, launch.Files, "the run folder holds only the env file, and the env file holds no other variable")
}

// shellHome sources the env file of a run in a POSIX shell whose temporary folder is tmp and returns the HOME that the shell holds afterwards.
func shellHome(t *testing.T, runDir, tmp string) string {
	t.Helper()

	envFile := filepath.Join(runDir, "sandbox-env.sh")
	launch := claude.New(nil).Sandbox(plan(), "", runDir, nil, nil)
	require.NoError(t, os.MkdirAll(runDir, 0o700))
	require.NoError(t, os.WriteFile(envFile, []byte(launch.Files["sandbox-env.sh"]), 0o600))

	shell := exec.CommandContext(t.Context(), "sh", "-c", `. "$1" && printf %s "$HOME"`, "sh", envFile) //nolint:gosec // a test sources its own temporary file
	shell.Env = []string{"PATH=/usr/bin:/bin", "HOME=/Users/owner", "TMPDIR=" + tmp}
	out, err := shell.Output()
	require.NoError(t, err)

	return string(out)
}

func TestSandbox_TheShellGetsANewEmptyHomeFolderOfItsOwnNotTheHomeOfTheOwner(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	first, second := shellHome(t, filepath.Join(t.TempDir(), "run-one"), tmp), shellHome(t, filepath.Join(t.TempDir(), "run-two"), tmp)

	assert.Equal(t, filepath.Join(tmp, "beehave-home-run-one"), first)
	assert.Equal(t, filepath.Join(tmp, "beehave-home-run-two"), second, "each run has a home folder of its own")

	for _, home := range []string{first, second} {
		entries, err := os.ReadDir(home)
		require.NoError(t, err, "the env file makes the folder before the shell runs a command")
		assert.Empty(t, entries, "the folder starts empty")
	}
}

func TestSandbox_TheEnvFileFallsBackToSlashTmpWhenTheShellHasNoTemporaryFolder(t *testing.T) {
	t.Parallel()

	file := claude.New(nil).Sandbox(plan(), "", "/run", nil, nil).Files["sandbox-env.sh"]

	assert.Contains(t, file, "export HOME=\"${TMPDIR:-/tmp}\"/beehave-home-run\n", "the fallback text is exactly ${TMPDIR:-/tmp}")
}

func TestSandbox_TheCLIKeepsTheHomeOfTheOwnerForItsLogin(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Sandbox(plan(), "", "/run", nil, nil)

	assert.NotContains(t, launch.Env, "HOME", "only the shell gets another HOME, through the env file")
}

func TestGuard_ARunWithNoSandboxGetsNoEnvFileAndNoHomeFolder(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Guard("/bin/beehave check command", "/run", nil, nil)

	assert.NotContains(t, launch.Files, "sandbox-env.sh")
	assert.NotContains(t, launch.Env, "CLAUDE_ENV_FILE")
	assert.NotContains(t, launch.Env, "CLAUDE_CODE_TMPDIR")
	assert.NotContains(t, launch.Env, "HOME")
}

func TestSandbox_TheSettingsAllowReadingTheEnvFileAndNotTheRunFolder(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Sandbox(plan(), "/bin/beehave check command", "/run/folder", nil, nil).Args

	assert.Equal(t, 1, strings.Count(args[len(args)-1], "/run/folder"), "the settings name the run folder once")

	_, settings := settingsOf(t, args)
	box, ok := settings["sandbox"].(map[string]any)
	require.True(t, ok)

	files, ok := box["filesystem"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, files["allowRead"], "/run/folder/sandbox-env.sh")
	assert.NotContains(t, files["allowRead"], "/run/folder")
}

func TestSandbox_WritesTheDenyByDefaultBaseTheCredentialLayerAndTheNetworkList(t *testing.T) {
	t.Parallel()

	_, settings := settingsOf(t, claude.New(nil).Sandbox(plan(), "", "/run", nil, nil).Args)

	want := `{
	  "attribution": {"commit": "", "pr": "", "sessionUrl": false},
	  "sandbox": {
	    "enabled": true,
	    "failIfUnavailable": true,
	    "allowUnsandboxedCommands": false,
	    "filesystem": {
	      "denyRead": ["~/", "/home/o/.ssh", "/home/o/.aws"],
	      "allowRead": ["/work/tree", "/repo/.git", "/opt/tools", "/run/sandbox-env.sh"],
	      "denyWrite": ["/home/o/.ssh", "/home/o/.aws"]
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

	args := claude.New(nil).Sandbox(p, "", "/run", nil, nil).Args

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

	args := claude.New(nil).Sandbox(p, "", "/run", nil, nil).Args

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

func TestSandbox_OpensEachWriteFileByAReadAndAnEditAllowRuleOfItsExactPath(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Sandbox(plan(), "", "/run", []string{"/state/outputs/1.review.json"}, nil).Args

	var parsed struct {
		Permissions struct{ Allow, Deny []string }
	}

	require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed))

	assert.NotContains(t, args[len(args)-1], "allowWrite", "the Edit allow rule alone opens the file to the shell")
	assert.Equal(t, []string{"Read(//state/outputs/1.review.json)", "Edit(//state/outputs/1.review.json)"}, parsed.Permissions.Allow, "the exact file, with no /** tail")
	assert.Contains(t, args, "/state/outputs", "the folder of the file is a working directory")
	assert.Contains(t, parsed.Permissions.Deny, "Edit(//home/o/.ssh/**)", "the deny rules stay")
}

func TestSandbox_AnEditPlanAllowsTheFileToolsInTheWorkFolderByOneRule(t *testing.T) {
	t.Parallel()

	for name, edits := range map[string]bool{"edits": true, "no_edits": false} {
		p := plan()
		p.Edits = edits

		args := claude.New(nil).Sandbox(p, "", "/run", []string{"/state/outputs/1.review.json"}, nil).Args

		var parsed struct {
			Permissions struct{ Allow, Deny []string }
		}

		require.NoError(t, json.Unmarshal([]byte(args[len(args)-1]), &parsed), name)

		want := []string{"Read(//state/outputs/1.review.json)", "Edit(//state/outputs/1.review.json)"}
		if edits {
			want = append(want, "Edit(//work/tree/**)")
		}

		assert.Equal(t, want, parsed.Permissions.Allow, name)
		assert.Contains(t, parsed.Permissions.Deny, "Edit(//home/o/.ssh/**)", "%s: the deny rules stay, and a deny rule wins over an allow rule", name)
	}
}

func TestSandbox_AnEmptyPlanWritesEmptyListsAndNeverNull(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Sandbox(agent.SandboxPlan{Work: "/w"}, "", "/run", nil, nil).Args
	text := args[len(args)-1]

	assert.Contains(t, text, `"allowedDomains":[]`)
	assert.Contains(t, text, `"denyWrite":[]`)
	assert.NotContains(t, text, "null")
}

func TestSandbox_NeverSetsTheKeysThatWidenTheSandbox(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Sandbox(plan(), "/bin/beehave check command", "/run", nil, nil).Args
	text := args[len(args)-1]

	for _, key := range []string{"excludedCommands", "allowLocalBinding", "allowAllUnixSockets", "allowAppleEvents"} {
		assert.NotContains(t, text, key)
	}

	// The shared git directory is readable, and no explicit entry makes it writable: the settings carry no allowWrite key, and a plan with no write
	// file carries no allow rule.
	_, settings := settingsOf(t, args)
	box, ok := settings["sandbox"].(map[string]any)
	require.True(t, ok)

	files, ok := box["filesystem"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, files, "allowWrite")

	rules, ok := settings["permissions"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, rules, "allow")
}

func TestSandbox_CarriesTheHookOfTheGuardInTheSameSettings(t *testing.T) {
	t.Parallel()

	args := claude.New(nil).Sandbox(plan(), "/bin/beehave check command", "/run", nil, nil).Args

	assert.Equal(t, 1, countOf(args, "--settings"), "one settings object holds the hook and the sandbox")

	_, settings := settingsOf(t, args)
	require.Contains(t, settings, "hooks")
	require.Contains(t, settings, "sandbox")

	hook, err := json.Marshal(settings["hooks"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/beehave check command","onFailure":"block"}]}]}`, string(hook))
}

func TestSandbox_WithoutAGuardWritesNoHook(t *testing.T) {
	t.Parallel()

	_, settings := settingsOf(t, claude.New(nil).Sandbox(plan(), "", "/run", nil, nil).Args)

	assert.NotContains(t, settings, "hooks")
}

func TestProfile_ClaudeHasTheSandboxCapabilityAndAMinimumVersion(t *testing.T) {
	t.Parallel()

	profile := claude.New(nil).Profile()

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

func TestSandbox_PassesEachReadFolderAsAWorkingDirectoryByTheAddDirFlag(t *testing.T) {
	t.Parallel()

	p := plan()
	p.ReadDirs = []string{"/state/inputs/1.plan"}

	args := claude.New(nil).Sandbox(p, "", "/run", nil, nil).Args

	flags, parsed := settingsOf(t, args)
	assert.Equal(t, []string{"--setting-sources", "", "--strict-mcp-config", "--disallowedTools", "WebFetch", "WebSearch", "--add-dir", "/state/inputs/1.plan"}, flags,
		"an allow rule does not lift blockReadsOutsideWorkingDirectories, a working directory does")
	assert.NotContains(t, parsed["permissions"], "additionalDirectories", "one mechanism: the flag, not the settings key")
	assert.NotContains(t, parsed["permissions"], "allow", "no Read allow rule: the docs say no allow rule lets the file tools through the block")

	none, _ := settingsOf(t, claude.New(nil).Sandbox(plan(), "", "/run", nil, nil).Args)
	assert.NotContains(t, none, "--add-dir", "a plan with no read folder passes no flag")
}
