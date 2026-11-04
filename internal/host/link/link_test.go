package link_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host/link"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

func TestRun_ReturnsStdout(t *testing.T) {
	t.Parallel()

	out, err := link.Run(t.Context(), proctest.Always(model.CommandResult{Stdout: "ok\n"}), "sky status", []string{"status"})

	require.NoError(t, err)
	assert.Equal(t, "ok\n", out)
}

func TestRun_WrapsTheFailureWithTheNameExitAndStderr(t *testing.T) {
	t.Parallel()

	_, err := link.Run(t.Context(), proctest.Always(model.CommandResult{ExitCode: 3, Stderr: " boom \n"}), "sky down", nil)

	require.ErrorIs(t, err, link.ErrCommand)
	assert.EqualError(t, err, "command failed: sky down: exit 3: boom")
}

func TestRemote_QuotesEachWordAndPutsTheTargetFirst(t *testing.T) {
	t.Parallel()

	ssh := proctest.Always(model.CommandResult{Stdout: "x"})

	out, err := link.New("head", ssh).Remote(t.Context(), "echo", "echo", "a b", "$HOME")

	require.NoError(t, err)
	assert.Equal(t, "x", out)
	assert.Equal(t, [][]string{{"-o", "BatchMode=yes", "head", model.ShellQuote("echo"), model.ShellQuote("a b"), model.ShellQuote("$HOME")}}, ssh.Calls)
}

func TestRemote_WrapsAFailure(t *testing.T) {
	t.Parallel()

	_, err := link.New("head", proctest.Always(model.CommandResult{ExitCode: 1, Stderr: "no"})).Remote(t.Context(), "df", "df")

	require.ErrorIs(t, err, link.ErrCommand)
}

func TestRunner_RunsTheBinaryOnTheHost(t *testing.T) {
	t.Parallel()

	ssh := proctest.Always(model.CommandResult{})

	link.New("head", ssh).Runner("git").Run(t.Context(), []string{"status", "-s"})

	assert.Equal(t, [][]string{{"-o", "BatchMode=yes", "head", model.ShellQuote("git"), model.ShellQuote("status"), model.ShellQuote("-s")}}, ssh.Calls)
}

func TestDrop_IsAnErrorForExitCode255Only(t *testing.T) {
	t.Parallel()

	l := link.New("head", proctest.Always(model.CommandResult{}))

	assert.NoError(t, l.Drop(model.CommandResult{ExitCode: 1}))
	assert.NoError(t, l.Drop(model.CommandResult{}))

	err := l.Drop(model.CommandResult{ExitCode: 255, Stderr: "no route\n"})

	require.ErrorIs(t, err, link.ErrDropped)
	assert.EqualError(t, err, "the ssh link to the host dropped: ssh to head: no route")
}
