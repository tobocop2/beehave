package hosttest_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host"
	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/ifacetest"
	"github.com/tobocop2/beehest/internal/model"
)

var (
	errDown  = errors.New("down")
	errNotUp = errors.New("not up")
)

// recorder is a Reporter that keeps the failures instead of failing the test.
type recorder struct {
	failures []string
}

func (*recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// memHost is a host in memory. down fails every call. noDisk makes the disk reading unreadable. stuck leaves every job running.
type memHost struct {
	down   bool
	noLog  bool
	noDisk bool
	stuck  bool
	root   string
	state  model.HostState
	jobs   map[model.JobID]model.JobState
}

func newMemHost() *memHost {
	return &memHost{state: model.HostAbsent, jobs: map[model.JobID]model.JobState{}}
}

func (m *memHost) fail() error {
	if m.down {
		return errDown
	}

	return nil
}

func (m *memHost) set(state model.HostState) error {
	m.state = state

	return m.fail()
}

func (m *memHost) Up(context.Context) error { return m.set(model.HostUp) }

func (m *memHost) Down(context.Context) error { return m.set(model.HostAbsent) }

func (m *memHost) Drain(context.Context) error { return m.set(model.HostDraining) }

func (m *memHost) Resume(context.Context) error { return m.set(model.HostUp) }

func (m *memHost) Status(context.Context) (model.HostStatus, error) {
	return model.HostStatus{State: m.state, Disk: model.DiskReading{FreeBytes: 1 << 40, Readable: !m.noDisk}}, m.fail()
}

func (m *memHost) Submit(_ context.Context, spec model.JobSpec) (model.JobID, error) {
	if m.state == model.HostAbsent {
		return "", errNotUp
	}

	id := model.JobID(strconv.Itoa(len(m.jobs) + 1))
	m.jobs[id] = model.JobSucceeded

	switch {
	case spec.Command == hosttest.FailingCommand, hosttest.Mangled(spec.Command):
		m.jobs[id] = model.JobFailed
	case spec.Command == hosttest.UnknownCommand:
		m.jobs[id] = model.JobUnknown
	}

	if m.stuck {
		m.jobs[id] = model.JobRunning
	}

	return id, m.fail()
}

func (m *memHost) Wait(_ context.Context, id model.JobID) (model.JobState, error) {
	return m.jobs[id], m.fail()
}

func (m *memHost) Cancel(_ context.Context, id model.JobID) error {
	m.jobs[id] = model.JobCanceled

	return m.fail()
}

func (m *memHost) Log(context.Context, model.JobID) (string, error) {
	if m.noLog {
		return "", errDown
	}

	return "", m.fail()
}

func (m *memHost) LostProof(context.Context, model.JobID) (model.LostProof, error) {
	return model.LostProof{}, m.fail()
}

func (m *memHost) WorkRoot(context.Context) (string, error) {
	if m.root != "" {
		return m.root, m.fail()
	}

	return "/home/lane", m.fail()
}

func TestCheckHostWorks_RefusesAWorkRootThatIsNotOneAbsolutePath(t *testing.T) {
	t.Parallel()

	for _, root := range []string{"home/lane", ".", "~"} {
		m := newMemHost()
		m.root = root
		rec := &recorder{}

		hosttest.CheckHostWorks(rec, m)

		assert.Contains(t, fmt.Sprint(rec.failures), "one absolute path", root)
	}
}

func TestCheckHostWorks_RefusesAWorkRootThatChanges(t *testing.T) {
	t.Parallel()

	rec := &recorder{}

	hosttest.CheckHostWorks(rec, &driftingRoot{memHost: newMemHost()})

	assert.Contains(t, fmt.Sprint(rec.failures), "the work root in a second call")
}

// driftingRoot names a new work root in each call.
type driftingRoot struct {
	*memHost
	calls int
}

func (d *driftingRoot) WorkRoot(context.Context) (string, error) {
	d.calls++

	return "/home/lane" + strconv.Itoa(d.calls), nil
}

// lostHost is a host whose proof read answers from fields. up says whether the link is up.
type lostHost struct {
	*memHost

	up       bool
	whenDown model.LostProof
	whenUp   model.LostProof
	err      error
}

func (l *lostHost) LostProof(context.Context, model.JobID) (model.LostProof, error) {
	if l.up {
		return l.whenUp, l.err
	}

	return l.whenDown, l.err
}

func TestCheckLostProof_ReportsAProofWhileTheLinkIsDownAnErrorAndAWrongKind(t *testing.T) {
	t.Parallel()

	stopped := model.LostProof{Kind: model.LostStopped}

	for name, tc := range map[string]struct {
		host *lostHost
		want string
	}{
		"a proof while the link is down": {&lostHost{memHost: newMemHost(), whenDown: stopped, whenUp: stopped}, "the proof while the link is down"},
		"an error":                       {&lostHost{memHost: newMemHost(), whenUp: stopped, err: errDown}, "unexpected error"},
		"another kind of proof":          {&lostHost{memHost: newMemHost(), whenUp: model.LostProof{Kind: model.LostEnded}}, "the proof once the link is back"},
		"no proof where one is due":      {&lostHost{memHost: newMemHost()}, "the proof once the link is back"},
	} {
		rec := &recorder{}
		tc.host.up = false

		hosttest.CheckLostProof(rec, tc.host, hosttest.LostJob{
			ID: "1", LinkDown: func() { tc.host.up = false }, LinkUp: func() { tc.host.up = true }, Proof: model.LostStopped,
		})

		assert.Contains(t, fmt.Sprint(rec.failures), tc.want, name)
	}

	rec := &recorder{}
	good := &lostHost{memHost: newMemHost(), whenUp: stopped}

	hosttest.CheckLostProof(rec, good, hosttest.LostJob{ID: "1", LinkDown: func() { good.up = false }, LinkUp: func() { good.up = true }, Proof: model.LostStopped})

	assert.Empty(t, rec.failures, "a host that gives no proof while the link is down and its proof after passes")
}

func TestTestHost_RunsAgainstAnAdapter(t *testing.T) {
	t.Parallel()

	hosttest.TestHost(t, func(_ *testing.T, mode hosttest.Mode) host.Host {
		m := newMemHost()
		m.down = mode == hosttest.Broken
		m.noDisk = mode == hosttest.UnreadableDisk
		m.noLog = mode == hosttest.UnreadableLog

		return m
	})
}

func TestCheckUnreadableLog_AcceptsAnErrorAndFailsAHostThatReadsAnEmptyLog(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	hosttest.CheckUnreadableLog(rec, &memHost{noLog: true, state: model.HostAbsent, jobs: map[model.JobID]model.JobState{}})
	assert.Empty(t, rec.failures)

	rec = &recorder{}
	hosttest.CheckUnreadableLog(rec, newMemHost())
	assert.Equal(t, []string{"Log of a log that cannot be read: want an error from a failed command, got none"}, rec.failures)
}

func TestCheckBeforeUp(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	hosttest.CheckBeforeUp(rec, newMemHost())
	assert.Empty(t, rec.failures)

	rec = &recorder{}
	hosttest.CheckBeforeUp(rec, &memHost{state: model.HostUp, jobs: map[model.JobID]model.JobState{}})
	assert.Contains(t, fmt.Sprint(rec.failures), "state before Up")
	assert.Contains(t, fmt.Sprint(rec.failures), "Submit before Up")
}

func TestCheckDownTwice(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	hosttest.CheckDownTwice(rec, newMemHost())
	assert.Empty(t, rec.failures)

	rec = &recorder{}
	hosttest.CheckDownTwice(rec, &stickyHost{newMemHost()})
	assert.Contains(t, fmt.Sprint(rec.failures), "state after a second Down")
}

// stickyHost reports itself up after every Down.
type stickyHost struct{ *memHost }

func (s *stickyHost) Down(context.Context) error { return s.set(model.HostUp) }

func TestCheckUnreadableDisk(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	hosttest.CheckUnreadableDisk(rec, &memHost{noDisk: true, state: model.HostAbsent, jobs: map[model.JobID]model.JobState{}})
	assert.Empty(t, rec.failures)

	rec = &recorder{}
	hosttest.CheckUnreadableDisk(rec, newMemHost())
	assert.Contains(t, fmt.Sprint(rec.failures), "readable reading")
}

func TestCheckUnknownState(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	hosttest.CheckUnknownState(rec, newMemHost())
	assert.Empty(t, rec.failures, "the memory host leaves an unknown job in the unknown state")

	rec = &recorder{}
	hosttest.CheckUnknownState(rec, &finalHost{newMemHost()})
	assert.Contains(t, fmt.Sprint(rec.failures), "final state")
}

// finalHost reports every job as succeeded, whatever its command.
type finalHost struct{ *memHost }

func (finalHost) Wait(context.Context, model.JobID) (model.JobState, error) {
	return model.JobSucceeded, nil
}

// A transport that changes the command text is caught by the suite, whatever the adapter.
func TestCheckHostWorks_ReportsATransportThatChangesTheCommandText(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	hosttest.CheckHostWorks(rec, &quotingHost{newMemHost()})

	assert.Contains(t, fmt.Sprint(rec.failures), "a transport could change")
	assert.True(t, hosttest.Mangled(hosttest.HostileCommand+" "))
	assert.False(t, hosttest.Mangled(hosttest.HostileCommand))
	assert.False(t, hosttest.Mangled(hosttest.PassingCommand))
}

// quotingHost drops the quotes of every command, as a transport that shell-splits the text would.
type quotingHost struct{ *memHost }

func (q *quotingHost) Submit(ctx context.Context, spec model.JobSpec) (model.JobID, error) {
	spec.Command = strings.ReplaceAll(spec.Command, "'", "")

	return q.memHost.Submit(ctx, spec)
}

func TestCheckHostWorks_AcceptsAGoodHost(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	hosttest.CheckHostWorks(rec, newMemHost())

	assert.Empty(t, rec.failures)
}

func TestCheckHostWorks_ReportsABrokenHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host *memHost
		want string
	}{
		{"every_command_fails", &memHost{down: true, state: model.HostAbsent, jobs: map[model.JobID]model.JobState{}}, "unexpected error"},
		{"disk_is_unreadable", &memHost{noDisk: true, state: model.HostAbsent, jobs: map[model.JobID]model.JobState{}}, "the disk reading is readable"},
		{"a_job_never_ends", &memHost{stuck: true, state: model.HostAbsent, jobs: map[model.JobID]model.JobState{}}, "state of a passing job"},
		{"a_host_that_starts_up", &memHost{state: model.HostUp, jobs: map[model.JobID]model.JobState{}}, "state before Up"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := &recorder{}
			hosttest.CheckHostWorks(rec, tc.host)

			assert.Contains(t, fmt.Sprint(rec.failures), tc.want)
		})
	}
}

func TestCheckHostFails_AcceptsABrokenHostAndReportsAGoodOne(t *testing.T) {
	t.Parallel()

	bad := newMemHost()
	bad.down = true
	rec := &recorder{}
	hosttest.CheckHostFails(rec, bad)
	assert.Empty(t, rec.failures)

	rec = &recorder{}
	hosttest.CheckHostFails(rec, newMemHost())
	assert.Len(t, rec.failures, 10)
	assert.Contains(t, rec.failures, "Log: want an error from a failed command, got none")
}

// TestFailedCommandMethods_CoverEveryMethodOfHost fails when a method of host.Host has no failed-command case.
func TestFailedCommandMethods_CoverEveryMethodOfHost(t *testing.T) {
	t.Parallel()

	ran := map[string]bool{}
	for _, name := range hosttest.FailedCommandMethods() {
		ran[name] = true
	}

	declared, err := ifacetest.Methods([]string{"../host.go"}, "Host")
	require.NoError(t, err)

	for _, name := range declared {
		assert.True(t, ran[name], "host.Host.%s has no failed-command case", name)
	}

	for name := range ran {
		assert.Contains(t, declared, name, "a failed-command case runs %s, which host.Host lacks", name)
	}
}
