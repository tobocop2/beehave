package ssh_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/link"
	"github.com/tobocop2/beehest/internal/host/ssh"
	"github.com/tobocop2/beehest/internal/host/sshtest"
	"github.com/tobocop2/beehest/internal/model"
)

// Two engines are two ssh.Host values with their own fake machine and their own nonces, over one real folder: the work root of the shared build host.
// The fake machine records the decoded job command. These tests run that command with a real sh and a real flock, so only systemd is faked.

// requireFlock skips a test on a machine with no flock command, such as a Mac without util-linux.
func requireFlock(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock is not on PATH")
	}
}

type engine struct {
	t    *testing.T
	root string
	m    *sshtest.Machine
	h    *ssh.Host
	tag  string
}

func newEngine(t *testing.T, root, tag string) *engine {
	t.Helper()

	m := sshtest.New(hosttest.Working)
	h, err := ssh.New("gates", configRoot(root), m.SSH(),
		ssh.WithClock(newClock().clock()), ssh.WithBank(func(context.Context) error { return nil }),
		ssh.WithNonce(func() string { return tag }))
	require.NoError(t, err)

	return &engine{t: t, root: root, m: m, h: h, tag: tag}
}

// job is a running local process of one submitted job.
type job struct {
	e    *engine
	unit string
	cmd  *exec.Cmd
	// reaped is true once kill has stopped and reaped the process.
	reaped bool
}

// submit submits a job that writes started-<tag>, then waits for release-<tag>. An empty class sets no slot.
func (e *engine) submit(class string, count int) *job {
	e.t.Helper()

	command := "touch " + filepath.Join(e.root, "started-"+e.tag) + "; until test -e " + filepath.Join(e.root, "release-"+e.tag) + "; do sleep 0.05; done"
	spec := model.JobSpec{Name: "gate", Command: command, Slot: model.Slot{Class: class, Count: count}}

	id, err := e.h.Submit(e.t.Context(), spec)
	require.NoError(e.t, err)

	e.m.Mu.Lock()
	text := e.m.Units[string(id)].Command
	e.m.Mu.Unlock()

	cmd := exec.CommandContext(context.WithoutCancel(e.t.Context()), "sh", "-c", text) //nolint:gosec // the test runs the job command that the fake machine recorded, under a real shell
	cmd.Dir = e.root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(e.t, cmd.Start())

	j := &job{e: e, unit: string(id), cmd: cmd}
	e.t.Cleanup(j.kill)

	return j
}

func (j *job) started() bool { return exists(filepath.Join(j.e.root, "started-"+j.e.tag)) }

func (j *job) release() {
	j.e.t.Helper()

	require.NoError(j.e.t, os.WriteFile(filepath.Join(j.e.root, "release-"+j.e.tag), nil, 0o600))
}

// kill stops the job and every child of it, and reaps it once. A second call does nothing.
func (j *job) kill() {
	j.e.t.Helper()

	if j.reaped {
		return
	}

	j.reaped = true

	// A group whose leader ended on its own gives ESRCH, or EPERM on macOS while the leader waits to be reaped. Both mean that nothing is left to stop.
	if err := syscall.Kill(-j.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
		j.e.t.Errorf("kill the job group: %v", err)
	}

	var killed *exec.ExitError
	if err := j.cmd.Wait(); err != nil && !errors.As(err, &killed) {
		j.e.t.Errorf("wait for the killed job: %v", err)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}

	t.Fatalf("timed out: %s", what)
}

// still asserts that ok stays false for a while: the job is held back.
func still(t *testing.T, what string, ok func() bool) {
	t.Helper()

	for deadline := time.Now().Add(1500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ok() {
			t.Fatalf("%s", what)
		}
	}
}

func TestSlots_AJobWithNoSlotRunsBesideAnotherAndTakesNoLock(t *testing.T) {
	t.Parallel()

	requireFlock(t)

	root := t.TempDir()
	a, b := newEngine(t, root, "a"), newEngine(t, root, "b")

	ja, jb := a.submit("", 0), b.submit("", 0)

	eventually(t, "both jobs start together", func() bool { return ja.started() && jb.started() })
	assert.NotContains(t, a.m.Units[ja.unit].Command, "flock")
}

func TestSlots_TheSecondEngineWaitsUntilTheFirstJobEnds(t *testing.T) {
	t.Parallel()

	requireFlock(t)

	root := t.TempDir()
	a, b := newEngine(t, root, "a"), newEngine(t, root, "b")

	ja := a.submit("mutation", 1)
	eventually(t, "the first job starts", ja.started)

	jb := b.submit("mutation", 1)

	still(t, "the second job started while the first one holds the only slot", jb.started)
	ja.release()
	eventually(t, "the second job starts after the first ended", jb.started)
}

func TestSlots_TwoSlotsLetTwoEnginesRunAndHoldTheThirdJob(t *testing.T) {
	t.Parallel()

	requireFlock(t)

	root := t.TempDir()
	a, b, c := newEngine(t, root, "a"), newEngine(t, root, "b"), newEngine(t, root, "c")

	ja, jb := a.submit("bench", 2), b.submit("bench", 2)
	eventually(t, "two jobs start together", func() bool { return ja.started() && jb.started() })

	jc := c.submit("bench", 2)

	still(t, "a third job started with both slots held", jc.started)
	jb.release()
	eventually(t, "the third job takes the free slot", jc.started)
}

func TestSlots_AKilledJobFreesItsSlot(t *testing.T) {
	t.Parallel()

	requireFlock(t)

	root := t.TempDir()
	a, b := newEngine(t, root, "a"), newEngine(t, root, "b")

	ja := a.submit("mutation", 1)
	eventually(t, "the first job starts", ja.started)

	jb := b.submit("mutation", 1)

	still(t, "the second job started before the holder died", jb.started)
	ja.kill()
	eventually(t, "the second job starts after the holder was killed", jb.started)
}

func TestSlots_AJobOfAnotherClassNeverWaitsForAFullClass(t *testing.T) {
	t.Parallel()

	requireFlock(t)

	root := t.TempDir()
	a, b := newEngine(t, root, "a"), newEngine(t, root, "b")

	ja := a.submit("mutation", 1)
	eventually(t, "the first job starts", ja.started)

	jb := b.submit("bench", 1)

	eventually(t, "a job of another class starts at once", jb.started)
}

func TestSlots_AWaitingJobShowsAMarkerUntilItHoldsASlot(t *testing.T) {
	t.Parallel()

	requireFlock(t)

	root := t.TempDir()
	a, b := newEngine(t, root, "a"), newEngine(t, root, "b")

	ja := a.submit("mutation", 1)
	eventually(t, "the first job starts", ja.started)

	jb := b.submit("mutation", 1)
	marker := filepath.Join(root, ".beehest", jb.unit+".waiting")

	eventually(t, "the waiting job writes its marker", func() bool { return exists(marker) })
	assert.False(t, exists(filepath.Join(root, ".beehest", ja.unit+".waiting")), "the holder never shows a marker")
	ja.release()
	eventually(t, "the marker goes when the job holds a slot", func() bool { return jb.started() && !exists(marker) })
}

func TestSlots_CancelingAWaitingJobTakesNoSlotAndFreesNone(t *testing.T) {
	t.Parallel()

	requireFlock(t)

	root := t.TempDir()
	a, b, c := newEngine(t, root, "a"), newEngine(t, root, "b"), newEngine(t, root, "c")

	ja := a.submit("mutation", 1)
	eventually(t, "the first job starts", ja.started)

	jb := b.submit("mutation", 1)
	still(t, "the waiter started", jb.started)
	jb.kill()

	jc := c.submit("mutation", 1)

	still(t, "a third job started although the holder still runs", jc.started)
	assert.Contains(t, a.m.Units[ja.unit].Command, "flock", "the holder took its slot with a flock")
	ja.release()
	eventually(t, "the third job starts after the holder ended", jc.started)
}

func TestSubmit_RefusesASlotWithABadClassOrNoCount(t *testing.T) {
	t.Parallel()

	for name, slot := range map[string]model.Slot{
		"a class with a slash": {Class: "a/b", Count: 1},
		"a class with a space": {Class: "a b", Count: 1},
		"no count":             {Class: "mutation"},
		"a negative count":     {Class: "mutation", Count: -1},
		"a count with no name": {Count: 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			e := newEngine(t, t.TempDir(), "a")
			_, err := e.h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "true", Slot: slot})

			require.ErrorIs(t, err, model.ErrBadSlot)
			assert.Empty(t, e.m.Units, "a refused job starts no unit")
		})
	}
}

func TestUp_RefusesAMachineWithNoFlock(t *testing.T) {
	t.Parallel()

	e := newEngine(t, t.TempDir(), "a")
	e.m.NoFlock = true

	err := e.h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrNoFlock)
	require.ErrorContains(t, err, "Install util-linux (flock)")
}

// A pending job does not count toward the stale limit, which counts a running unit that writes no log. The unit reads running for far longer than
// the limit in both cases; the marker beside the log is the only difference.
func TestWait_AWaitingMarkerReadsPendingSoTheJobNeverGoesStale(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		marker bool
		want   model.JobState
	}{
		"a job with the marker waits for its slot and ends as it ends": {marker: true, want: model.JobSucceeded},
		"a job with no marker and no log growth goes stale":            {marker: false, want: model.JobStale},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m := sshtest.New(hosttest.Working)
			h, _, _ := newHost(t, m, config())

			id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x", Slot: model.Slot{Class: "mutation", Count: 1}})
			require.NoError(t, err)

			u := m.Units[string(id)]
			u.Live = 300
			u.LogSizes = []int{10}
			m.Files[sshtest.Root+"/work/.beehest/beehest-n1.waiting"] = tc.marker

			state, err := h.Wait(t.Context(), id)

			require.NoError(t, err)
			assert.Equal(t, tc.want, state)
		})
	}
}

func TestSubmit_WrapsTheCommandInTheSlotLoopOnlyForAJobWithASlot(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	limited, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "echo hi", Slot: model.Slot{Class: "mutation", Count: 3}})
	require.NoError(t, err)

	free, err := h.Submit(t.Context(), model.JobSpec{Name: "y", Command: "echo hi"})
	require.NoError(t, err)

	text := m.Units[string(limited)].Command
	assert.Contains(t, text, "flock -n 9")
	assert.Contains(t, text, "-le 3")
	assert.Contains(t, text, sshtest.Root+"/work/.beehest/slots/mutation")
	assert.True(t, strings.HasSuffix(text, "echo hi"), "the command runs last, as a child of the script")
	assert.Equal(t, "echo hi", m.Units[string(free)].Command)
}
