package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/agent/claude"
	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/model"
)

// readFirstClaude is a fake claude that models two checks of Claude Code 2.1.330 on the file tools. The Write tool refuses a file that exists and that
// the session has not read, as a tool error. A Read outside the work folder and the --add-dir folders is a permission denial. The prompt names the output
// file (write=) and the file of another call (peek=). The fake writes the output file, reads it first when the Write is refused, and tries to read the other file.
const readFirstClaude = `#!/bin/sh
if [ "$1" = --version ]; then echo "2.1.330 (Claude Code)"; exit 0; fi
dirs=$(pwd)
while [ $# -gt 0 ]; do
  if [ "$1" = --add-dir ]; then
    shift
    while [ $# -gt 0 ] && [ "${1#--}" = "$1" ]; do dirs="$dirs
$1"; shift; done
    continue
  fi
  shift
done
prompt=$(cat)
out=$(printf '%s\n' "$prompt" | sed -n 's/^write=//p')
peek=$(printf '%s\n' "$prompt" | sed -n 's/^peek=//p')
denials=
working() { printf '%s\n' "$dirs" | while read -r d; do case "$1" in "$d"/*) echo yes; break;; esac; done; }
read_call() {
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"'"$2"'","name":"Read","input":{"file_path":"'"$1"'"}}]}}'
  if [ "$(working "$1")" = yes ]; then return 0; fi
  echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"'"$2"'","is_error":true,"content":"'"$1"' is outside the working directories"}]}}'
  denials="$denials${denials:+,}"'{"tool_name":"Read","tool_use_id":"'"$2"'","tool_input":{"file_path":"'"$1"'"}}'
  return 1
}
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"w1","name":"Write","input":{"file_path":"'"$out"'"}}]}}'
if [ -e "$out" ]; then
  echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"w1","is_error":true,"content":"File has not been read yet. Read it first before writing to it."}]}}'
  if read_call "$out" r1; then printf '{"regressions":"0"}\n' > "$out"; fi
else
  printf '{"regressions":"0"}\n' > "$out"
fi
read_call "$peek" r2
echo '{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s1","total_cost_usd":0,"permission_denials":['"$denials"']}'
`

func TestAgents_ASandboxedCallWritesItsEmptyOutputFileWithTheWriteToolAndCannotReadAnotherCall(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	tools := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(tools, "claude"), readFirstClaude))

	outputs := filepath.Join(r.home, ".local", "state", "beehest", "outputs")
	file, other := filepath.Join(outputs, "7.agent_3Aplan_231", "output.json"), filepath.Join(outputs, "8.agent_3Aplan_231", "output.json")
	r.touch(t, file)
	r.touch(t, other)

	r.f.agents.adapter = newAdapters(nil, nil, nil).agent
	r.f.agents.run = func(ctx context.Context, spec agent.Spec) agent.Result { return agent.Runner{}.Run(ctx, spec) }
	r.f.agents.env = map[string]string{"PATH": tools + string(os.PathListSeparator) + "/bin" + string(os.PathListSeparator) + "/usr/bin"}
	r.f.agents.runners = []model.Runner{{Name: "main", Agent: claude.Name, ConfigDir: filepath.Join(r.home, "login")}}
	require.NoError(t, os.MkdirAll(filepath.Join(r.home, "login"), 0o700))

	req := request()
	req.Resume, req.Model = "", ""
	req.Sandbox, req.OutputFile, req.Outputs, req.NoEdits = &model.Sandbox{}, file, []string{"regressions"}, true
	req.Prompt = "write=" + file + "\npeek=" + other + "\n"

	result, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)

	written, err := os.ReadFile(file) //nolint:gosec // the output file of the test
	require.NoError(t, err)
	assert.JSONEq(t, `{"regressions":"0"}`, string(written), "the Write lands after the Read of the file in its own working directory")
	assert.Equal(t, []agent.Denial{{Tool: "Read", Target: other}}, result.Denials, "the output file of another call stays outside the working directories")
}
