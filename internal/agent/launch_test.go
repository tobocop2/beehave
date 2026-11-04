package agent_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/exectest"
	"github.com/tobocop2/obee/internal/model"
)

// launchScript records, for one run, what it sees of the launch: the arguments, the run folder, its mode, the content of one file and one variable.
const launchScript = `#!/bin/sh
here=$(dirname "$0")
echo "$*" > "$here/args"
cat > /dev/null
echo "$STUB_DIR" > "$here/dir"
ls -ld "$STUB_DIR" | cut -c1-10 > "$here/mode"
cat "$STUB_DIR/conf/hooks.json" > "$here/seen"
echo "$STUB_VALUE" > "$here/value"
echo "$CLAUDE_CONFIG_DIR" > "$here/login"
`

// launchAdapter is an adapter whose guard and sandbox return the launch in the field.
type launchAdapter struct {
	agent.Adapter
	launch func(runDir string) agent.Launch
	resume bool
}

func (a launchAdapter) Profile() agent.Profile {
	p := a.Adapter.Profile()
	p.Resume, p.ResumeLast = func(id string) []string { return []string{"resume", id, "-"} }, a.resume

	return p
}

func (a launchAdapter) Guard(_, runDir string) agent.Launch { return a.launch(runDir) }

func (a launchAdapter) Sandbox(_ agent.SandboxPlan, _, runDir string) agent.Launch {
	return a.launch(runDir)
}

func stubLaunch(runDir string) agent.Launch {
	return agent.Launch{
		Args:  []string{"--conf", filepath.Join(runDir, "conf", "hooks.json")},
		Env:   map[string]string{"STUB_DIR": runDir, "STUB_VALUE": "from-launch", "CLAUDE_CONFIG_DIR": "/other/login"},
		Files: map[string]string{"conf/hooks.json": "hook-content"},
	}
}

func launchFixture(t *testing.T, adapter agent.Adapter) (*fixture, string) {
	t.Helper()

	f := newFixture(t, "claude", model.AgentClaude, "")
	require.NoError(t, exectest.Write(filepath.Join(f.records, "claude"), launchScript))

	f.spec.Adapter = adapter
	f.spec.Guard = "obee check command"
	f.spec.Log = &bytes.Buffer{}

	return f, f.records
}

func TestRunner_ALaunchWritesItsFilesInAPrivateFolderThatIsGoneAfterTheRun(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f, records := launchFixture(t, launchAdapter{Adapter: claude.New(), launch: stubLaunch})

	got := agent.Runner{}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)

	dir := record(t, records, "dir")
	assert.Equal(t, "hook-content", record(t, records, "seen"), "the file was in the run folder while the CLI ran")
	assert.Equal(t, "drwx------", record(t, records, "mode"))
	assert.Contains(t, record(t, records, "args"), "--conf "+filepath.Join(dir, "conf", "hooks.json"))

	_, err := os.Stat(dir)
	assert.ErrorIs(t, err, os.ErrNotExist, "the run folder is removed when the run ends")
}

func TestRunner_ALaunchEnvIsAddedButNeverReplacesTheLogin(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f, records := launchFixture(t, launchAdapter{Adapter: claude.New(), launch: stubLaunch})

	agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, "from-launch", record(t, records, "value"))
	assert.Equal(t, f.config, record(t, records, "login"), "a launch cannot point the CLI at another login")
}

func TestRunner_AResumeSubcommandComesAfterEveryOtherArgument(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	f, records := launchFixture(t, launchAdapter{Adapter: claude.New(), launch: stubLaunch, resume: true})
	f.spec.Model, f.spec.Resume = "opus", "sess-1"

	got := agent.Runner{}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)
	assert.True(t, strings.HasSuffix(record(t, records, "args"), " resume sess-1 -"), record(t, records, "args"))
	assert.Contains(t, record(t, records, "args"), "--model opus --conf")
}

func TestRunner_ALaunchFileOutsideTheRunFolderDoesNotStartTheCLI(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	for _, path := range []string{"../escape", "/abs/path", "a/../../b", ""} {
		adapter := launchAdapter{Adapter: claude.New(), launch: func(string) agent.Launch {
			return agent.Launch{Files: map[string]string{path: "x"}}
		}}
		f, records := launchFixture(t, adapter)

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, path)
		assert.Contains(t, got.Note, "outside its run folder", path)
		assert.NoFileExists(t, filepath.Join(records, "args"), path)
	}
}
