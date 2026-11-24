package flow

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

const (
	hostWork    = "/work"
	hostRepo    = "/work/repo"
	hostUnsaved = "1111111111111111111111111111111111111111"
)

// stubHost is a build host over fakes. Its work root is hostWork. Down counts the calls. A method it does not define panics.
type stubHost struct {
	host.Host

	downs int
}

func (*stubHost) WorkRoot(context.Context) (string, error) { return hostWork, nil }

func (h *stubHost) Down(context.Context) error {
	h.downs++

	return nil
}

func (*stubHost) Status(context.Context) (model.HostStatus, error) {
	return model.HostStatus{State: model.HostAbsent}, nil
}

// scriptRunner answers a command line from a function, so a test says what the host holds without a real git.
type scriptRunner func(args []string) model.CommandResult

func (f scriptRunner) Run(_ context.Context, args []string) model.CommandResult { return f(args) }

// fakeHosts stops the host of a project with the real host.Teardown over the stub host and a scripted git and find.
// With unbanked set, the one repository holds a commit that no remote has.
type fakeHosts struct {
	mu       sync.Mutex
	host     *stubHost
	unbanked bool
	private  string
	asked    []string
	clusters []string // the host that each teardown named
	ups      []string
	live     bool // the host was up before the run, so Up starts nothing
}

func (*fakeHosts) Sized(string, string) (bool, error) { return false, nil }

func (*fakeHosts) Defer(_ context.Context, _, cluster string) (bool, string, error) {
	return false, cluster, nil
}

func (f *fakeHosts) Up(_ context.Context, project, _ string, _ func(string) (int, error)) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.ups = append(f.ups, project)

	return !f.live, "", nil
}

func (f *fakeHosts) Teardown(ctx context.Context, project, cluster string) error {
	f.mu.Lock()
	f.asked = append(f.asked, project)
	f.clusters = append(f.clusters, cluster)
	f.mu.Unlock()

	find := scriptRunner(func([]string) model.CommandResult {
		return model.CommandResult{Stdout: hostRepo + "/.git\x00"}
	})

	git := scriptRunner(func(args []string) model.CommandResult {
		joined := strings.Join(args, " ")

		switch {
		case strings.Contains(joined, "for-each-ref --format=%(objecttype)") && f.unbanked:
			return model.CommandResult{Stdout: "commit\t" + hostUnsaved + "\t\t\trefs/heads/item-7\n"}
		case strings.Contains(joined, "config --get-regexp"):
			return model.CommandResult{ExitCode: 1}
		case strings.Contains(joined, "rev-list"):
			return model.CommandResult{ExitCode: 1}
		}

		return model.CommandResult{}
	})

	return host.Teardown(ctx, f.host, git, find, f.private)
}

// Ready says every host is ready.
func (*fakeHosts) Ready(context.Context, string, string) (bool, error) { return true, nil }

// LostProof gives no proof: no run of these examples loses a gate job.
func (*fakeHosts) LostProof(context.Context, string, string, string) (model.LostProof, error) {
	return model.LostProof{}, nil
}

var _ proc.Runner = scriptRunner(nil)

func TestExampleHost_ARoundStartsTheHostOnceBeforeAnyItem(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject}, rig.hosts.ups, "one host_up for the round, and none for a child")
}

func TestExampleHost_ARoundStopsTheHostOnceAfterItsWorkIsBanked(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject}, rig.hosts.asked, "one teardown for the round, and none for a child")
	assert.Equal(t, 1, rig.hosts.host.downs, "the host is deleted once")
}

func TestExampleHost_AHostWithWorkThatCannotBeBankedStaysUp(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.hosts.unbanked = true

	_, err := rig.run(t)

	require.NoError(t, err, "the round itself finished")
	assert.Zero(t, rig.hosts.host.downs, "the host is not deleted while work exists only on it")
}

func TestExampleHost_ARoundThatFoundTheHostLiveDoesNotStopIt(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "1 3")
	rig.hosts.live = true

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, rig.hosts.asked, "no teardown call for a host that another run started")
	assert.Zero(t, rig.hosts.host.downs)
}

type failingHosts struct{ err error }

func (failingHosts) Sized(string, string) (bool, error) { return false, nil }

func (failingHosts) Defer(context.Context, string, string) (bool, string, error) {
	return false, "", nil
}

func (failingHosts) Up(context.Context, string, string, func(string) (int, error)) (bool, string, error) {
	return false, "", nil
}

func (f failingHosts) Teardown(context.Context, string, string) error { return f.err }

func (f failingHosts) Ready(context.Context, string, string) (bool, error) { return false, f.err }

func (f failingHosts) LostProof(context.Context, string, string, string) (model.LostProof, error) {
	return model.LostProof{}, f.err
}

func TestStopHost_ACanceledRunRecordsNoResultSoAResumeStopsTheHostAgain(t *testing.T) {
	t.Parallel()

	// The engine runs; only the context of the step ends, as it does when beehave cancels a run.
	s := &script{e: &Engine{ctx: newHarness(t).engine(t).ctx, deps: Deps{Hosts: failingHosts{err: context.Canceled}}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.stopHost(ctx, "stop")
	require.ErrorIs(t, err, context.Canceled)

	live, err := s.stopHost(t.Context(), "stop")
	require.NoError(t, err)
	assert.Equal(t, false, live.Outputs["down"], "a refusal on a live run is a result")
	assert.Contains(t, live.Outputs["reason"], "canceled")
}

func TestHostTeardown_ABuildWithNoHostStopperFailsTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    host_teardown(name = \"stop\")\n")

	_, err := h.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "cannot stop a host")
}
