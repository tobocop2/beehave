package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
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

	require.NoError(t, newDoctor(versionRunner{stdout: "bd version 1.3.2 (abc)\n"}, nil).BdVersion(context.Background()))
	require.ErrorContains(t, newDoctor(versionRunner{stdout: "bd version 0.56.1 (abc)\n"}, nil).BdVersion(context.Background()), "found 0.56.1")
}

// fakeSecurity puts a `security` script that prints the value for its entry on a PATH of its own.
func fakeSecurity(t *testing.T, body string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "security"), []byte("#!/bin/sh\n"+body), 0o700)) //nolint:gosec // a fake program must be executable
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

func TestDoctor_TokenReadableIsTrueOnlyForAnEntryThatHoldsAToken(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeSecurity(t, "[ \"$1 $2 $3 $4\" = \"find-generic-password -s obee-a -w\" ] && echo secret-token\n")

	readable := newDoctor(nil, proc.NewExecRunner("security", "")).TokenReadable

	require.True(t, readable(context.Background(), "obee-a"))
	require.False(t, readable(context.Background(), "obee-missing"))
}

func TestDoctor_TokenReadableIsFalseForAnEmptyTokenAndForAFailingSecurity(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeSecurity(t, "echo\n")

	readable := newDoctor(nil, proc.NewExecRunner("security", "")).TokenReadable
	require.False(t, readable(context.Background(), "obee-a"))

	fakeSecurity(t, "echo secret >&2\nexit 44\n")
	require.False(t, readable(context.Background(), "obee-a"))
}
