package hosttest_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/hosttest"
	"github.com/tobocop2/obee/internal/model"
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

	switch spec.Command {
	case hosttest.FailingCommand:
		m.jobs[id] = model.JobFailed
	case hosttest.UnknownCommand:
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

func TestTestHost_RunsAgainstAnAdapter(t *testing.T) {
	t.Parallel()

	hosttest.TestHost(t, func(_ *testing.T, mode hosttest.Mode) host.Host {
		m := newMemHost()
		m.down = mode == hosttest.Broken
		m.noDisk = mode == hosttest.UnreadableDisk

		return m
	})
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
	assert.Len(t, rec.failures, 9)
}
