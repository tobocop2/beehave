package hosttest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/model"
)

// keptHost is a machine that the owner provides: it is up before Up and after Down, and Up and Down send nothing.
type keptHost struct{ *memHost }

func newKeptHost() *keptHost {
	m := newMemHost()
	m.state = model.HostUp

	return &keptHost{m}
}

func (k *keptHost) Up(context.Context) error { return k.fail() }

func (*keptHost) Down(context.Context) error { return nil }

func TestCheckKeptHostWorks_PassesForAMachineThatDownLeavesUp(t *testing.T) {
	t.Parallel()

	rec := &recorder{}

	hosttest.CheckKeptHostWorks(rec, newKeptHost())

	assert.Empty(t, rec.failures)
}

func TestCheckKeptHostWorks_FailsForAHostThatDownRemoves(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	m := newMemHost()
	m.state = model.HostUp

	hosttest.CheckKeptHostWorks(rec, m)

	assert.Contains(t, rec.failures, "state after Down: got absent, want up")
}

func TestCheckUnreachable_PassesForAHostWhoseMachineDoesNotAnswer(t *testing.T) {
	t.Parallel()

	rec := &recorder{}

	hosttest.CheckUnreachable(rec, unreachable{newMemHost()})

	assert.Empty(t, rec.failures)
}

func TestCheckUnreachable_FailsForAHostThatTakesAJob(t *testing.T) {
	t.Parallel()

	rec := &recorder{}

	hosttest.CheckUnreachable(rec, newKeptHost())

	assert.NotEmpty(t, rec.failures)
}

// unreachable is a host whose machine does not answer: Up fails and Status reports it absent.
type unreachable struct{ *memHost }

func (unreachable) Up(context.Context) error { return errNotUp }

func TestTestHost_NonOwningRunsTheKeptChecks(t *testing.T) {
	t.Parallel()

	hosttest.TestHost(t, func(_ *testing.T, mode hosttest.Mode) host.Host {
		switch mode {
		case hosttest.Broken:
			m := newKeptHost()
			m.down = true

			return m
		case hosttest.UnreadableDisk:
			m := newKeptHost()
			m.noDisk = true

			return m
		case hosttest.Unreachable:
			return unreachable{newMemHost()}
		case hosttest.Working:
		}

		return newKeptHost()
	}, hosttest.NonOwning())
}
