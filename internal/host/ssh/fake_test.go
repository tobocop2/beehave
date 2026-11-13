package ssh_test

import (
	"context"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/host/sshtest"
	"github.com/tobocop2/beehave/internal/model"
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

func config() model.HostConfig { return configWith(nil) }

// configWith returns the host of config() with the keys of set written over the ssh block. An empty value leaves the key out.
func configWith(set map[string]string) model.HostConfig {
	keys := map[string]string{ssh.BlockUser: "lane", ssh.BlockWorkRoot: sshtest.Root + "/work", ssh.BlockKnownHosts: model.KnownHostsStrict}
	maps.Copy(keys, set)

	var entries []model.BlockEntry

	for key, value := range keys {
		if value != "" {
			entries = append(entries, model.TextEntry(key, value))
		}
	}

	return model.HostConfig{
		Provider: ssh.Name, MaxRun: 2 * time.Hour, StaleMinutes: 20, PrivateRemote: "private",
		Block: model.NewBlock("t", entries),
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
