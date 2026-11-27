package hosttest_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host"
	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/link"
	"github.com/tobocop2/beehest/internal/host/skypilot"
	"github.com/tobocop2/beehest/internal/host/slurm"
	"github.com/tobocop2/beehest/internal/host/slurmtest"
	"github.com/tobocop2/beehest/internal/host/ssh"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc/proctest"
)

// drainingSSH returns the ssh adapter over a fake machine. The owner provides the machine, so it is up.
func drainingSSH(t *testing.T) host.Host {
	t.Helper()

	h, _ := lostSSH(t)

	return h
}

// drainingSlurm returns the Slurm adapter over a fake cluster that is up.
func drainingSlurm(t *testing.T) host.Host {
	t.Helper()

	h, _ := lostSlurm(t)

	return h
}

// drainingSkyPilot returns the SkyPilot adapter whose cluster is up, with its queue on a fake Slurm head node.
func drainingSkyPilot(t *testing.T) host.Host {
	t.Helper()

	f := slurmtest.New(hosttest.Working)
	f.Up = true

	sky := &proctest.Scripted{}
	sky.Plan(fmt.Sprintf(`[{"name": %q, "status": "UP"}]`, slurmtest.Target), "status")

	cfg := model.HostConfig{Provider: skypilot.Name, IdleMinutes: 60, MaxRun: 2 * time.Hour}

	h, err := skypilot.New(slurmtest.Target, cfg, sky, f.SSH(), func(l *link.Link, config model.HostConfig, slot skypilot.Slot) skypilot.Queue {
		return slurm.New(l, config, slot, slurm.WithLimit(config.MaxRun))
	})
	require.NoError(t, err)

	return h
}

// undrained is a host that has only the methods of host.Host: the embedded interface hides the Drain and Resume of the host inside.
type undrained struct {
	host.Host
}

func TestHostConformance_DrainRefusesNewJobsAndResumeTakesThemAgainOnEveryAdapterThatDrains(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func(*testing.T) host.Host{
		string(ssh.Name):      drainingSSH,
		string(slurm.Name):    drainingSlurm,
		string(skypilot.Name): drainingSkyPilot,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			hosttest.CheckDrain(t, build(t))
		})
	}

	t.Run("a host without the drain fails the check", func(t *testing.T) {
		t.Parallel()

		m := newMemHost()
		m.state = model.HostUp

		for what, check := range map[string]func(hosttest.Reporter, host.Host){
			"CheckDrain":     hosttest.CheckDrain,
			"CheckHostFails": hosttest.CheckHostFails,
		} {
			rec := &recorder{}

			check(rec, undrained{Host: m})

			assert.Contains(t, fmt.Sprint(rec.failures), "has no Drain and Resume", what)
		}
	})
}
