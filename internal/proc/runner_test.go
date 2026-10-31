package proc_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/exectest"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

const fakeScript = `#!/bin/sh
case "$1" in
out) echo "out:$2"; echo "err" >&2 ;;
fail) exit 7 ;;
half) printf partial >&2; exec sleep 5 ;;
line) printf 'partial\n' >&2; exec sleep 5 ;;
crlf) printf 'a\r\nb' ; printf 'e\r\n' >&2; exit 3 ;;
grand) sleep 120 & echo $! > "$2"; wait ;;
slow) exec sleep 5 ;;
dir) pwd ;;
esac
`

// fakeOnPath puts a fake binary first on PATH and returns the folder that holds it.
func fakeOnPath(t *testing.T, name, script string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(dir, name), script))
	t.Setenv("PATH", dir+":/bin:/usr/bin")
	runOnce(t, filepath.Join(dir, name))

	return dir
}

// runOnce starts a new script once, so the first-run scan of macOS happens before any timer starts.
// The script gets no arguments, so it ends at once; a script that cannot start is not an error here.
func runOnce(t *testing.T, path string) {
	t.Helper()

	if err := exec.CommandContext(t.Context(), path).Run(); err != nil {
		t.Logf("warm-up run of %s: %v", path, err)
	}
}

func TestExecRunner_ReturnsOutputAndExitCode(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)
	run := proc.ExecRunner{Binary: "bd"}

	done := run.Run(context.Background(), []string{"out", "x"})
	assert.Equal(t, 0, done.ExitCode)
	assert.Equal(t, "out:x\n", done.Stdout)
	assert.Equal(t, "err\n", done.Stderr)

	assert.Equal(t, 7, run.Run(context.Background(), []string{"fail"}).ExitCode)
}

func TestExecRunner_PassesTheEnvironmentItIsGivenToTheCommand(t *testing.T) {
	t.Parallel()

	done := proc.ExecRunner{Binary: "sh", Env: []string{"OBEE_PROC_TEST=seen"}}.Run(context.Background(), []string{"-c", `printf %s "$OBEE_PROC_TEST"`})

	assert.Equal(t, "seen", done.Stdout)
}

func TestExecRunner_RunsInTheFolderItIsGiven(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	done := proc.ExecRunner{Binary: "bd", Dir: dir}.Run(context.Background(), []string{"dir"})
	assert.Equal(t, dir+"\n", done.Stdout)
}

func TestExecRunner_ATimeoutReadsAsExitCode124(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)

	done := proc.ExecRunner{Binary: "bd", Timeout: 100 * time.Millisecond}.Run(context.Background(), []string{"slow"})
	assert.Equal(t, 124, done.ExitCode)
	assert.Equal(t, "bd: timed out", done.Stderr)
}

func TestExecRunner_AMissingBinaryReadsAsExitCode127(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "other", fakeScript)

	done := proc.ExecRunner{Binary: "bd"}.Run(context.Background(), []string{"out"})
	assert.Equal(t, 127, done.ExitCode)
	assert.Equal(t, "bd: command not found", done.Stderr)
}

func TestExecRunner_ABinaryThatCannotStartReadsAsExitCode127(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", "#!/nonexistent/interpreter\n")

	done := proc.ExecRunner{Binary: "bd"}.Run(context.Background(), []string{"out"})
	assert.Equal(t, 127, done.ExitCode)
	assert.Contains(t, done.Stderr, "bd: ")
}

func TestNewExecRunner_StopsACallAfterTheDefaultTimeout(t *testing.T) {
	t.Parallel()

	run := proc.NewExecRunner("bd", "/some/dir")
	assert.Equal(t, proc.ExecRunner{Binary: "bd", Dir: "/some/dir", Timeout: 120 * time.Second}, run)
	assert.Equal(t, 120*time.Second, proc.DefaultTimeout)
}

func TestExecRunner_ACancelBeforeTheCallReadsAsExitCode130(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "other", fakeScript)

	stopped, cancel := context.WithCancel(context.Background())
	cancel()

	done := proc.ExecRunner{Binary: "bd"}.Run(stopped, []string{"out"})
	assert.Equal(t, 130, done.ExitCode)
	assert.Equal(t, "bd: cancelled", done.Stderr) //nolint:misspell // the note text is the contract

	expired, stop := context.WithTimeout(context.Background(), time.Nanosecond)
	defer stop()
	<-expired.Done()

	assert.Equal(t, 124, proc.ExecRunner{Binary: "bd"}.Run(expired, []string{"out"}).ExitCode)
}

func TestExecRunner_ACancelDuringTheCallReadsAsExitCode130(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)

	stopped, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)

	done := proc.ExecRunner{Binary: "bd"}.Run(stopped, []string{"slow"})
	assert.Equal(t, 130, done.ExitCode)
	assert.Equal(t, "bd: cancelled", done.Stderr) //nolint:misspell // the note text is the contract
}

func TestExecRunner_ATimeoutNoteStartsOnItsOwnLine(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)

	done := proc.ExecRunner{Binary: "bd", Timeout: time.Second}.Run(context.Background(), []string{"half"})
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

	done := proc.ExecRunner{Binary: "bd", Timeout: time.Minute}.Run(running, []string{"grand", pidFile})
	assert.Equal(t, 130, done.ExitCode)

	text, err := os.ReadFile(pidFile) //nolint:gosec // a path the test made
	require.NoError(t, err)

	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 30*time.Second, 50*time.Millisecond,
		"the sleep that the script started is still alive")
}

func TestExecRunner_ATimeoutNoteAfterAFinishedLineAddsNoBlankLine(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)

	done := proc.ExecRunner{Binary: "bd", Timeout: time.Second}.Run(context.Background(), []string{"line"})
	assert.Equal(t, 124, done.ExitCode)
	assert.Equal(t, "partial\nbd: timed out", done.Stderr)
}

func TestExecRunner_ATimeoutKillsTheGrandchildToo(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "bd", fakeScript)

	pidFile := filepath.Join(t.TempDir(), "pid")

	// The timeout must outlast the start of the script, which writes the pid file. A loaded machine starts it late.
	done := proc.ExecRunner{Binary: "bd", Timeout: 5 * time.Second}.Run(context.Background(), []string{"grand", pidFile})
	assert.Equal(t, 124, done.ExitCode)
	require.FileExists(t, pidFile, "the script did not start before the timeout")

	text, err := os.ReadFile(pidFile) //nolint:gosec // a path the test made
	require.NoError(t, err)

	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 30*time.Second, 50*time.Millisecond,
		"the sleep that the script started is still alive")
}

func TestExecRunner_ReturnsBothStreamsWithoutLineEndingTranslation(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeOnPath(t, "gh", fakeScript)

	done := proc.ExecRunner{Binary: "gh"}.Run(context.Background(), []string{"crlf"})
	assert.Equal(t, model.CommandResult{ExitCode: 3, Stdout: "a\r\nb", Stderr: "e\r\n"}, done)
}

func TestLookPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(dir, "tool"), "#!/bin/sh\n"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain"), []byte("text"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "folder"), 0o700))

	working, err := os.Getwd()
	require.NoError(t, err)

	relative, err := filepath.Rel(working, dir)
	require.NoError(t, err)

	tests := []struct {
		name, binary, path string
		found              bool
	}{
		{"finds_an_executable", "tool", "/nonexistent::" + dir, true},
		{"skips_a_file_without_execute_bits", "plain", dir, false},
		{"skips_a_folder", "folder", dir, false},
		{"empty_path_reads_the_process_path_that_has_no_tool", "tool", "", false},
		{"skips_a_relative_folder", "tool", relative, false},
		{"skips_the_current_folder", "tool", ".", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := proc.LookPath(tt.binary, tt.path)
			if tt.found {
				require.NoError(t, err)
				assert.Equal(t, filepath.Join(dir, tt.binary), got)

				return
			}

			require.ErrorIs(t, err, exec.ErrNotFound)
		})
	}
}

func TestLookPath_AnEmptyPathReadsTheProcessPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(dir, "tool"), "#!/bin/sh\n"))
	t.Setenv("PATH", dir)

	got, err := proc.LookPath("tool", "")

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "tool"), got)
}

func TestStartTime_ReadsALiveProcessAndRefusesAMissingOne(t *testing.T) {
	t.Parallel()

	started, err := proc.StartTime(t.Context(), os.Getpid())
	require.NoError(t, err)
	assert.NotEmpty(t, started)

	_, err = proc.StartTime(t.Context(), 1<<22)
	require.ErrorIs(t, err, proc.ErrNoProcess)
}

func TestKillGroupID_KillsTheGroupOfAStartedCommand(t *testing.T) {
	t.Parallel()

	cmd := proc.GroupCommand(t.Context(), "sleep", []string{"60"}, t.TempDir())
	require.NoError(t, cmd.Start())

	require.NoError(t, proc.KillGroupID(cmd.Process.Pid))
	require.Error(t, cmd.Wait())
}
