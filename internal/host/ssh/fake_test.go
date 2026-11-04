package ssh_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host/jobwait"
	"github.com/tobocop2/obee/internal/host/ssh"
	"github.com/tobocop2/obee/internal/host/sshtest"
	"github.com/tobocop2/obee/internal/model"
)

// fakeClock moves its time forward by the sleep, so a wait of hours runs at once.
type fakeClock struct{ now time.Time }

func newClock() *fakeClock { return &fakeClock{now: time.Unix(1_000_000, 0)} }

func (c *fakeClock) clock() jobwait.Clock {
	return jobwait.Clock{
		Poll:  10 * time.Second,
		Now:   func() time.Time { return c.now },
		Sleep: func(_ context.Context, d time.Duration) error { c.now = c.now.Add(d); return nil },
	}
}

// bankLog counts the banks and keeps what the machine had done when each one ran.
type bankLog struct {
	m     *sshtest.Machine
	calls []int
	err   error
}

func (b *bankLog) bank(context.Context) error {
	b.m.Mu.Lock()
	defer b.m.Mu.Unlock()

	b.calls = append(b.calls, len(b.m.Calls))

	return b.err
}

func config() model.HostConfig {
	return model.HostConfig{
		Provider: model.HostSSH, MaxRun: 2 * time.Hour, StaleMinutes: 20, PrivateRemote: "private",
		SSH: model.SSHConfig{User: "lane", WorkRoot: sshtest.Root + "/work", KnownHosts: model.KnownHostsStrict},
	}
}

// newHost returns the host over a machine, with a fake clock and a bank that only counts.
func newHost(t *testing.T, m *sshtest.Machine, cfg model.HostConfig) (*ssh.Host, *bankLog, *fakeClock) {
	t.Helper()

	clock, bank := newClock(), &bankLog{m: m}
	n := 0

	h, err := ssh.New("gates", cfg, m.SSH(),
		ssh.WithClock(clock.clock()), ssh.WithBank(bank.bank),
		ssh.WithNonce(func() string { n++; return "n" + strconv.Itoa(n) }))
	require.NoError(t, err)

	return h, bank, clock
}

// words returns the first words of every call the machine took, joined, for an order check.
func words(m *sshtest.Machine) []string {
	m.Mu.Lock()
	defer m.Mu.Unlock()

	var out []string

	for _, call := range m.Calls {
		out = append(out, strings.Join(call, " "))
	}

	return out
}

func calledWith(m *sshtest.Machine, prefix string) int {
	count := 0

	for _, call := range words(m) {
		if strings.HasPrefix(call, prefix) {
			count++
		}
	}

	return count
}
