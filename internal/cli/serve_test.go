//go:build unix

package cli_test

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/flow"
)

// The serve tests send a real signal to the test process, and every running serve command receives it. So they do not run in parallel.

const serveWait = 10 * time.Second

type serveResult struct {
	code           int
	stdout, stderr string
}

// startServe runs beehest serve in the background and returns the channel of its result.
func startServe(t *testing.T, rig *flowRig) <-chan serveResult {
	t.Helper()

	done := make(chan serveResult, 1)

	go func() {
		code, stdout, stderr := rig.execute(t.Context(), "serve")
		done <- serveResult{code, stdout, stderr}
	}()

	return done
}

func signalSelf(t *testing.T, sig syscall.Signal) {
	t.Helper()

	require.NoError(t, syscall.Kill(os.Getpid(), sig))
}

func waitFor(t *testing.T, channel <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-channel:
	case <-time.After(serveWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestServe_LaunchesBlocksAndShutsDownOnceOnSigterm(t *testing.T) { //nolint:paralleltest // a signal reaches every running serve command
	rig := newRig(t)
	rig.runner.launched = make(chan struct{})

	done := startServe(t, rig)
	waitFor(t, rig.runner.launched, "Launch")

	select {
	case got := <-done:
		t.Fatalf("serve returned before a signal: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}

	assert.Zero(t, rig.runner.count(), "the engine runs until a signal")
	signalSelf(t, syscall.SIGTERM)

	select {
	case got := <-done:
		assert.Equal(t, 0, got.code, got.stderr)
	case <-time.After(serveWait):
		t.Fatal("serve did not end after SIGTERM")
	}

	assert.Equal(t, 1, rig.runner.count(), "Shutdown runs once")
	assert.Equal(t, flow.ModeServe, rig.options.Mode, "the engine records that it serves")
	assert.Empty(t, rig.runner.items, "serve takes no item")
}

func TestServe_SigintShutsDownOnceAndExitsZero(t *testing.T) { //nolint:paralleltest // a signal reaches every running serve command
	rig := newRig(t)
	rig.runner.launched = make(chan struct{})

	done := startServe(t, rig)
	waitFor(t, rig.runner.launched, "Launch")
	signalSelf(t, syscall.SIGINT)

	got := <-done
	assert.Equal(t, 0, got.code, got.stderr)
	assert.Equal(t, 1, rig.runner.count())
}

func TestServe_ASecondSignalDuringASlowShutdownExits130(t *testing.T) { //nolint:paralleltest // a signal reaches every running serve command
	rig := newRig(t)
	rig.runner.launched = make(chan struct{})
	rig.runner.started = make(chan struct{})
	rig.runner.slow = make(chan struct{})

	t.Cleanup(func() { close(rig.runner.slow) })

	done := startServe(t, rig)
	waitFor(t, rig.runner.launched, "Launch")
	signalSelf(t, syscall.SIGTERM)
	waitFor(t, rig.runner.started, "Shutdown")

	select {
	case got := <-done:
		t.Fatalf("serve ended while Shutdown was still running: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}

	signalSelf(t, syscall.SIGTERM)

	select {
	case got := <-done:
		assert.Equal(t, 130, got.code, got.stderr)
	case <-time.After(serveWait):
		t.Fatal("a second signal did not end serve")
	}

	assert.Equal(t, 1, rig.runner.count(), "the second signal does not call Shutdown again")
}

func TestRun_ASecondInterruptDuringASlowShutdownExits130AndSaysTheRunStaysOpen(t *testing.T) { //nolint:paralleltest // a signal reaches every running command
	rig := newRig(t)
	rig.runner.block = make(chan struct{})
	rig.runner.started = make(chan struct{})
	rig.runner.slow = make(chan struct{})

	t.Cleanup(func() { close(rig.runner.slow) })

	// main gives the command the context that the first signal cancels.
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt)
	defer stop()

	done := make(chan serveResult, 1)

	go func() {
		code, stdout, stderr := rig.execute(ctx, "run", "12")
		done <- serveResult{code, stdout, stderr}
	}()

	signalSelf(t, syscall.SIGINT)
	waitFor(t, rig.runner.started, "Shutdown")

	select {
	case got := <-done:
		t.Fatalf("run ended while Shutdown was still waiting for the store: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}

	signalSelf(t, syscall.SIGINT)

	select {
	case got := <-done:
		assert.Equal(t, 130, got.code, got.stderr)
		assert.Contains(t, got.stderr, "the run stays open")
	case <-time.After(serveWait):
		t.Fatal("a second interrupt did not end beehest run")
	}

	assert.Equal(t, 1, rig.runner.count(), "the second interrupt does not call Shutdown again")
}

// runThenSlowStop runs beehest run on a fake engine whose run ends at once and whose Shutdown is held, then sends one interrupt.
func runThenSlowStop(t *testing.T, rig *flowRig) serveResult {
	t.Helper()

	rig.runner.started = make(chan struct{})
	rig.runner.slow = make(chan struct{})

	t.Cleanup(func() { close(rig.runner.slow) })

	done := make(chan serveResult, 1)

	go func() {
		code, stdout, stderr := rig.execute(t.Context(), "run", "12")
		done <- serveResult{code, stdout, stderr}
	}()

	waitFor(t, rig.runner.started, "Shutdown")
	signalSelf(t, syscall.SIGINT)

	select {
	case got := <-done:
		return got
	case <-time.After(serveWait):
		t.Fatal("an interrupt did not end beehest run")
	}

	return serveResult{}
}

func TestRun_AnInterruptDuringASlowStopAfterAStoppedResultKeepsExitOneAndDoesNotSayTheRunIsOpen(t *testing.T) { //nolint:paralleltest // a signal reaches every running command
	rig := newRig(t)
	rig.runner.stopped = &flow.Stopped{Step: "check", RC: 2, Output: "boom"}

	got := runThenSlowStop(t, rig)

	assert.Equal(t, 1, got.code, got.stderr)
	assert.Contains(t, got.stderr, "stopped at once")
	assert.NotContains(t, got.stderr, "stays open")
	assert.Equal(t, 1, rig.runner.count())
}

func TestRun_AnInterruptDuringASlowStopAfterAFinishedRunKeepsTheRunError(t *testing.T) { //nolint:paralleltest // a signal reaches every running command
	rig := newRig(t)
	rig.runner.runErr = errFake

	got := runThenSlowStop(t, rig)

	assert.Equal(t, 1, got.code, got.stderr)
	assert.Contains(t, got.stderr, errFake.Error())
	assert.Contains(t, got.stderr, "stopped at once")
	assert.NotContains(t, got.stderr, "stays open")
}

func TestRun_AnInterruptDuringASlowStopAfterADoneRunExitsZeroAndDoesNotSayTheRunIsOpen(t *testing.T) { //nolint:paralleltest // a signal reaches every running command
	rig := newRig(t)

	got := runThenSlowStop(t, rig)

	assert.Equal(t, 0, got.code, got.stderr)
	assert.Contains(t, got.stderr, "the run had ended before the stop")
	assert.NotContains(t, got.stderr, "stays open")
	assert.Equal(t, 1, rig.runner.count())
}

func TestServe_AFolderThatAnotherEngineOwnsIsRefusedWithTheLockText(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.engineOf = func() error { return flow.ErrStateBusy }

	code, stdout, stderr := rig.execute(t.Context(), "serve")

	assert.Equal(t, 1, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "another beehest engine owns this state folder")
	assert.NotContains(t, stderr, "another beehest run")
}

func TestServe_AnEngineThatCannotOpenFailsWithExitOne(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.engineOf = func() error { return errFake }

	code, _, stderr := rig.execute(t.Context(), "serve")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, errFake.Error())
}

func TestServe_ALaunchThatFailsShutsTheEngineDownAndExitsOne(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.runner.launchErr = errFake

	code, _, stderr := rig.execute(t.Context(), "serve")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, errFake.Error())
	assert.Equal(t, 1, rig.runner.count())
}

func TestServe_AShutdownThatFailsExitsOne(t *testing.T) { //nolint:paralleltest // a signal reaches every running serve command
	rig := newRig(t)
	rig.runner.launched = make(chan struct{})
	rig.runner.shutdownErr = errFake

	done := startServe(t, rig)
	waitFor(t, rig.runner.launched, "Launch")
	signalSelf(t, syscall.SIGTERM)

	got := <-done
	assert.Equal(t, 1, got.code)
	assert.Contains(t, got.stderr, errFake.Error())
}

func TestServe_TakesNoItem(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "serve", "12")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "unknown command")
	assert.Zero(t, rig.engines)
}

func TestServe_AConfigWithProblemsExitsTwoAndOpensNoEngine(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	writeConfig(t, rig.dir, "runners.star", "agents(max_concurrent = 0)\n"+validRunners)

	code, stdout, stderr := rig.execute(t.Context(), "serve")

	assert.Equal(t, 2, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "agents.max_concurrent must be 1 or more")
	assert.Zero(t, rig.engines, "a config with problems opens no engine")
}

func TestServe_TheHelpSaysTheEngineAppliesTheReviewPollAndStartsEachSentenceWithASpace(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, stdout, _ := rig.execute(t.Context(), "serve", "--help")

	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "applies the review poll and waits. A beehest run on the same state folder")
	assert.NotContains(t, stdout, "project schedules")
}
