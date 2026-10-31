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

// forkScript starts 40 sleeps of 2 seconds in the background. A start that the kernel refuses ends the shell with a non-zero code.
// The loop ends by itself, and the exit trap kills every sleep it started.
const forkScript = `trap 'kill $(jobs -p) 2>/dev/null' EXIT
i=0
while [ $i -lt 40 ]; do
  sleep 2 &
  i=$((i + 1))
done
echo started $i`

func TestExecRunner_AProcessCapStopsAStartPastIt(t *testing.T) { //nolint:paralleltest // the cap counts every process of the user, so a fork test runs alone
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 3}.Run(context.Background(), []string{"-c", forkScript})

	assert.NotZero(t, done.ExitCode, "a start past the cap fails the shell")
	assert.NotContains(t, done.Stdout, "started 40")
}

func TestExecRunner_NoProcessCapStartsEveryProcess(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second}.Run(context.Background(), []string{"-c", forkScript})

	assert.Zero(t, done.ExitCode, done.Stderr)
	assert.Contains(t, done.Stdout, "started 40")
}

func TestExecRunner_AProcessCapKeepsTheArgumentsAndTheExitCodeOfTheCommand(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), []string{"-c", `printf %s "$0 $1"; exit 4`, "a", "b c"})

	assert.Equal(t, 4, done.ExitCode)
	assert.Equal(t, "a b c", done.Stdout)
}

func TestExecRunner_AProcessCapOnAMissingCommandReadsAsNotFound(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "obee-no-such-binary", MaxNewProcesses: 10}.Run(context.Background(), nil)

	assert.Equal(t, 127, done.ExitCode)
	assert.Contains(t, done.Stderr, "command not found")
}

func TestExecRunner_ACapDoesNotReadTheRequestPath(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, Only: []string{"PATH=/nonexistent"}, MaxNewProcesses: 50}.Run(context.Background(), []string{"-c", "echo ok"})

	assert.Zero(t, done.ExitCode, done.Stderr)
	assert.Equal(t, "ok\n", done.Stdout)
	assert.Empty(t, done.Stderr, "no count tool is looked up in the PATH of the request")
}

func TestExecRunner_AFailedCountFailsTheStepAndTheCommandDoesNotRun(t *testing.T) { //nolint:paralleltest // see above
	marker := filepath.Join(t.TempDir(), "ran")
	count := func() (int, error) { return 0, errors.New("no table") }
	done := proc.WithCount(proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), MaxNewProcesses: 50}, count).Run(context.Background(), []string{"-c", "touch " + marker})

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.EqualError(t, done.SetupErr, "could not count the user's processes: no table")
	assert.NoFileExists(t, marker)
}

func TestExecRunner_APathThatCannotBeMadeAbsoluteFailsTheCallAndTheCommandDoesNotRun(t *testing.T) { //nolint:paralleltest // see above
	marker := filepath.Join(t.TempDir(), "ran")
	abs := func(string) (string, error) { return "", errors.New("no working folder") }
	done := proc.WithAbs(proc.ExecRunner{Binary: "./tool.sh", Dir: "rel", MaxNewProcesses: 50}, abs).Run(context.Background(), []string{marker})

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.EqualError(t, done.SetupErr, "could not resolve ./tool.sh in rel: no working folder")
	assert.NoFileExists(t, marker)
}

func TestExecRunner_ALimitThatCannotBeSetFailsTheStepAndTheCommandDoesNotRun(t *testing.T) { //nolint:paralleltest // see above
	hard := proc.ExecRunner{Binary: "sh", Dir: t.TempDir()}.Run(context.Background(), []string{"-c", "ulimit -H -u"})
	if limit, err := strconv.Atoi(strings.TrimSpace(hard.Stdout)); err != nil || limit > 100000 {
		t.Skip("the hard limit of processes is too high to test a limit that cannot be set")
	}

	marker := filepath.Join(t.TempDir(), "ran")
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 100000}.Run(context.Background(), []string{"-c", "touch " + marker})

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.Error(t, done.SetupErr)
	assert.True(t, strings.HasPrefix(done.SetupErr.Error(), "could not set max_new_processes = 100000: "), done.SetupErr.Error())
	assert.NoFileExists(t, marker)
}

func TestExecRunner_ACapOnAProgramPathRelativeToDirIsApplied(t *testing.T) { //nolint:paralleltest // see above
	hard := proc.ExecRunner{Binary: "sh", Dir: t.TempDir()}.Run(context.Background(), []string{"-c", "ulimit -H -u"})
	if limit, err := strconv.Atoi(strings.TrimSpace(hard.Stdout)); err != nil || limit > 100000 {
		t.Skip("the hard limit of processes is too high to test a limit that cannot be set")
	}

	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "probe.sh"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)) //nolint:gosec // the test script must be executable

	done := proc.ExecRunner{Binary: "./probe.sh", Dir: dir, Timeout: 30 * time.Second, MaxNewProcesses: 100000}.Run(context.Background(), nil)

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.Error(t, done.SetupErr)
	assert.True(t, strings.HasPrefix(done.SetupErr.Error(), "could not set max_new_processes = 100000: "), done.SetupErr.Error())
	assert.NoFileExists(t, marker)
}

func TestExecRunner_ACappedProgramPathThatIsMissingReadsAsNotFound(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "./absent.sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), nil)

	require.NoError(t, done.SetupErr)
	assert.Equal(t, 127, done.ExitCode)
	assert.NotEmpty(t, done.Stderr)
}

func TestExecRunner_AProgramThatExits125WithItsOwnCouldNotTextKeepsItsResult(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), []string{"-c", `echo out; echo "could not open the thing" >&2; exit 125`})

	require.NoError(t, done.SetupErr)
	assert.Equal(t, 125, done.ExitCode)
	assert.Equal(t, "out\n", done.Stdout)
	assert.Equal(t, "could not open the thing\n", done.Stderr)
}

func TestExecRunner_AProgramThatPrintsTheOldSentinelKeepsItsResult(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), []string{"-c", `echo obee-cap-not-set >&2; echo out; exit 125`})

	require.NoError(t, done.SetupErr)
	assert.Equal(t, 125, done.ExitCode)
	assert.Equal(t, "out\n", done.Stdout)
	assert.Equal(t, "obee-cap-not-set\n", done.Stderr)
}

func TestExecRunner_ACappedCallThatCannotStartReadsAsNotFound(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "sh", Dir: filepath.Join(t.TempDir(), "missing"), MaxNewProcesses: 1000}.Run(context.Background(), nil)

	require.NoError(t, done.SetupErr)
	assert.Equal(t, 127, done.ExitCode)
	assert.NotEmpty(t, done.Stderr)
}

func TestExecRunner_AProgramCannotWriteToTheSetupPipe(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), []string{"-c", `echo forged >&3 2>/dev/null; echo out`})

	require.NoError(t, done.SetupErr)
	assert.Equal(t, "out\n", done.Stdout)
}
