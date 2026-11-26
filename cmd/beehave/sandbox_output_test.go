package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

// outputSettings is the part of the settings of a sandboxed run that can open a path for write.
type outputSettings struct {
	Sandbox struct {
		Filesystem struct {
			AllowWrite []string `json:"allowWrite"`
		} `json:"filesystem"`
	} `json:"sandbox"`
	Permissions struct {
		Allow []string `json:"allow"`
	} `json:"permissions"`
}

func settingsOfArgs(t *testing.T, args []string) outputSettings {
	t.Helper()

	require.GreaterOrEqual(t, len(args), 2)
	require.Equal(t, "--settings", args[len(args)-2])

	return decodeOutputSettings(t, args[len(args)-1])
}

func decodeOutputSettings(t *testing.T, text string) outputSettings {
	t.Helper()

	var parsed outputSettings
	require.NoError(t, json.Unmarshal([]byte(text), &parsed))

	return parsed
}

// outputRequest is a sandboxed request with edits off that declares one output, written to file.
func outputRequest(file string) flow.AgentRequest {
	req := request()
	req.Sandbox, req.NoEdits, req.Outputs, req.OutputFile = &model.Sandbox{}, true, []string{"verdict"}, file

	return req
}

// assertOnlyWriteGrant asserts the full set of write grants of the settings: one Edit allow rule of the exact file and no allowWrite entry. Claude Code
// merges the paths of the Edit allow rules into the write paths of the sandbox, so that rule is the grant for the shell too.
// Each grant is also read as the pattern that Claude Code reads it as, so a glob entry that covers a sibling turns the test red.
func assertOnlyWriteGrant(t *testing.T, settings outputSettings, file string, closed ...string) {
	t.Helper()

	assert.Empty(t, settings.Sandbox.Filesystem.AllowWrite, "no allowWrite entry: the Edit allow rule is the one route")
	assert.Equal(t, []string{"Read(/" + file + ")", "Edit(/" + file + ")"}, settings.Permissions.Allow, "the one grant is the output file by its exact path, to read and to edit")

	for _, rule := range settings.Permissions.Allow {
		path := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(rule, "Read(/"), "Edit(/"), ")")
		assert.False(t, strings.ContainsAny(path, `*?[\`), "a write grant holds no glob character: %s", rule)

		for _, other := range closed {
			matched, err := filepath.Match(path, other)
			require.NoError(t, err)
			assert.False(t, matched || within(path, other), "the grant %s covers %s", rule, other)
		}
	}
}

func TestSandboxPlan_AStepWithOutputsAndNoEditsGetsSettingsThatAllowItsOutputFileAndRefuseASibling(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	outputs := filepath.Join(r.home, ".local", "state", "beehave", "outputs")
	file, sibling := filepath.Join(outputs, "1.agent_3Async-review-1_231", "output.json"), filepath.Join(outputs, "1.agent_3Aimplement_231", "output.json")

	_, err := r.f.agents.Run(t.Context(), outputRequest(file))
	require.NoError(t, err)
	require.NotNil(t, r.f.spec.Sandbox)
	assert.True(t, r.f.spec.NoEdits, "edits stay off")

	settings := settingsOfArgs(t, claude.New(nil).Sandbox(*r.f.spec.Sandbox, "", t.TempDir(), r.f.spec.Write, nil).Args)

	assertOnlyWriteGrant(t, settings, file, sibling, file+".tmp", outputs, r.work, filepath.Join(r.work, "a.go"))
	assert.False(t, within(r.work, sibling), "the sibling is outside the work folder, which the sandbox opens by default")
}

func TestSandboxPlan_AStepThatDeclaresNoOutputsOpensNoPathForWrite(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := outputRequest(filepath.Join(r.home, "state", "outputs", "1.json"))
	req.Outputs = nil

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, r.f.spec.Sandbox)

	settings := settingsOfArgs(t, claude.New(nil).Sandbox(*r.f.spec.Sandbox, "", t.TempDir(), r.f.spec.Write, nil).Args)
	assert.Empty(t, settings.Sandbox.Filesystem.AllowWrite)
	assert.Empty(t, settings.Permissions.Allow)
}

func TestSandboxPlan_AnOutputFileInACredentialPathStopsTheStep(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := outputRequest(filepath.Join(r.home, ".ssh", "outputs", "7.json"))

	result, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, "The file "+req.OutputFile+" of the state folder overlaps the credential path "+filepath.Join(r.home, ".ssh"))
}

func TestSandboxPlan_AStateFileWhosePathHoldsAGlobCharacterStopsTheStep(t *testing.T) {
	t.Parallel()

	for _, folder := range []string{"st*te", "st?te", "st[a]te", `st\ate`} {
		r := newSandboxRig(t)
		req := outputRequest(filepath.Join(r.home, folder, "outputs", "7.json"))

		result, err := r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeNotStarted, result.Outcome, folder)
		assert.Contains(t, result.Note, "The file "+req.OutputFile+" of the state folder holds a glob character", folder)

		req = outputRequest(filepath.Join(r.home, "state", "outputs", "7.json"))
		req.DataFile = filepath.Join(r.home, folder, "data", "7", "data.json")

		result, err = r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeNotStarted, result.Outcome, "a data file: %s", folder)
	}
}

func TestAgentRun_AStepWithNoSandboxAndAnOutputFileWhosePathHoldsAGlobCharacterStops(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)

	for _, folder := range []string{"st*te", "st?te", "st[a]te", `st\ate`} {
		req := outputRequest(filepath.Join(r.home, folder, "outputs", "7.json"))
		req.Sandbox = nil

		result, err := r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeNotStarted, result.Outcome, folder)
		assert.Contains(t, result.Note, "The file "+req.OutputFile+" of the state folder holds a glob character", folder)
	}
}

func TestAgentRun_AStepWithNoSandboxStartsWithItsOutputFileInACredentialPath(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	req := outputRequest(filepath.Join(r.home, ".ssh", "outputs", "7.json"))
	req.Sandbox = nil

	result, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.NotEqual(t, agent.OutcomeNotStarted, result.Outcome, result.Note)
	assert.Equal(t, []string{req.OutputFile}, r.f.spec.Write)
}

func TestAgentRun_AStepWithNoSandboxAndNoHomeFolderStillChecksTheGlobCharacterOfItsOutputFile(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	r.f.agents.home = func() (string, error) { return "", errors.New("no home") }

	req := outputRequest(filepath.Join(r.home, "st*te", "outputs", "7.json"))
	req.Sandbox = nil

	result, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, "holds a glob character")

	req = outputRequest(filepath.Join(r.home, "state", "outputs", "7.json"))
	req.Sandbox = nil

	result, err = r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.NotEqual(t, agent.OutcomeNotStarted, result.Outcome, result.Note)
}

func TestSandboxPlan_AWorkFolderWhosePathHoldsAGlobCharacterStopsAStepThatMayEdit(t *testing.T) {
	t.Parallel()

	for _, folder := range []string{"w*b", "w?b", "w[ab]", `w\b`} {
		r := newSandboxRig(t)
		work := filepath.Join(filepath.Dir(r.work), folder)
		require.NoError(t, os.MkdirAll(work, 0o700))
		runGit(t, work, "init", "-q")
		commitIn(t, work, "start")
		r.f.agents.trees = treeAt(work)

		req := request()
		req.Sandbox = &model.Sandbox{}

		result, err := r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeNotStarted, result.Outcome, folder)
		assert.Contains(t, result.Note, "The work folder "+work+" holds a glob character, "+folder[1:2], folder)

		req.NoEdits = true

		result, err = r.f.agents.Run(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, agent.OutcomeExited, result.Outcome, "a step that may not edit gets no rule of its work folder: %s: %s", folder, result.Note)
	}
}

// deniedClaude is a fake claude whose result line refuses edits of four files of the work folder: a hook, hello.txt, .ENV and .envs/dev.yaml.
const deniedClaude = `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = --version ]; then echo "2.1.330 (Claude Code)"; exit 0; fi
done
cat > /dev/null
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"result","subtype":"success","session_id":"s1","result":"done","permission_denials":[{"tool_name":"Write","tool_input":{"file_path":"'"$PWD"'/.git/hooks/pre-commit"}},{"tool_name":"Write","tool_input":{"file_path":"'"$PWD"'/hello.txt"}},{"tool_name":"Edit","tool_input":{"file_path":"'"$PWD"'/.ENV"}},{"tool_name":"Write","tool_input":{"file_path":"'"$PWD"'/.envs/dev.yaml"}}]}'
`

func TestAgents_ARefusedEditThatADenyRuleOfTheSandboxCoversIsMarkedClosed(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "claude"), deniedClaude))

	r.f.agents.adapter = func(model.Agent) agent.Adapter { return claude.New(nil) }
	r.f.agents.run = agent.Runner{}.Run
	r.f.agents.env = map[string]string{"PATH": bin + ":/bin:/usr/bin"}
	require.NoError(t, os.Mkdir(filepath.Join(r.home, "login"), 0o700))

	req := request()
	req.Sandbox, req.Resume = &model.Sandbox{}, ""

	result, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, result.Denials, 4, result.Note)

	assert.True(t, strings.HasSuffix(result.Denials[0].Target, "/.git/hooks/pre-commit"), result.Denials[0].Target)
	assert.True(t, result.Denials[0].Closed, "the closed paths of the work folder deny the hook")
	assert.True(t, strings.HasSuffix(result.Denials[1].Target, "/hello.txt"), result.Denials[1].Target)
	assert.False(t, result.Denials[1].Closed, "no deny rule covers a plain file of the work folder")
	assert.True(t, result.Denials[2].Closed, "the .env pattern covers .ENV, without case: %s", result.Denials[2].Target)
	assert.True(t, result.Denials[3].Closed, "the .env pattern covers the files of a folder it matches: %s", result.Denials[3].Target)
}

func TestDeniesWrite_CoversTheDenyPathsAndPatternsOfThePlan(t *testing.T) {
	t.Parallel()

	plan := agent.SandboxPlan{
		Deny: []string{"/h/.ssh"}, DenyPatterns: []string{"/w/.env*", "/w/**/.env*"},
		DenyWrite: []string{"/w/.git"}, DenyWritePatterns: []string{"/w/.claude/settings*.json", "/w/**/.claude/settings*.json"},
	}

	for path, want := range map[string]bool{
		"/h/.ssh/id_rsa": true, "/w/.env": true, "/w/a/.env.local": true, "/w/.git/hooks/pre-commit": true,
		"/w/.claude/settings.json": true, "/w/sub/.claude/settings.local.json": true,
		"/w/hello.txt": false, "/w/.github/ci.yml": false, "/w/.claude/notes.md": false,
		"/w/.ENV": true, "/w/.envs/dev.yaml": true, "/w/.claude/settings.json/x": true, "/H/.SSH/id_rsa": true, "/w/.GIT/config": true,
	} {
		assert.Equal(t, want, deniesWrite(plan, path), path)
	}
}

// settingsClaude is a fake claude that obeys the write grants of --settings as Claude Code does. It reads each Edit allow rule as a pattern, as
// Claude Code does, and grants a write by the shell or by the Write tool to a path that a rule matches: Claude Code merges the paths of the Edit allow
// rules into the write paths of the sandbox. It copies the settings to settings.json in the work folder and tries the file and the sibling that the
// prompt names by both routes. Each try adds "<route> <wrote|refused> <path>" to writes.log in the work folder, and the result line lists each refused
// Write call in permission_denials.
const settingsClaude = `#!/bin/sh
set -f
for arg in "$@"; do
  if [ "$arg" = --version ]; then echo "2.1.329 (Claude Code)"; exit 0; fi
done
settings=
while [ $# -gt 0 ]; do
  if [ "$1" = --settings ]; then settings=$2; fi
  shift
done
printf '%s' "$settings" > settings.json
prompt=$(cat)
grants=$(printf '%s' "$settings" | sed -n 's/.*"allow":\[\([^]]*\)\].*/\1/p' | tr ',' '\n' | sed -n 's/^"Edit(\/\(.*\))"$/\1/p')
granted() {
  for pattern in $grants; do
    case "$1" in $pattern) return 0 ;; esac
  done
  return 1
}
denials=
for key in write sibling; do
  path=$(printf '%s\n' "$prompt" | sed -n "s/^$key=//p")
  for route in shell tool; do
    if granted "$path"; then
      echo '{"verdict":"approve"}' > "$path"; echo "$route wrote $path" >> writes.log
    else
      echo "$route refused $path" >> writes.log
      if [ "$route" = tool ]; then
        denials="$denials${denials:+,}{\"tool_name\":\"Write\",\"tool_use_id\":\"t-$key\",\"tool_input\":{\"file_path\":\"$path\"}}"
      fi
    fi
  done
done
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo "{\"type\":\"result\",\"session_id\":\"s1\",\"result\":\"done\",\"permission_denials\":[$denials]}"
`

func TestAgents_TheFakeAgentWritesItsOutputFileUnderTheRealSandboxSettings(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "claude"), settingsClaude))

	r.f.agents.adapter = func(model.Agent) agent.Adapter { return claude.New(nil) }
	// The step has edits off, so it runs in a checkout that the engine discards when the step ends. The run keeps the files of the fake agent first.
	kept := t.TempDir()
	r.f.agents.run = func(ctx context.Context, spec agent.Spec) agent.Result {
		result := agent.Runner{}.Run(ctx, spec)

		for _, name := range []string{"settings.json", "writes.log"} {
			data, err := os.ReadFile(filepath.Join(spec.Dir, name)) //nolint:gosec // a file of the fake agent in the folder of the test
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(kept, name), data, 0o600))
		}

		return result
	}
	r.f.agents.env = map[string]string{"PATH": bin + ":/bin:/usr/bin"}

	outputs := filepath.Join(r.home, "state", "outputs")
	require.NoError(t, os.MkdirAll(outputs, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(r.home, "login"), 0o700))

	file, sibling := filepath.Join(outputs, "1.agent_3Async-review-1_231", "output.json"), filepath.Join(outputs, "1.agent_3Aimplement_231", "output.json")
	require.NoError(t, os.Mkdir(filepath.Dir(file), 0o700))
	require.NoError(t, os.Mkdir(filepath.Dir(sibling), 0o700))
	req := outputRequest(file)
	req.Prompt = "write=" + file + "\nsibling=" + sibling + "\n"

	result, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	require.Empty(t, result.Note)

	settings, err := os.ReadFile(filepath.Join(kept, "settings.json"))
	require.NoError(t, err)
	assertOnlyWriteGrant(t, decodeOutputSettings(t, string(settings)), file, sibling, outputs, r.work)

	written, err := os.ReadFile(file) //nolint:gosec // the output file of the test
	require.NoError(t, err, "the agent wrote its output file")
	assert.JSONEq(t, `{"verdict":"approve"}`, string(written))

	log, err := os.ReadFile(filepath.Join(kept, "writes.log"))
	require.NoError(t, err)
	assert.Equal(t, "shell wrote "+file+"\ntool wrote "+file+"\nshell refused "+sibling+"\ntool refused "+sibling+"\n", string(log))
	assert.NoFileExists(t, sibling)
	assert.Equal(t, []agent.Denial{{Tool: "Write", Target: sibling, Edit: true}}, result.Denials, "the result carries the refused Write call")
}
