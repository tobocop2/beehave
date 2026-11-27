package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/agent/claude"
	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/fstree"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/sandbox"
)

// engineDeps builds the deps of the engine as the run command does.
func engineDeps(t *testing.T) flow.Deps {
	t.Helper()

	cfg := config.Config{Projects: sandboxedProject(model.SandboxOn, config.LocalHost)}

	return depsOn(cfg, flow.Options{StateDir: t.TempDir()}, logins{}, newAdapters(nil, nil, nil))
}

// leftover plants the folder of a killed engine that had the pid of this test process, and returns its path.
func leftover(t *testing.T, root string) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(root, 0o700))

	path := filepath.Join(root, fstree.Name(os.Getpid(), fstree.Attempts-1))
	require.NoError(t, os.Mkdir(path, 0o700))

	return path
}

func TestDepsOn_TheGateOfTheEngineReclaimsAFolderOfAKilledEngineWithThePidOfTheProcess(t *testing.T) { //nolint:paralleltest // t.Setenv changes TMPDIR for the process
	notRoot(t)

	old := leftover(t, ownTemp(t))
	runs, ok := engineDeps(t).Commands.(programs)
	require.True(t, ok, "the engine builds programs")

	box := runs.sandbox
	box.enforcer = func(string) (sandbox.Enforcer, error) { return &fakeEnforcer{}, nil }

	_, code := box.run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

	require.Zero(t, code)
	assert.NoDirExists(t, old, "the set of the engine did not make the folder, so it is a leftover")
}

func TestDepsOn_TheAgentRunnerOfTheEngineReclaimsAFolderOfAKilledEngineWithThePidOfTheProcess(t *testing.T) { //nolint:paralleltest // the runner makes its folders in /tmp, which other tests share
	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "claude"), "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '2.1.330 (Claude Code)'; fi\ncat > /dev/null\n"))

	work, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	old := leftover(t, filepath.Join("/tmp", "b"+strconv.FormatInt(int64(os.Getuid()), 36)))
	t.Cleanup(func() { _ = fstree.Remove(old) }) //nolint:errcheck // the code under test removes it; a red run leaves it for this

	built, ok := engineDeps(t).Agents.(agents)
	require.True(t, ok, "the engine builds agents")

	// The outcome is not asserted: the runner reclaims before it starts the CLI, and a host without the platform sandbox refuses the start after that.
	built.run(t.Context(), agent.Spec{
		Adapter: claude.New(nil), Runner: model.Runner{Name: "main", Agent: claude.Name}, ConfigFolder: t.TempDir(), Dir: work, Prompt: "go\n",
		Env: map[string]string{"PATH": bin + ":/bin:/usr/bin", "HOME": t.TempDir()}, Sandbox: &agent.SandboxPlan{Work: work},
	})

	assert.NoDirExists(t, old, "the set of the engine did not make the folder, so it is a leftover")
}
