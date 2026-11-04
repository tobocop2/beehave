package skypilot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/hosttest"
	"github.com/tobocop2/obee/internal/host/slurm"
	"github.com/tobocop2/obee/internal/host/slurmtest"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

const (
	fakeCluster = slurmtest.Target
	fakeDF      = slurmtest.DF
	sshFailed   = 255 // the exit status of ssh for a link that dropped
)

// fakeHost is a sky command line over the same state as the fake Slurm cluster behind the ssh command line.
type fakeHost struct {
	*slurmtest.Cluster
}

func newFakeHost(mode hosttest.Mode) *fakeHost { return &fakeHost{slurmtest.New(mode)} }

// slotMarker returns the marker that a slot command waits for.
func slotMarker(command string) string {
	_, rest, _ := strings.Cut(command, "test -e ")
	marker, _, _ := strings.Cut(rest, ";")

	return marker
}

func (f *fakeHost) sky() fakeRunner { return fakeRunner{run: f.runSky} }

type fakeRunner struct {
	run func(args []string) model.CommandResult
}

func (r fakeRunner) Run(_ context.Context, args []string) model.CommandResult { return r.run(args) }

func failure(text string) model.CommandResult { return model.CommandResult{ExitCode: 1, Stderr: text} }

func ok(out string) model.CommandResult { return model.CommandResult{Stdout: out} }

func (f *fakeHost) runSky(args []string) model.CommandResult {
	f.Mu.Lock()
	defer f.Mu.Unlock()

	if f.Broken {
		return failure("sky: broken")
	}

	switch args[0] {
	case "launch":
		f.Up = true

		return ok("")
	case "down":
		f.Up, f.Draining = false, false

		return ok("")
	case "exec":
		f.Slots = append(f.Slots, slotMarker(args[len(args)-1]))

		return ok("")
	case "status":
		if !f.Up {
			return ok("[]\n")
		}

		return ok(fmt.Sprintf(`[{"name": %q, "status": "UP", "autostop": 60, "to_down": true}, {"name": "other", "status": "UP"}]`, fakeCluster))
	}

	return failure("sky: unknown command")
}

func newTestHost(t *testing.T, mode hosttest.Mode) host.Host {
	t.Helper()

	f := newFakeHost(mode)
	config := model.HostConfig{Provider: model.HostSkyPilot, IdleMinutes: 60, MaxRun: 24 * time.Hour}

	h, err := New(fakeCluster, config, f.sky(), f.SSH(), fastWaits()...)
	if err != nil {
		t.Fatal(err)
	}

	return h
}

// fastWaits are the options of a test host whose polls are one millisecond apart and whose bank has nothing to do.
func fastWaits() []slurm.Option {
	return []slurm.Option{slurm.WithClock(time.Millisecond, time.Now, proc.Sleep), slurm.WithBank(func(context.Context) error { return nil })}
}
