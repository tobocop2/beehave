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
	ups      []string
	live     bool // the host was up before the run, so Up starts nothing
}

func (f *fakeHosts) Up(_ context.Context, project, _ string) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.ups = append(f.ups, project)

	return !f.live, "", nil
}

func (f *fakeHosts) Teardown(ctx context.Context, project, _ string) error {
	f.mu.Lock()
	f.asked = append(f.asked, project)
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

var _ proc.Runner = scriptRunner(nil)

func hostExample(t *testing.T, teardown string) (*agentRig, string, *fakeHosts, *fakeWriter) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	hosts := &fakeHosts{host: &stubHost{}}
	writer := newFakeWriter()

	rig.h.hosts = hosts
	rig.h.writer = writer

	rig.h.opts.Env["HOST_UP"] = "true"

	if teardown != "" {
		rig.h.opts.Env["HOST_TEARDOWN"] = teardown
	}

	return rig, file, hosts, writer
}

func TestExample_ARunWithoutHostUpStartsNoHost(t *testing.T) {
	t.Parallel()

	rig, file, hosts, _ := hostExample(t, "")
	delete(rig.h.opts.Env, "HOST_UP")

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
	assert.Empty(t, hosts.ups, "HOST_UP is not true, so the run starts no host")
}

func TestExample_AHostRunStopsTheHostAfterItsWorkIsBanked(t *testing.T) {
	t.Parallel()

	rig, file, hosts, writer := hostExample(t, "true")

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
	assert.Equal(t, []string{testProject}, hosts.asked)
	assert.Equal(t, 1, hosts.host.downs, "the host is deleted once")
	assert.Empty(t, writer.notes, "a host that went down needs no warning")
}

func TestExample_AHostWithWorkThatCannotBeBankedStaysUpAndTheRunSaysWhy(t *testing.T) {
	t.Parallel()

	rig, file, hosts, writer := hostExample(t, "true")
	hosts.unbanked = true

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err, "the item itself finished")
	assert.Equal(t, "merged", result.Merge)
	assert.Zero(t, hosts.host.downs, "the host is not deleted while work exists only on it")
	require.Len(t, writer.notes, 1, "the stay is reported on the item")
	assert.Contains(t, writer.notes[0], "The build host stays up.")
	assert.NotContains(t, writer.notes[0], "billing")
	assert.Contains(t, writer.notes[0], "host stays up")
	assert.Contains(t, writer.notes[0], "/work/repo refs/heads/item-7", "the note names the branch that only the host holds")
}

func TestExample_ARunWithoutAHostCallsNoTeardown(t *testing.T) {
	t.Parallel()

	rig, file, hosts, writer := hostExample(t, "")

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Empty(t, hosts.asked, "no host call")
	assert.Zero(t, hosts.host.downs)
	assert.Empty(t, writer.notes)
}

func TestExample_AHostRunStartsTheHostOnceBeforeTheItem(t *testing.T) {
	t.Parallel()

	rig, file, hosts, _ := hostExample(t, "")

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
	assert.Equal(t, []string{testProject}, hosts.ups, "the host is started once")
}

func TestExample_ARunThatFoundTheHostLiveDoesNotStopIt(t *testing.T) {
	t.Parallel()

	rig, file, hosts, writer := hostExample(t, "true")
	hosts.live = true

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Empty(t, hosts.asked, "no teardown call for a host that another run started")
	assert.Zero(t, hosts.host.downs)
	require.Len(t, writer.notes, 1)
	assert.Contains(t, writer.notes[0], "did not start the host")
}

type failingHosts struct{ err error }

func (failingHosts) Up(context.Context, string, string) (bool, string, error) { return false, "", nil }

func (f failingHosts) Teardown(context.Context, string, string) error { return f.err }

func TestStopHost_ACanceledRunRecordsNoResultSoAResumeStopsTheHostAgain(t *testing.T) {
	t.Parallel()

	s := &script{e: &Engine{deps: Deps{Hosts: failingHosts{err: context.Canceled}}}}
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
