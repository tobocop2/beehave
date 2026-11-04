package host_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

var errDown = errors.New("down")

// stubHost answers Status, Wait and Down from fields. Every other method does nothing.
type stubHost struct {
	status    model.HostStatus
	statusErr error
	state     model.JobState
	waitErr   error
	root      string
	rootErr   error
	downs     int
	submits   int
}

func (s *stubHost) WorkRoot(context.Context) (string, error) { return s.root, s.rootErr }

func (*stubHost) Log(context.Context, model.JobID) (string, error) { return "", nil }

func (*stubHost) Up(context.Context) error { return nil }

func (s *stubHost) Status(context.Context) (model.HostStatus, error) { return s.status, s.statusErr }

func (s *stubHost) Down(context.Context) error {
	s.downs++
	s.status.State = model.HostAbsent

	return nil
}

func (s *stubHost) Submit(context.Context, model.JobSpec) (model.JobID, error) {
	s.submits++

	return "1", nil
}

func (s *stubHost) Wait(context.Context, model.JobID) (model.JobState, error) {
	return s.state, s.waitErr
}

func (*stubHost) Cancel(context.Context, model.JobID) error { return nil }

func (*stubHost) Drain(context.Context) error { return nil }

func (*stubHost) Resume(context.Context) error { return nil }

const gb = 1 << 30

func TestDiskFloor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		host    *stubHost
		wantErr error
	}{
		{"enough_space", &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 60 * gb, Readable: true}}}, nil},
		{"exactly_the_floor", &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 50 * gb, Readable: true}}}, nil},
		{"under_the_floor", &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 49 * gb, Readable: true}}}, host.ErrDiskLow},
		{"unreadable_reading", &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 900 * gb}}}, host.ErrDiskRead},
		{"status_fails", &stubHost{statusErr: errDown}, host.ErrDiskRead},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := host.DiskFloor(t.Context(), tc.host, 50)

			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestDiskFloor_ABrokenStatusKeepsItsCause(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, host.DiskFloor(t.Context(), &stubHost{statusErr: errDown}, 50), errDown)
}

func TestWithDiskFloor_ASubmitStartsOnlyOnAHostAboveTheFloor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		host       *stubHost
		wantErr    error
		wantSubmit int
	}{
		{"enough_space", &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 60 * gb, Readable: true}}}, nil, 1},
		{"under_the_floor", &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 10 * gb, Readable: true}}}, host.ErrDiskLow, 0},
		{"unreadable_reading", &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 900 * gb}}}, host.ErrDiskRead, 0},
		{"status_fails", &stubHost{statusErr: errDown}, errDown, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			id, err := host.WithDiskFloor(tc.host, 50).Submit(t.Context(), model.JobSpec{Name: "gate-p"})

			assert.Equal(t, tc.wantSubmit, tc.host.submits)

			if tc.wantErr == nil {
				require.NoError(t, err)
				assert.Equal(t, model.JobID("1"), id)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
			assert.Empty(t, id)
			assert.Contains(t, err.Error(), "job gate-p does not start")
		})
	}
}

func TestWithDiskFloor_UnderTheFloorNamesTheFreeAndTheNeededSpace(t *testing.T) {
	t.Parallel()

	stub := &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 10 * gb, Readable: true}}}
	_, err := host.WithDiskFloor(stub, 50).Submit(t.Context(), model.JobSpec{Name: "j"})

	require.ErrorContains(t, err, "10 GB free, floor 50 GB")
}

// freeing is a host whose free space grows by 30 GB at each Status call, from a start value.
type freeing struct {
	*stubHost

	free  uint64
	reads int
}

func (f *freeing) Status(context.Context) (model.HostStatus, error) {
	f.reads++
	f.free += 30 * gb

	return model.HostStatus{State: model.HostUp, Disk: model.DiskReading{FreeBytes: f.free, Readable: true}}, nil
}

func TestWithDiskWait_AJobWaitsForTheSpaceInsteadOfRefusing(t *testing.T) {
	t.Parallel()

	stub := &freeing{stubHost: &stubHost{}, free: 0}
	id, err := host.WithDiskWait(stub, 50, time.Minute, time.Millisecond).Submit(t.Context(), model.JobSpec{Name: "gate-p"})

	require.NoError(t, err)
	assert.Equal(t, model.JobID("1"), id)
	assert.Equal(t, 2, stub.reads, "one read under the floor, one above it")
	assert.Equal(t, 1, stub.submits)
}

func TestWithDiskWait_AJobStillUnderTheFloorWhenTheWaitEndsIsRefused(t *testing.T) {
	t.Parallel()

	stub := &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 10 * gb, Readable: true}}}
	_, err := host.WithDiskWait(stub, 50, 20*time.Millisecond, time.Millisecond).Submit(t.Context(), model.JobSpec{Name: "gate-p"})

	require.ErrorIs(t, err, host.ErrDiskLow)
	require.ErrorContains(t, err, "10 GB free, floor 50 GB")
	assert.Zero(t, stub.submits)
}

func TestWithDiskWait_AnUnreadableDiskIsRefusedAtOnceWithoutWaiting(t *testing.T) {
	t.Parallel()

	stub := &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 900 * gb}}}
	started := time.Now()
	_, err := host.WithDiskWait(stub, 50, time.Hour, time.Hour).Submit(t.Context(), model.JobSpec{Name: "gate-p"})

	require.ErrorIs(t, err, host.ErrDiskRead)
	assert.Less(t, time.Since(started), time.Minute)
	assert.Zero(t, stub.submits)
}

func TestWithDiskWait_ACanceledContextEndsTheWait(t *testing.T) {
	t.Parallel()

	stub := &stubHost{status: model.HostStatus{Disk: model.DiskReading{FreeBytes: 10 * gb, Readable: true}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := host.WithDiskWait(stub, 50, time.Hour, time.Hour).Submit(ctx, model.JobSpec{Name: "gate-p"})

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, stub.submits)
}

func TestWithDiskFloor_EveryOtherMethodIsTheMethodOfTheHost(t *testing.T) {
	t.Parallel()

	stub := &stubHost{state: model.JobSucceeded}
	wrapped := host.WithDiskFloor(stub, 50)

	state, err := wrapped.Wait(t.Context(), "1")
	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Zero(t, stub.submits)
}

// tearing is a stubHost with a Teardown, as the providers have.
type tearing struct {
	*stubHost

	teardowns int
}

func (h *tearing) Teardown(context.Context) error {
	h.teardowns++

	return nil
}

func TestDiskGuards_KeepTheTeardownOfAHostThatHasOne(t *testing.T) {
	t.Parallel()

	type keeper interface {
		Teardown(ctx context.Context) error
	}

	for name, wrap := range map[string]func(host.Host) host.Host{
		"floor": func(h host.Host) host.Host { return host.WithDiskFloor(h, 50) },
		"wait":  func(h host.Host) host.Host { return host.WithDiskWait(h, 50, time.Hour, time.Hour) },
	} {
		inner := &tearing{stubHost: &stubHost{}}
		wrapped, ok := wrap(inner).(keeper)

		require.True(t, ok, "%s: a wrapped provider keeps its Teardown, or host_teardown cannot bank its work", name)
		require.NoError(t, wrapped.Teardown(t.Context()), name)
		assert.Equal(t, 1, inner.teardowns, name)

		_, ok = wrap(&stubHost{}).(keeper)
		assert.False(t, ok, "%s: a host with no Teardown gets none, so a caller still sees it cannot bank", name)
	}
}

func TestWaitJob(t *testing.T) {
	t.Parallel()

	for _, state := range []model.JobState{model.JobSucceeded, model.JobFailed, model.JobCanceled} {
		got, err := host.WaitJob(t.Context(), &stubHost{state: state}, "7")

		require.NoError(t, err)
		assert.Equal(t, state, got)
	}
}

func TestWaitJob_RefusesAStateThatIsNotFinal(t *testing.T) {
	t.Parallel()

	for _, state := range []model.JobState{model.JobRunning, model.JobPending, model.JobUnknown, ""} {
		got, err := host.WaitJob(t.Context(), &stubHost{state: state}, "7")

		require.ErrorIs(t, err, host.ErrJobState, string(state))
		assert.Equal(t, model.JobUnknown, got)
	}
}

func TestWaitJob_ReturnsTheErrorOfTheWait(t *testing.T) {
	t.Parallel()

	got, err := host.WaitJob(t.Context(), &stubHost{state: model.JobSucceeded, waitErr: errDown}, "7")

	require.ErrorIs(t, err, errDown)
	assert.Equal(t, model.JobSucceeded, got, "the state comes back with the error")
}

// emptyRepo answers git for a repository with no branch, tag, stash entry or worktree commit.
func emptyRepo() *proctest.Scripted {
	git := &proctest.Scripted{}
	git.Plan("", "for-each-ref")
	git.Plan("", "worktree", "list")
	git.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")

	return git
}

func TestTeardown_StopsAHostWithNoUnbankedWork(t *testing.T) {
	t.Parallel()

	find := &proctest.Scripted{}
	find.Plan("/work/a/.git\x00", "/work")
	h := &stubHost{root: "/work"}

	require.NoError(t, host.Teardown(t.Context(), h, emptyRepo(), find, privateURL))
	assert.Equal(t, 1, h.downs)
}

func TestTeardown_ScansTheWorkRootOfTheHost(t *testing.T) {
	t.Parallel()

	find := &proctest.Scripted{}
	find.Plan("/srv/lane/a/.git\x00", "/srv/lane")
	h := &stubHost{root: "/srv/lane"}

	require.NoError(t, host.Teardown(t.Context(), h, emptyRepo(), find, privateURL))
	assert.True(t, find.Called("-H", "/srv/lane"), "the scan starts at the root the host names")
}

func TestTeardown_KeepsTheHostUpWhenItsWorkRootIsUnknown(t *testing.T) {
	t.Parallel()

	h := &stubHost{rootErr: errDown}

	err := host.Teardown(t.Context(), h, emptyRepo(), &proctest.Scripted{}, privateURL)

	require.ErrorIs(t, err, errDown)
	require.ErrorContains(t, err, "host stays up")
	assert.Zero(t, h.downs)
}

func TestTeardown_KeepsTheHostUpWhenTheGuardsRefuse(t *testing.T) {
	t.Parallel()

	noRepos := &proctest.Scripted{}
	noRepos.Plan("\x00", "/work")

	unbanked := &proctest.Scripted{}
	unbanked.Plan("/work/a/.git\x00", "/work")

	tests := map[string]struct {
		git, find *proctest.Scripted
		want      error
	}{
		"no_repository":  {emptyRepo(), noRepos, host.ErrNoRepos},
		"unbanked_work":  {scriptedRepo(), unbanked, host.ErrUnbanked},
		"scan_cannot_go": {emptyRepo(), proctest.Always(model.CommandResult{ExitCode: 1, Stderr: "denied"}), nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := &stubHost{root: "/work"}
			err := host.Teardown(t.Context(), h, tc.git, tc.find, "")

			require.ErrorContains(t, err, "host stays up")

			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}

			assert.Zero(t, h.downs, "work that is not banked keeps the host up")
		})
	}
}

func TestTeardown_ReturnsTheErrorOfDown(t *testing.T) {
	t.Parallel()

	find := &proctest.Scripted{}
	find.Plan("/work/a/.git\x00", "/work")

	err := host.Teardown(t.Context(), downFails{&stubHost{root: "/work"}}, emptyRepo(), find, privateURL)

	require.ErrorIs(t, err, errDown)
}

// downKeeps is a host whose Down returns no error and does nothing, as a provider whose delete call is lost does.
type downKeeps struct{ *stubHost }

func (d downKeeps) Down(context.Context) error {
	d.downs++

	return nil
}

func TestTeardown_AuditsThatTheHostIsGoneAfterDown(t *testing.T) {
	t.Parallel()

	find := &proctest.Scripted{}
	find.Plan("/work/a/.git\x00", "/work")

	kept := &stubHost{root: "/work", status: model.HostStatus{State: model.HostUp}}
	err := host.Teardown(t.Context(), downKeeps{kept}, emptyRepo(), find, privateURL)

	require.ErrorIs(t, err, host.ErrNotDeleted)
	require.ErrorContains(t, err, "up")
}

func TestTeardown_AHostThatCannotBeReadAfterDownIsNotConfirmedDeleted(t *testing.T) {
	t.Parallel()

	find := &proctest.Scripted{}
	find.Plan("/work/a/.git\x00", "/work")

	err := host.Teardown(t.Context(), downKeeps{&stubHost{root: "/work", statusErr: errDown}}, emptyRepo(), find, privateURL)

	require.ErrorIs(t, err, host.ErrNotDeleted)
	require.ErrorIs(t, err, errDown)
}

// downFails is a host whose Down fails. Every other method does nothing.
type downFails struct{ *stubHost }

func (downFails) Down(context.Context) error { return errDown }
