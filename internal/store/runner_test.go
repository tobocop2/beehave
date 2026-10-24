package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/store"
)

const fakeScript = `#!/bin/sh
case "$1" in
out) echo "out:$2"; echo "err" >&2 ;;
fail) exit 7 ;;
half) printf partial >&2; exec sleep 5 ;;
grand) sleep 30 & echo $! > "$2"; wait ;;
slow) exec sleep 5 ;;
dir) pwd ;;
esac
`

// fakeOnPath puts a fake binary first on PATH and returns the folder that holds it.
func fakeOnPath(t *testing.T, name, script string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700)) //nolint:gosec // a test script must be executable
	t.Setenv("PATH", dir+":/bin:/usr/bin")

	return dir
}

func TestExecRunner_ReturnsOutputAndExitCode(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)
	run := store.ExecRunner{Binary: "bd"}

	done := run.Run(context.Background(), []string{"out", "x"})
	assert.Equal(t, 0, done.ExitCode)
	assert.Equal(t, "out:x\n", done.Stdout)
	assert.Equal(t, "err\n", done.Stderr)

	assert.Equal(t, 7, run.Run(context.Background(), []string{"fail"}).ExitCode)
}

func TestExecRunner_RunsInTheFolderItIsGiven(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	done := store.ExecRunner{Binary: "bd", Dir: dir}.Run(context.Background(), []string{"dir"})
	assert.Equal(t, dir+"\n", done.Stdout)
}

func TestExecRunner_ATimeoutReadsAsExitCode124(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)

	done := store.ExecRunner{Binary: "bd", Timeout: 100 * time.Millisecond}.Run(context.Background(), []string{"slow"})
	assert.Equal(t, 124, done.ExitCode)
	assert.Equal(t, "bd: timed out", done.Stderr)
}

func TestExecRunner_AMissingBinaryReadsAsExitCode127(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "other", fakeScript)

	done := store.ExecRunner{Binary: "bd"}.Run(context.Background(), []string{"out"})
	assert.Equal(t, 127, done.ExitCode)
	assert.Equal(t, "bd: command not found", done.Stderr)
}

func TestExecRunner_ABinaryThatCannotStartReadsAsExitCode127(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", "#!/nonexistent/interpreter\n")

	done := store.ExecRunner{Binary: "bd"}.Run(context.Background(), []string{"out"})
	assert.Equal(t, 127, done.ExitCode)
	assert.Contains(t, done.Stderr, "bd: ")
}

func TestNewExecRunner_StopsACallAfterTheDefaultTimeout(t *testing.T) {
	t.Parallel()

	run := store.NewExecRunner("bd", "/some/dir")
	assert.Equal(t, store.ExecRunner{Binary: "bd", Dir: "/some/dir", Timeout: 120 * time.Second}, run)
	assert.Equal(t, 120*time.Second, store.DefaultTimeout)
}

func TestExecRunner_ACancelBeforeTheCallReadsAsExitCode130(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "other", fakeScript)

	stopped, cancel := context.WithCancel(context.Background())
	cancel()

	done := store.ExecRunner{Binary: "bd"}.Run(stopped, []string{"out"})
	assert.Equal(t, 130, done.ExitCode)
	assert.Equal(t, "bd: cancelled", done.Stderr) //nolint:misspell // the note text is the contract

	expired, stop := context.WithTimeout(context.Background(), time.Nanosecond)
	defer stop()
	<-expired.Done()

	assert.Equal(t, 124, store.ExecRunner{Binary: "bd"}.Run(expired, []string{"out"}).ExitCode)
}

func TestExecRunner_ACancelDuringTheCallReadsAsExitCode130(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)

	stopped, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)

	done := store.ExecRunner{Binary: "bd"}.Run(stopped, []string{"slow"})
	assert.Equal(t, 130, done.ExitCode)
	assert.Equal(t, "bd: cancelled", done.Stderr) //nolint:misspell // the note text is the contract
}

func TestExecRunner_ATimeoutNoteStartsOnItsOwnLine(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)

	done := store.ExecRunner{Binary: "bd", Timeout: time.Second}.Run(context.Background(), []string{"half"})
	assert.Equal(t, 124, done.ExitCode)
	assert.Equal(t, "partial\nbd: timed out", done.Stderr)
}

func TestExecRunner_ACancelKillsTheGrandchildToo(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)

	pidFile := filepath.Join(t.TempDir(), "pid")
	running, cancel := context.WithCancel(context.Background())

	go func() {
		for {
			if text, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(text), "\n") { //nolint:gosec // a path the test made
				cancel()

				return
			}

			time.Sleep(20 * time.Millisecond)
		}
	}()

	done := store.ExecRunner{Binary: "bd", Timeout: time.Minute}.Run(running, []string{"grand", pidFile})
	assert.Equal(t, 130, done.ExitCode)

	text, err := os.ReadFile(pidFile) //nolint:gosec // a path the test made
	require.NoError(t, err)

	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 3*time.Second, 50*time.Millisecond,
		"the sleep that the script started is still alive")
}
