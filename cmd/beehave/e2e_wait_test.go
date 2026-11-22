package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
)

// e2eWaiting is a fact that a script waits for. runs is the folder where the run and serve processes of the script record their pids.
// ended, when set, closes when the run that the wait is inside of ends. bound is the guard against a real hang. state, when set, says where the run stands.
type e2eWaiting struct {
	what  string
	found func() bool
	runs  string
	ended <-chan struct{}
	bound time.Duration
	state func() string
}

// waitOn is the one wait of the e2e scripts and of the fake forge. It returns when the fact is recorded.
// When every run or serve process of the script is gone, and one of them began after the wait began, or when ended closes, and the fact is still absent, it fails at once and gives the state of the run.
func waitOn(fail func(format string, args ...any), w e2eWaiting) {
	before, _ := runRecords(w.runs)

	ticker := time.NewTicker(e2ePoll)
	defer ticker.Stop()

	guard := time.NewTimer(w.bound)
	defer guard.Stop()

	for {
		if w.found() {
			return
		}

		// The fact is read again after the end is seen, so a fact written by the last act of the run is not missed.
		if runsEndedSince(w.runs, before) {
			if w.found() {
				return
			}

			fail(waitEndedText+"%s is not there%s", w.what, w.stateText())

			return
		}

		select {
		case <-ticker.C:
		case <-w.ended:
			if w.found() {
				return
			}

			fail("the run ended and %s is not there%s", w.what, w.stateText())

			return
		case <-guard.C:
			fail("waited %s for %s%s", w.bound, w.what, w.stateText())

			return
		}
	}
}

// waitEndedText starts the failure of a wait that sees every run of the script end.
const waitEndedText = "every run of the script ended and "

// e2eRunWait runs the wait of a script command. With "!" before the command, the script expects the wait to fail on the end of the run, with the status in the text; a wait that runs to its guard fails the script.
func e2eRunWait(ts *testscript.TestScript, neg bool, w e2eWaiting) {
	if !neg {
		waitOn(ts.Fatalf, w)

		return
	}

	var text string

	waitOn(func(format string, args ...any) { text = fmt.Sprintf(format, args...) }, w)

	if !strings.HasPrefix(text, waitEndedText) || !strings.Contains(text, "; beehave status: ") {
		ts.Fatalf("the wait for %s did not fail on the end of the run: %q", w.what, text)
	}

	ts.Logf("%s", text)
}

func (w e2eWaiting) stateText() string {
	if w.state == nil {
		return ""
	}

	return "; " + w.state()
}

// e2eWaitingFor is the wait of a script for a fact, with the guard of e2eWait.
func e2eWaitingFor(ts *testscript.TestScript, what string, found func() bool) e2eWaiting {
	return e2eWaiting{what: what, found: found, runs: e2eRunsFolder(ts.Getenv("TMPDIR")), bound: e2eWait, state: func() string { return e2eStatusText(ts) }}
}

// e2eRunsFolder is the folder, below the temporary folder of a script, where a run or serve process records its pid.
func e2eRunsFolder(tmp string) string {
	return filepath.Join(tmp, "beehave-e2e-runs")
}

// e2eRecordRun records the pid of a run or serve process of a script, and ends the process when it cannot.
func e2eRecordRun(args []string) {
	if err := recordRun(os.TempDir(), args); err != nil {
		fmt.Fprintln(os.Stderr, "harness fault:", err)
		os.Exit(1)
	}
}

func recordRun(tmp string, args []string) error {
	if len(args) == 0 || (args[0] != "run" && args[0] != "serve") {
		return nil
	}

	folder := e2eRunsFolder(tmp)
	if err := os.MkdirAll(folder, e2eFolderMode); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(folder, strconv.Itoa(os.Getpid())), nil, e2eFileMode)
}

// runRecords reads the records of the folder. It returns the names of the records whose process is gone, and whether a process of a record is left.
// A record that is no pid counts as a process that is left.
func runRecords(folder string) (gone map[string]bool, live bool) {
	gone = map[string]bool{}

	entries, err := os.ReadDir(folder)
	if err != nil {
		return gone, false
	}

	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			live = true

			continue
		}

		gone[entry.Name()] = true
	}

	return gone, live
}

// runsEndedSince reports whether a process recorded after the records of before is gone and no process is left.
// A process that ended before the wait began is not the run that the wait is for, and a process that did not record yet is not seen.
func runsEndedSince(folder string, before map[string]bool) bool {
	gone, live := runRecords(folder)
	if live {
		return false
	}

	for name := range gone {
		if !before[name] {
			return true
		}
	}

	return false
}

// e2eStatus runs beehave status in the environment of the script and returns its output.
func e2eStatus(ts *testscript.TestScript) (string, error) {
	var binary string

	for _, folder := range filepath.SplitList(ts.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(folder, "beehave")); err == nil {
			binary = filepath.Join(folder, "beehave")

			break
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), e2eWait)
	defer cancel()

	command := exec.CommandContext(ctx, binary, "status") //nolint:gosec // a test runs the beehave of its own PATH
	command.Env = []string{
		"BEEHAVE_CONFIG=" + ts.Getenv("BEEHAVE_CONFIG"), "BEEHAVE_STATE_DIR=" + ts.Getenv("BEEHAVE_STATE_DIR"), e2eDirVariable + "=" + ts.Getenv(e2eDirVariable),
		"PATH=" + ts.Getenv("PATH"), "HOME=" + ts.Getenv("HOME"),
	}
	out, err := command.Output()

	return string(out), err
}

// e2eStatusText is the status of the run for a failure text.
func e2eStatusText(ts *testscript.TestScript) string {
	out, err := e2eStatus(ts)
	if err != nil {
		return "beehave status failed: " + err.Error()
	}

	return "beehave status: " + out
}
