package slurm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/slurmtest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// spy records the ssh calls and can answer one command with a failure that is not a dropped link.
type spy struct {
	inner proc.Runner

	mu    sync.Mutex
	calls []string
	fail  string // the first word of a command that exits 1
}

func (s *spy) Run(ctx context.Context, args []string) model.CommandResult {
	s.mu.Lock()
	s.calls = append(s.calls, strings.Join(args, " "))
	fail := s.fail
	s.mu.Unlock()

	if fail != "" && strings.Contains(strings.Join(args, " "), " "+fail+" ") {
		return model.CommandResult{ExitCode: 1, Stderr: fail + ": command not found"}
	}

	return s.inner.Run(ctx, args)
}

func (s *spy) count(word string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0

	for _, call := range s.calls {
		if strings.Contains(call, word) {
			n++
		}
	}

	return n
}

func (s *spy) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.calls)
}

func hostConfig() model.HostConfig {
	return model.HostConfig{
		Provider: Name, MaxRun: 2 * time.Hour, StaleMinutes: 20, PrivateRemote: "private",
		Block: model.NewBlock("t", []model.BlockEntry{model.TextEntry(BlockUser, "lane")}),
	}
}

func fastClock() Option {
	return WithClock(jobwait.Clock{Poll: time.Millisecond, Now: time.Now, Sleep: proc.Sleep})
}

// newClusterHost returns the host over a fake cluster that answers, with a bank that only counts.
func newClusterHost(t *testing.T, mode hosttest.Mode, cfg model.HostConfig) (*Host, *slurmtest.Cluster, *spy, *int) {
	t.Helper()

	f := slurmtest.New(mode)
	f.Up = mode != hosttest.Unreachable
	s := &spy{inner: f.SSH()}
	banks := new(int)

	h, err := NewHost(slurmtest.Target, cfg, s, fastClock(), WithNonce(fixedNonce), WithBank(func(context.Context) error { *banks++; return nil }))
	require.NoError(t, err)

	return h, f, s, banks
}

func TestHost_PassesTheConformanceSuiteAsAClusterTheOwnerProvides(t *testing.T) {
	t.Parallel()

	hosttest.TestHost(t, func(t *testing.T, mode hosttest.Mode) host.Host {
		t.Helper()

		h, _, _, _ := newClusterHost(t, mode, hostConfig())

		return h
	}, hosttest.NonOwning())
}

func TestNewHost_RunsNoSSHAndRefusesWhatCouldChangeTheCommandLine(t *testing.T) {
	t.Parallel()

	f := slurmtest.New(hosttest.Working)
	s := &spy{inner: f.SSH()}

	_, err := NewHost(slurmtest.Target, hostConfig(), s)
	require.NoError(t, err)
	assert.Zero(t, s.total(), "building the host sends nothing")

	text := func(key, value string) *model.Block {
		return model.NewBlock("t", []model.BlockEntry{model.TextEntry(key, value)})
	}

	for name, set := range map[string]func(*model.HostConfig){
		"a work root":   func(c *model.HostConfig) { c.Block = text(BlockWorkRoot, "/home/lane/work") },
		"no run limit":  func(c *model.HostConfig) { c.MaxRun = 0 },
		"a bad address": func(c *model.HostConfig) { c.Block = text(BlockAddress, "-oProxyCommand=x") },
		"a bad user":    func(c *model.HostConfig) { c.Block = text(BlockUser, "-oProxyCommand=x") },
		"a bad policy":  func(c *model.HostConfig) { c.Block = text(BlockKnownHosts, "no") },
	} {
		cfg := hostConfig()
		set(&cfg)

		_, err = NewHost(slurmtest.Target, cfg, s)

		require.ErrorIs(t, err, link.ErrConfig, name)
	}

	_, err = NewHost("-bad", hostConfig(), s)
	require.ErrorIs(t, err, link.ErrConfig, "a target that starts with a dash")
}

func TestNewHost_ARefusedWorkRootSaysWhyAndWhatToDo(t *testing.T) {
	t.Parallel()

	cfg := hostConfig()
	cfg.Block = model.NewBlock("t", []model.BlockEntry{model.TextEntry(BlockWorkRoot, "/home/lane/work")})

	_, err := NewHost(slurmtest.Target, cfg, &spy{inner: slurmtest.New(hosttest.Working).SSH()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "host.slurm.work_root")
	assert.Contains(t, err.Error(), "login folder")
}

func TestUp_ChecksTheHeadNodeAnswersAndChangesNothing(t *testing.T) {
	t.Parallel()

	h, f, s, _ := newClusterHost(t, hosttest.Working, hostConfig())

	require.NoError(t, h.Up(t.Context()))
	require.NoError(t, h.Up(t.Context()), "Up is idempotent")

	assert.Zero(t, s.count("sbatch"))
	assert.Zero(t, s.count("scontrol"))
	assert.Empty(t, f.Files, "Up writes nothing on the head node")
}

func TestUp_NamesTheSSHCommandWhenTheHeadNodeDoesNotAnswer(t *testing.T) {
	t.Parallel()

	h, _, _, _ := newClusterHost(t, hosttest.Unreachable, hostConfig())

	err := h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrDropped)
	assert.Contains(t, err.Error(), "ssh beehave-test")
}

func TestUp_RefusesAHeadNodeWithoutSlurm(t *testing.T) {
	t.Parallel()

	h, _, s, _ := newClusterHost(t, hosttest.Working, hostConfig())
	s.fail = "sinfo"

	err := h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrCommand)
	assert.Contains(t, err.Error(), "sinfo")
}

func TestStatus_ReadsSlurmAndDiskAndReportsADroppedLinkAsAbsent(t *testing.T) {
	t.Parallel()

	h, f, _, _ := newClusterHost(t, hosttest.Working, hostConfig())

	got, err := h.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, model.HostUp, got.State)
	assert.True(t, got.Disk.Readable)

	f.Mu.Lock()
	f.Up = false
	f.Mu.Unlock()

	got, err = h.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, model.HostAbsent, got.State)
}

func TestStatus_AFailureThatIsNotADroppedLinkIsAnErrorNotAnAbsentHost(t *testing.T) {
	t.Parallel()

	h, _, s, _ := newClusterHost(t, hosttest.Working, hostConfig())
	s.fail = "sinfo"

	_, err := h.Status(t.Context())

	require.ErrorIs(t, err, link.ErrCommand)
}

func TestDown_SendsNothingAndLeavesTheClusterUp(t *testing.T) {
	t.Parallel()

	h, _, s, _ := newClusterHost(t, hosttest.Working, hostConfig())
	before := s.total()

	require.NoError(t, h.Down(t.Context()))

	assert.Equal(t, before, s.total(), "beehave did not create the cluster, so Down sends no command")

	got, err := h.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, model.HostUp, got.State)
}

func TestTeardown_BanksTheWorkAndLeavesTheClusterUp(t *testing.T) {
	t.Parallel()

	h, _, s, banks := newClusterHost(t, hosttest.Working, hostConfig())

	require.NoError(t, h.Teardown(t.Context()))

	assert.Equal(t, 1, *banks)
	assert.Zero(t, s.count("scancel"), "Teardown stops no job")

	got, err := h.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, model.HostUp, got.State, "the cluster is not the cluster of beehave")
}

func TestTeardown_ABankThatFailsIsAnErrorThatSaysTheHostKeepsItsWork(t *testing.T) {
	t.Parallel()

	f := slurmtest.New(hosttest.Working)
	f.Up = true
	errBank := errors.New("push refused")

	h, err := NewHost(slurmtest.Target, hostConfig(), f.SSH(), WithBank(func(context.Context) error { return errBank }))
	require.NoError(t, err)

	err = h.Teardown(t.Context())

	require.ErrorIs(t, err, errBank)
	assert.Contains(t, err.Error(), "host keeps its work")
}

func TestSubmit_BoundsTheJobByMaxRunAndHoldsNoSlot(t *testing.T) {
	t.Parallel()

	h, f, s, _ := newClusterHost(t, hosttest.Working, hostConfig())

	_, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})

	require.NoError(t, err)
	assert.Equal(t, 1, s.count("--time=120"), "max_run of two hours is 120 minutes for sbatch")
	assert.Empty(t, f.Slots, "no slot job: the cluster is not deleted when idle")
}

func TestSubmit_RoundsTheRunLimitUpToWholeMinutes(t *testing.T) {
	t.Parallel()

	cfg := hostConfig()
	cfg.MaxRun = 90 * time.Second

	h, _, s, _ := newClusterHost(t, hosttest.Working, cfg)

	_, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})

	require.NoError(t, err)
	assert.Equal(t, 1, s.count("--time=2 "), "a limit of 90 seconds is two minutes, never zero")
}

func TestQueue_WithoutAHostSendsNoTimeLimit(t *testing.T) {
	t.Parallel()

	q, ssh := newScripted(t)
	ssh.Plan("42\n", "sbatch")

	_, err := q.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "x"})
	require.NoError(t, err)

	assert.NotContains(t, strings.Join(ssh.Calls[0], " "), "--time")
}
