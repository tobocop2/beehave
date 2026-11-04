package agent_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestRunner_ALaunchFileThatCannotBeWrittenDoesNotStartTheCLI(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	cases := map[string]map[string]string{
		"a_folder_that_is_a_file": {"a": "x", "a/b": "y"},
		"a_file_that_is_a_folder": {"z/f": "x", "z/f/..": "y"},
	}

	for name, files := range cases {
		adapter := launchAdapter{Adapter: claude.New(), launch: func(string) agent.Launch { return agent.Launch{Files: files} }}
		f, records := launchFixture(t, adapter)

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, name)
		assert.Contains(t, got.Note, "launch file", name)
		assert.NoFileExists(t, filepath.Join(records, "args"), name)
	}
}

func TestRunner_ARunFolderThatCannotBeMadeDoesNotStartTheCLI(t *testing.T) {
	f, records := launchFixture(t, launchAdapter{Adapter: claude.New(), launch: stubLaunch})
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	got := agent.Runner{}.Run(context.Background(), f.spec)

	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "cannot make the run folder")
	assert.NoFileExists(t, filepath.Join(records, "args"))
}

// foldersOf returns a launch function that gives files to the run, and the function that lists the run folders it was given.
func foldersOf(files map[string]string) (func(string) agent.Launch, func() []string) {
	var seen []string

	launch := func(runDir string) agent.Launch {
		seen = append(seen, runDir)

		return agent.Launch{Files: files}
	}

	return launch, func() []string { return seen }
}

func TestRunner_TheRunFolderIsGoneAfterARefusalThatCameAfterItWasMade(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	for name, files := range map[string]map[string]string{
		"a_path_outside_the_folder": {"../escape": "x"},
		"a_file_that_cannot_exist":  {"a": "x", "a/b": "y"},
	} {
		launch, folders := foldersOf(files)
		f, _ := launchFixture(t, launchAdapter{Adapter: claude.New(), launch: launch})

		got := agent.Runner{}.Run(context.Background(), f.spec)

		require.Equal(t, agent.OutcomeNotStarted, got.Outcome, name)
		require.Len(t, folders(), 1, name)
		assert.NoDirExists(t, folders()[0], name)
	}
}

func TestRunner_TheRunFolderIsGoneAfterACancelledRun(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	launch, folders := foldersOf(map[string]string{"conf/hooks.json": "x"})
	f := newFixture(t, "claude", model.AgentClaude, "holdgrand")
	f.spec.Adapter, f.spec.Guard, f.spec.Log = launchAdapter{Adapter: claude.New(), launch: launch}, "obee check command", &bytes.Buffer{}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		for !fileExists(filepath.Join(f.records, "grandchild")) {
			time.Sleep(20 * time.Millisecond)
		}

		cancel()
	}()

	got := agent.Runner{}.Run(ctx, f.spec)

	require.Equal(t, agent.OutcomeCancelled, got.Outcome)
	require.Len(t, folders(), 1)
	assert.NoDirExists(t, folders()[0])
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
