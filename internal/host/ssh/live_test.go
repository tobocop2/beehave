//go:build live

package ssh_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/model"
)

// localSSH runs the remote words on this machine, through sh as the login shell of a host would. The scheduler does not know the link, so the
// command lines of systemd-run and systemctl are the real ones.
type localSSH struct{}

func (localSSH) Run(ctx context.Context, args []string) model.CommandResult {
	at := 0

	for i, arg := range args {
		if arg == "--" {
			at = i
		}
	}

	out, err := exec.CommandContext(ctx, "sh", "-c", strings.Join(args[at+2:], " ")).CombinedOutput() //nolint:gosec // the words are quoted by the link
	if err == nil {
		return model.CommandResult{Stdout: string(out)}
	}

	var exit *exec.ExitError
	if !asExit(err, &exit) {
		return model.CommandResult{ExitCode: 255, Stderr: err.Error()}
	}

	return model.CommandResult{ExitCode: exit.ExitCode(), Stdout: string(out), Stderr: string(out)}
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError) //nolint:errorlint // exec returns the value itself

	if ok {
		*target = e
	}

	return ok
}

// TestLive_AJobRunsAsASystemdUserUnit runs the adapter against the systemd of this machine. It needs linger for the login user (Up refuses otherwise)
// and the variable BEEHAVE_LIVE_SYSTEMD. Every unit is named beehave-livetest* and every file lives in a folder named beehave-live-*.
func TestLive_AJobRunsAsASystemdUserUnit(t *testing.T) {
	if os.Getenv("BEEHAVE_LIVE_SYSTEMD") == "" {
		t.Skip("set BEEHAVE_LIVE_SYSTEMD=1 to run against the systemd of this machine")
	}

	root, err := os.MkdirTemp("", "beehave-live-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })

	n := 0
	cfg := model.HostConfig{Provider: ssh.Name, MaxRun: 2 * time.Minute, StaleMinutes: 5, Block: model.NewBlock("test", []model.BlockEntry{model.TextEntry(ssh.BlockWorkRoot, root)})}

	h, err := ssh.New("localhost", cfg, localSSH{}, ssh.WithBank(func(context.Context) error { return nil }),
		ssh.WithNonce(func() string { n++; return "livetest" + strconv.Itoa(n) }))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()

	require.NoError(t, h.Up(ctx))

	wait := func(command string) model.JobState {
		id, err := h.Submit(ctx, model.JobSpec{Name: "live", Command: command})
		require.NoError(t, err)

		state, err := h.Wait(ctx, id)
		require.NoError(t, err)

		return state
	}

	assert.Equal(t, model.JobSucceeded, wait(`echo "pid $$ home ${HOME}" > "`+filepath.Join(root, "out")+`"; exit 0`))
	assert.Equal(t, model.JobFailed, wait("exit 3"))

	text, err := os.ReadFile(filepath.Join(root, "out"))
	require.NoError(t, err)
	assert.Regexp(t, `^pid [0-9]+ home /`, string(text), "the dollar signs reach the shell of the job")

	id, err := h.Submit(ctx, model.JobSpec{Name: "live", Command: "setsid sleep 113 & sleep 114"})
	require.NoError(t, err)
	require.NoError(t, h.Cancel(ctx, id))

	state, err := h.Wait(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, model.JobCanceled, state)
	assert.Error(t, exec.CommandContext(ctx, "pgrep", "-f", "sleep 113").Run(), "the setsid child died with the unit")
}
