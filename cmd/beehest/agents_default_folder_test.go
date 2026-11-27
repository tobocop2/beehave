package main

import (
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

// doneClaude is a fake claude that names a session and ends with a result line.
const doneClaude = `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = --version ]; then echo "2.1.330 (Claude Code)"; exit 0; fi
done
cat > /dev/null
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"result","subtype":"success","session_id":"s1","result":"done"}'
`

// defaultFolderRig is an agents value over the real agent runner, a fake claude, and a runner with no config_dir.
func defaultFolderRig(t *testing.T) *sandboxRig {
	t.Helper()

	r := newSandboxRig(t)
	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "claude"), doneClaude))

	r.f.agents.adapter = func(model.Agent) agent.Adapter { return claude.New(nil) }
	r.f.agents.run = agent.Runner{}.Run
	r.f.agents.env = map[string]string{"PATH": bin + ":/bin:/usr/bin"}
	r.f.agents.runners[0].ConfigDir = ""

	return r
}

func TestAgents_TheRealRunnerStartsARunnerWithNoConfigFolderWhenTheDefaultFolderExists(t *testing.T) {
	t.Parallel()

	r := defaultFolderRig(t)
	require.NoError(t, os.Mkdir(filepath.Join(r.home, ".claude"), 0o700))

	req := request()
	req.Resume = ""

	result, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeExited, result.Outcome, result.Note)
	assert.Contains(t, result.Transcript, filepath.Join(r.home, ".claude", "projects"), "the transcript is in the default folder")
}

func TestAgents_TheRealRunnerDoesNotStartARunnerWithNoConfigFolderAndNoDefaultFolder(t *testing.T) {
	t.Parallel()

	r := defaultFolderRig(t)

	req := request()
	req.Resume = ""

	result, err := r.f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, result.Outcome)
	assert.Contains(t, result.Note, filepath.Join(r.home, ".claude"), "the note names the folder that is missing")
	assert.Contains(t, result.Note, "config_dir", "the note names the fix")
}
