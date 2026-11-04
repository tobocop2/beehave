package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
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

func TestDoctor_BdVersionAcceptsANewBdAndRefusesAnOldOne(t *testing.T) {
	t.Parallel()

	require.NoError(t, newDoctor(versionRunner{stdout: "bd version 1.3.2 (abc)\n"}, nil, nil).BdVersion(context.Background()))
	require.ErrorContains(t, newDoctor(versionRunner{stdout: "bd version 0.56.1 (abc)\n"}, nil, nil).BdVersion(context.Background()), "found 0.56.1")
}

func TestDoctor_ReadUsageReadsTheLoginOfAClaudeRunner(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "absent")
	reading := newDoctor(nil, nil, nil).ReadUsage(context.Background(), model.Runner{Name: "a", Agent: model.AgentClaude, ConfigDir: missing})

	_, known := reading.Usage()
	require.False(t, known)
	require.Contains(t, reading.Reason(), "does not exist")
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

	readable := newDoctor(nil, proc.NewExecRunner("security", ""), nil).TokenReadable

	require.True(t, readable(context.Background(), "beehave-a"))
	require.False(t, readable(context.Background(), "beehave-missing"))
}

func TestDoctor_TokenReadableIsFalseForAnEmptyTokenAndForAFailingSecurity(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeSecurity(t, "echo\n")

	readable := newDoctor(nil, proc.NewExecRunner("security", ""), nil).TokenReadable
	require.False(t, readable(context.Background(), "beehave-a"))

	fakeSecurity(t, "echo secret >&2\nexit 44\n")
	require.False(t, readable(context.Background(), "beehave-a"))
}
