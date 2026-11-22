package flow

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/host/sshtest"
	"github.com/tobocop2/beehave/internal/model"
)

// sshGate is the gate of an engine whose gate jobs run on one ssh host. Submit starts the job, and then the link to the machine goes down.
type sshGate struct {
	host    *ssh.Host
	machine *sshtest.Machine
}

func (*sshGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (g *sshGate) Submit(ctx context.Context, _, _, _, _, _, _ string) (GateJob, error) {
	id, err := g.host.Submit(ctx, model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})
	if err != nil {
		return GateJob{}, err
	}

	g.machine.Mu.Lock()
	g.machine.Drops = -1
	g.machine.Mu.Unlock()

	return GateJob{ID: string(id)}, nil
}

func (g *sshGate) Run(ctx context.Context, _, _, _ string, job GateJob) (GateResult, error) {
	_, err := g.host.Wait(ctx, model.JobID(job.ID))

	return GateResult{}, err
}

// sshHosts reads the proof of a lost job from the ssh host. The link comes back before the read number backAt.
type sshHosts struct {
	*upHosts

	host    *ssh.Host
	machine *sshtest.Machine
	backAt  int
}

func (s *sshHosts) LostProof(ctx context.Context, project, cluster, job string) (model.LostProof, error) {
	if s.proofReads()+1 >= s.backAt {
		s.machine.Mu.Lock()
		s.machine.Drops = 0
		s.machine.Mu.Unlock()
	}

	if _, err := s.upHosts.LostProof(ctx, project, cluster, job); err != nil {
		return model.LostProof{}, err
	}

	return s.host.LostProof(ctx, model.JobID(job))
}

func TestHostLoss_TheSshAdapterEndsTheGateVoidAfterTheHostComesBack(t *testing.T) {
	t.Parallel()

	machine := sshtest.New(hosttest.Working)

	var (
		mu  sync.Mutex
		now = time.Unix(1_000_000, 0)
		n   int
	)

	// The clock of the wait moves by each sleep, so the 10 minutes of the lost read pass at once.
	clock := jobwait.Clock{
		Poll: 10 * time.Second,
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()

			return now
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			defer mu.Unlock()

			now = now.Add(d)

			return nil
		},
	}

	host, err := ssh.New("gates", model.HostConfig{
		Provider: ssh.Name, MaxRun: 2 * time.Hour, StaleMinutes: 20, PrivateRemote: "private",
		Block: model.NewBlock("test", []model.BlockEntry{
			model.TextEntry(ssh.BlockUser, "lane"), model.TextEntry(ssh.BlockWorkRoot, sshtest.Root+"/work"), model.TextEntry(ssh.BlockKnownHosts, model.KnownHostsStrict),
		}),
	}, machine.SSH(), ssh.WithClock(clock), ssh.WithBank(func(context.Context) error { return nil }), ssh.WithNonce(func() string { n++; return "n" + strconv.Itoa(n) }))
	require.NoError(t, err)

	h := newHarness(t)
	reading := newReadingClock()
	hosts := &sshHosts{upHosts: &upHosts{started: true, afterProof: func() { reading.advance(time.Minute) }}, host: host, machine: machine, backAt: 2}
	h.hosts, h.reading, h.facts = hosts, reading.read, lostFacts(time.Hour)
	engine := h.engineWith(t, func(e *Engine) { e.deps.Gate = &sshGate{host: host, machine: machine} })

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(lostGateBody)), modeOff())

	require.ErrorContains(t, err, `GATE passed=False steps=["unknown", "unknown"] proof=stopped host=gpu-b job=beehave-n1`)
	assert.Equal(t, 2, hosts.proofReads(), "the first read finds the link down and gives no proof, and the second read comes after the host is back")
	assert.True(t, machine.Units["beehave-n1"].Stopped, "the proof is the stop of the unit on the machine")
	assert.True(t, machine.Files[stateFolderOnMachine+"/beehave-n1.canceled"], "the stop writes the marker of the job")
}

// stateFolderOnMachine is the state folder of the ssh host on the fake machine.
const stateFolderOnMachine = sshtest.Root + "/work/.beehave"
