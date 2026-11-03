package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

func found(string) (string, error) { return "/bin/srt", nil }

func sandboxedProject(mode model.SandboxMode, host string) map[string]config.Project {
	return map[string]config.Project{"p": {Project: model.Project{
		Name: "p", Gate: "make check", BuildHost: host, Sandbox: mode, Host: &model.HostConfig{},
		SandboxRead: []string{"/opt/tools"}, GateNetwork: []string{"proxy.golang.org"}, GateAllowWrite: []string{"cache"},
	}}}
}

func boxedGate(t *testing.T, projects map[string]config.Project, calls *gateCalls) projectGate {
	t.Helper()

	return projectGate{
		projects: projects,
		programs: programs{trees: treeAt(t.TempDir()), local: gateRunner(calls, model.CommandResult{Stdout: "abc123\n"}, model.CommandResult{Stdout: "ok"})},
		home:     func() (string, error) { return "/home/o", nil }, lookPath: found,
	}
}

func TestProjectGate_ASandboxedLocalGateRunsThroughSrtWithTheProjectLists(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := boxedGate(t, sandboxedProject(model.SandboxOn, config.LocalHost), calls)

	result, err := gate.Run(t.Context(), "p", "1", flow.GateJob{})

	require.NoError(t, err)
	assert.True(t, result.Passed)
	assert.NotContains(t, calls.binaries, "sh", "the gate command never runs outside the sandbox")

	var args []string

	for i, binary := range calls.binaries {
		if binary == "srt" {
			args = calls.args[i]
		}
	}

	require.Len(t, args, 4)
	assert.Equal(t, []string{"--settings", args[1], "-c", "make check"}, args)
}

func TestProjectGate_TheSrtSettingsDenyTheHomeAndHoldTheNetworkAndWriteListsOfTheProject(t *testing.T) {
	t.Parallel()

	var raw string

	calls := &gateCalls{}
	gate := boxedGate(t, sandboxedProject(model.SandboxOn, config.LocalHost), calls)
	inner := gate.programs.local
	gate.programs.local = func(binary, dir string, timeout time.Duration) proc.Runner {
		if binary != "srt" {
			return inner(binary, dir, timeout)
		}

		return recording{run: func(args []string) model.CommandResult {
			data, err := os.ReadFile(args[1])
			require.NoError(t, err)

			raw = string(data)

			return model.CommandResult{Stdout: "ok"}
		}}
	}

	_, err := gate.Run(t.Context(), "p", "1", flow.GateJob{})

	require.NoError(t, err)
	assert.Contains(t, raw, `"allowedDomains":["proxy.golang.org"]`)
	assert.Contains(t, raw, `"denyRead":["/home/o"`)
	assert.Contains(t, raw, "/opt/tools")
	assert.Contains(t, raw, "/cache")
	assert.Contains(t, raw, "/home/o/.ssh")
}

func TestProjectGate_AMissingSrtRefusesTheGateWithANoteAndRunsNothing(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := boxedGate(t, sandboxedProject(model.SandboxOn, config.LocalHost), calls)
	gate.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	_, err := gate.Run(t.Context(), "p", "1", flow.GateJob{})

	require.ErrorContains(t, err, "srt, the sandbox runtime, is not installed")
	assert.Empty(t, calls.binaries, "no program starts, not even git")
}

func TestProjectGate_APlanThatCannotStartRefusesTheGate(t *testing.T) {
	t.Parallel()

	projects := sandboxedProject(model.SandboxOn, config.LocalHost)
	project := projects["p"]
	project.SandboxRead = []string{"/home/o"}
	projects["p"] = project

	calls := &gateCalls{}
	_, err := boxedGate(t, projects, calls).Run(t.Context(), "p", "1", flow.GateJob{})

	require.ErrorContains(t, err, "the gate did not run")
	require.ErrorContains(t, err, "sandbox_read")
	assert.NotContains(t, calls.binaries, "sh")
}

func TestProjectGate_ASrtThatCannotStartRefusesTheGateInsteadOfFailingIt(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := boxedGate(t, sandboxedProject(model.SandboxOn, config.LocalHost), calls)
	gate.programs.local = gateRunner(calls, model.CommandResult{Stdout: "abc123\n"}, model.CommandResult{Stderr: "bwrap: loopback: Failed RTM_NEWADDR", ExitCode: 1})

	result, err := gate.Run(t.Context(), "p", "1", flow.GateJob{})

	require.ErrorContains(t, err, "the sandbox could not start")
	require.ErrorContains(t, err, "bwrap: loopback")
	assert.False(t, result.Passed)

	var gateRuns int

	for i, binary := range calls.binaries {
		if binary == "srt" && calls.args[i][3] == "make check" {
			gateRuns++
		}
	}

	assert.Zero(t, gateRuns, "the gate command never ran: only the probe did")
}

func TestProjectGate_TheDefaultLookupsFindSrtOnThePathAndTheHomeFromTheEnvironment(t *testing.T) {
	bin := t.TempDir()
	home := t.TempDir()

	t.Setenv("PATH", bin)
	t.Setenv("HOME", home)

	_, err := projectGate{}.srtHome()
	require.ErrorContains(t, err, "srt, the sandbox runtime, is not installed")

	require.NoError(t, os.WriteFile(filepath.Join(bin, "srt"), []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // exec.LookPath finds only an executable file

	got, err := projectGate{}.srtHome()

	require.NoError(t, err)
	assert.Equal(t, home, got)
}

func TestProjectGate_ANoHomeFolderRefusesTheGateBeforeAnyProgramStarts(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := boxedGate(t, sandboxedProject(model.SandboxOn, config.LocalHost), calls)
	gate.home = func() (string, error) { return "", errors.New("no home") }

	_, err := gate.Run(t.Context(), "p", "1", flow.GateJob{})

	require.ErrorContains(t, err, "cannot find the home folder")
	assert.Empty(t, calls.binaries)
}

func TestProjectGate_ASettingsFileThatCannotBeWrittenRefusesTheGate(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))

	calls := &gateCalls{}
	_, err := boxedGate(t, sandboxedProject(model.SandboxOn, config.LocalHost), calls).Run(t.Context(), "p", "1", flow.GateJob{})

	require.ErrorContains(t, err, "write the sandbox settings")
	assert.NotContains(t, calls.binaries, "sh")
}

func TestProjectGate_ASandboxOffGateAndARemoteGateRunWithNoSrt(t *testing.T) {
	t.Parallel()

	for name, projects := range map[string]map[string]config.Project{
		"off":    sandboxedProject(model.SandboxOff, config.LocalHost),
		"remote": sandboxedProject(model.SandboxOn, "cluster"),
	} {
		gate := boxedGate(t, projects, &gateCalls{})
		gate.lookPath = func(string) (string, error) { return "", errors.New("not found") }

		assert.False(t, gate.sandboxed("p"), name)
	}

	calls := &gateCalls{}
	_, err := boxedGate(t, sandboxedProject(model.SandboxOff, config.LocalHost), calls).Run(t.Context(), "p", "1", flow.GateJob{})

	require.NoError(t, err)
	assert.NotContains(t, calls.binaries, "srt")
	assert.Contains(t, calls.binaries, "sh")
}
