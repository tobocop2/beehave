package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/agent/claude"
)

func TestRunner_ALaunchCopiesItsFoldersIntoTheRunFolderBeforeTheCLIStarts(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "hooks.json"), []byte("copied-content"), 0o600))

	adapter := launchAdapter{Adapter: claude.New(nil), launch: func(runDir string) agent.Launch {
		return agent.Launch{Env: map[string]string{"STUB_DIR": runDir}, Copies: map[string]string{"conf": source}}
	}}
	f, records := launchFixture(t, adapter)

	got := agent.Runner{}.Run(context.Background(), f.spec)

	require.Equal(t, agent.OutcomeExited, got.Outcome, got.Note)
	assert.Equal(t, "copied-content", record(t, records, "seen"), "the copy was in the run folder while the CLI ran")
	assert.NoDirExists(t, record(t, records, "dir"), "the copy goes with the run folder")
}

func TestRunner_ALaunchCopyThatCannotBeMadeDoesNotStartTheCLI(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	for name, copies := range map[string]map[string]string{
		"a_target_outside_the_run_folder": {"../escape": t.TempDir()},
		"a_source_that_does_not_exist":    {"conf": filepath.Join(t.TempDir(), "missing")},
	} {
		adapter := launchAdapter{Adapter: claude.New(nil), launch: func(string) agent.Launch { return agent.Launch{Copies: copies} }}
		f, records := launchFixture(t, adapter)

		got := agent.Runner{}.Run(context.Background(), f.spec)

		assert.Equal(t, agent.OutcomeNotStarted, got.Outcome, name)
		assert.Contains(t, got.Note, "copy", name)
		assert.NoFileExists(t, filepath.Join(records, "args"), name)
	}
}
