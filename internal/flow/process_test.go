package flow

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The tests below run the flow in separate operating system processes. The test binary starts itself as the helper:
// TestHelperProcess does nothing unless the parent passed a mode after "--", then it does the job that the arguments name.
// A helper writes its ready file, then waits for the go file in the state folder, so all the helpers open the folder at
// the same moment.
const goFile = "helper-go"

// helperDeadline bounds every helper process; a helper that hangs is killed and the run reports its output.
const helperDeadline = time.Minute

func TestHelperProcess(t *testing.T) {
	t.Parallel()

	args := flag.Args()
	if len(args) == 0 {
		t.Skip("helper for the process tests")
	}

	require.Len(t, args, 3, "helper arguments: mode, state folder, index")

	mode, stateDir, index := args[0], args[1], args[2]
	require.Contains(t, []string{"client", "engine", "hang", "stall"}, mode, "unknown helper mode")

	opts := Options{StateDir: stateDir, Version: testVersion, MaxConcurrent: 2, Env: map[string]string{}}

	if mode == "stall" {
		// A stalled helper never writes its ready file; the parent kills it at the deadline.
		println("the helper stalls before its ready file")
		time.Sleep(time.Hour)
	}

	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "helper-ready-"+index), nil, 0o600))

	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(stateDir, goFile))

		return err == nil
	}, time.Minute, time.Millisecond)

	switch mode {
	case "hang":
		// A hung helper never exits; the parent kills it at the deadline and reports this line.
		println("the helper hangs now")
		time.Sleep(time.Hour)
	case "client":
		client, err := OpenClient(t.Context(), opts, ReleaseDeps{})
		require.NoError(t, err)
		require.NoError(t, client.Close())
	case "engine":
		engine, err := NewEngine(t.Context(), opts, Deps{})
		require.NoError(t, err)
		require.NoError(t, engine.Shutdown())
	}
}

type helperResult struct {
	out string
	err error
}

type helperRun struct {
	modes   []string
	results []helperResult
}

func (r *helperRun) failure(i int, why string) error {
	return fmt.Errorf("%s helper %s: %w\n%s", r.modes[i], why, r.results[i].err, r.results[i].out)
}

// notReady returns the error for a deadline that passed before every helper wrote its ready file. It holds the output of each helper that wrote none.
func (r *helperRun) notReady(stateDir string) error {
	err := errors.New("the helpers were not ready before the deadline")

	for i, mode := range r.modes {
		if _, statErr := os.Stat(filepath.Join(stateDir, "helper-ready-"+strconv.Itoa(i))); statErr != nil {
			err = fmt.Errorf("%w\n%s helper %d wrote no ready file: %w\n%s", err, mode, i, r.results[i].err, r.results[i].out)
		}
	}

	return err
}

func (r *helperRun) firstFailure() error {
	for i := range r.modes {
		if r.results[i].err != nil {
			return r.failure(i, "failed")
		}
	}

	return nil
}

// runHelpers starts one helper per mode on one state folder, releases them together, and returns the first failure with
// the output of the helper that failed. A helper that exits before the go file fails the run at once.
func runHelpers(stateDir string, modes []string, deadline time.Duration) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	run := &helperRun{modes: modes, results: make([]helperResult, len(modes))}
	exited := make(chan int, len(modes))

	var group sync.WaitGroup

	for i, mode := range modes {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$", "--", mode, stateDir, strconv.Itoa(i)) //nolint:gosec // the test starts its own binary
		group.Go(func() {
			out, err := cmd.CombinedOutput()
			run.results[i] = helperResult{string(out), err}
			exited <- i
		})
	}

	// The helpers wait for the go file: write it once every helper wrote its ready file.
	for ready := false; !ready; {
		select {
		case i := <-exited:
			cancel()
			group.Wait()

			return run.failure(i, "exited before the go file")
		case <-ctx.Done():
			group.Wait()

			return run.notReady(stateDir)
		case <-time.After(10 * time.Millisecond):
			files, err := filepath.Glob(filepath.Join(stateDir, "helper-ready-*"))
			ready = err == nil && len(files) == len(modes)
		}
	}

	if err := os.WriteFile(filepath.Join(stateDir, goFile), nil, 0o600); err != nil {
		return err
	}

	group.Wait()

	return run.firstFailure()
}

func TestRunHelpers_AHelperThatFailsBeforeTheGoFileFailsAtOnceWithItsOutput(t *testing.T) {
	t.Parallel()

	start := time.Now()
	err := runHelpers(t.TempDir(), []string{"bogus"}, helperDeadline)

	require.ErrorContains(t, err, "unknown helper mode")
	require.Less(t, time.Since(start), helperDeadline/2)
}

func TestRunHelpers_AHelperThatHangsIsKilledAtTheDeadlineAndItsOutputIsReported(t *testing.T) {
	t.Parallel()

	start := time.Now()
	err := runHelpers(t.TempDir(), []string{"hang"}, 2*time.Second)

	require.ErrorContains(t, err, "hang helper failed")
	require.ErrorContains(t, err, "the helper hangs now")
	require.Less(t, time.Since(start), 4*time.Second)
}

func TestRunHelpers_HelpersThatAreNotReadyAtTheDeadlineReportTheOutputOfThoseThatWroteNoReadyFile(t *testing.T) {
	t.Parallel()

	start := time.Now()
	err := runHelpers(t.TempDir(), []string{"hang", "stall"}, 2*time.Second)

	require.ErrorContains(t, err, "not ready before the deadline")
	require.ErrorContains(t, err, "the helper stalls before its ready file")
	require.NotContains(t, err.Error(), "the helper hangs now")
	require.Less(t, time.Since(start), 4*time.Second)
}

// This test is the guard for safe concurrent first opens: DBOS v1.8.0, not obee, makes them safe, so a failure here
// means the DBOS version or its SQLite setup regressed.
func TestOpen_ProcessesThatOpenOneNewStateFolderAtOnceAllSucceed(t *testing.T) {
	t.Parallel()

	const (
		folders = 3
		clients = 7
	)

	modes := []string{"engine"}
	for range clients {
		modes = append(modes, "client")
	}

	for range folders {
		require.NoError(t, runHelpers(newHarness(t).opts.StateDir, modes, helperDeadline))
	}
}
