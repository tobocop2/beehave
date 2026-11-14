package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/agent/opencode"
	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store/beads"
)

type versionRunner struct{ stdout string }

func (r versionRunner) Run(context.Context, []string) model.CommandResult {
	return model.CommandResult{Stdout: r.stdout}
}

func TestSecurityRunner_StopsACallThatWaitsOnALockedKeychainAfterTenSeconds(t *testing.T) {
	t.Parallel()

	runner := newSecurityRunner()

	require.Equal(t, "security", runner.Binary)
	require.Equal(t, 10*time.Second, runner.Timeout)
}

func TestDoctor_TheBdVersionCheckAcceptsANewBdAndRefusesAnOldOne(t *testing.T) {
	t.Parallel()

	newBd := newDoctor(newAdapters(versionRunner{stdout: "bd version 1.3.2 (abc)\n"}, nil, nil), nil).ToolVersions[beads.Tool]
	oldBd := newDoctor(newAdapters(versionRunner{stdout: "bd version 0.56.1 (abc)\n"}, nil, nil), nil).ToolVersions[beads.Tool]

	require.NoError(t, newBd(context.Background()))
	require.ErrorContains(t, oldBd(context.Background()), "found 0.56.1")
}

func TestDoctor_OnlyBdHasAVersionCheck(t *testing.T) {
	t.Parallel()

	versions := newDoctor(newAdapters(nil, nil, nil), nil).ToolVersions

	require.Len(t, versions, 1)
	require.Contains(t, versions, beads.Tool)
}

func TestDoctor_ATableWithNoAgentDeclaresEveryReaderEmptyNotMissing(t *testing.T) {
	t.Parallel()

	doctor := newDoctor(adapters{}, nil)

	require.NotNil(t, doctor.Logins)
	require.NotNil(t, doctor.ConfigDirFixes)
	require.NotNil(t, doctor.TokenFixes)
	require.Empty(t, doctor.Logins)
	require.Empty(t, doctor.ConfigDirFixes)
	require.Empty(t, doctor.TokenFixes)
}

func TestDoctor_TheLoginOfAClaudeRunnerIsReadThroughTheAdapter(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "absent")
	read := newDoctor(newAdapters(nil, nil, nil), nil).Logins[claude.Name]
	reading := read(context.Background(), model.Runner{Name: "a", Agent: claude.Name, ConfigDir: missing})

	_, known := reading.Usage()
	require.False(t, known)
	require.Contains(t, reading.Reason(), "does not exist")
}

func TestDoctor_TheLoginsAndTheFixesComeFromTheAgentsOfTheTable(t *testing.T) {
	t.Parallel()

	doctor := newDoctor(newAdapters(nil, nil, nil), nil)

	require.Len(t, doctor.Logins, 1)
	require.Equal(t, "Log in to it: CLAUDE_CONFIG_DIR=/login claude", doctor.ConfigDirFixes[claude.Name]("/login"))
	require.Equal(t, "Run `claude setup-token`, then: security add-generic-password -s entry -a $USER -w", doctor.TokenFixes[claude.Name]("entry"))
	require.Nil(t, doctor.ConfigDirFixes[opencode.Name])
	require.Nil(t, doctor.TokenFixes[opencode.Name])
}

func TestDoctor_AnAgentEntryWithNoFixTextAddsNoFix(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, nil, nil)
	table.agents = map[model.Agent]agentAdapter{opencode.Name: {build: table.agents[claude.Name].build}}
	doctor := newDoctor(table, nil)

	require.Empty(t, doctor.ConfigDirFixes)
	require.Empty(t, doctor.TokenFixes)
	require.Contains(t, doctor.Logins, opencode.Name)
}

// fakeSecurity puts a `security` script that prints the value for its entry on a PATH of its own.
func fakeSecurity(t *testing.T, body string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(dir, "security"), "#!/bin/sh\n"+body))
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

func TestDoctor_TokenReadableIsTrueOnlyForAnEntryThatHoldsAToken(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeSecurity(t, "[ \"$1 $2 $3 $4\" = \"find-generic-password -s beehave-a -w\" ] && echo secret-token\n")

	readable := newDoctor(newAdapters(nil, nil, nil), proc.NewExecRunner("security", "")).TokenReadable

	require.True(t, readable(context.Background(), "beehave-a"))
	require.False(t, readable(context.Background(), "beehave-missing"))
}

func TestDoctor_TokenReadableIsFalseForAnEmptyTokenAndForAFailingSecurity(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeSecurity(t, "echo\n")

	readable := newDoctor(newAdapters(nil, nil, nil), proc.NewExecRunner("security", "")).TokenReadable
	require.False(t, readable(context.Background(), "beehave-a"))

	fakeSecurity(t, "echo secret >&2\nexit 44\n")
	require.False(t, readable(context.Background(), "beehave-a"))
}

func TestBuildInfo_TheDoctorReadsALoginThroughTheClaudeCLIOnThePathOfTheCallerEnvironment(t *testing.T) {
	t.Parallel()

	tools := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tools, "claude"), []byte("#!/bin/sh\necho fake-claude-ran >&2\nexit 7\n"), e2eToolMode))

	info := buildInfo(map[string]string{"PATH": tools, "HOME": t.TempDir()}, "dev", cli.Flow{})
	reading := info.Doctor.Logins[claude.Name](t.Context(), model.Runner{Name: "a", Agent: claude.Name, ConfigDir: t.TempDir()})

	require.Equal(t, "claude exited 7: fake-claude-ran", reading.Reason())
}
