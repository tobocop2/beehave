package storetest_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/storetest"
)

var errDown = errors.New("down")

// recorder is a Reporter that keeps the failures instead of failing the test.
type recorder struct {
	failures []string
}

func (*recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// memTracker is a correct in-memory tracker. Flaws switch on one wrong behavior each.
type memTracker struct {
	store.Tracker

	rows        map[string]model.ClaimRow
	down        bool
	overwrite   bool
	keepFree    bool
	freeAny     bool
	beatAny     bool
	reclaimLive bool
	noLease     bool
}

func newMem() *memTracker {
	return &memTracker{rows: map[string]model.ClaimRow{}}
}

func (m *memTracker) err() error {
	if m.down {
		return errDown
	}

	return nil
}

func (m *memTracker) HeldRows(context.Context) ([]model.ClaimRow, error) {
	var rows []model.ClaimRow
	for _, row := range m.rows {
		rows = append(rows, row)
	}

	return rows, m.err()
}

func (m *memTracker) Beat(_ context.Context, holder string) error {
	for _, row := range m.rows {
		if row.Holder == holder {
			return m.err()
		}
	}

	if m.beatAny {
		return m.err()
	}

	return errors.Join(model.ErrNoClaim, m.err())
}

func (m *memTracker) TryHold(_ context.Context, row model.ClaimRow) error {
	if _, held := m.rows[row.Item]; !held || m.overwrite {
		row.LeaseEnds = time.Unix(100, 0)
		if m.noLease {
			row.LeaseEnds = time.Time{}
		}

		m.rows[row.Item] = row
	}

	return m.err()
}

func (m *memTracker) Reclaim(_ context.Context, item string) (bool, error) {
	if _, found := m.rows[item]; found && m.reclaimLive {
		delete(m.rows, item)

		return true, m.err()
	}

	return false, m.err()
}

func (m *memTracker) Free(_ context.Context, row model.ClaimRow) error {
	current, found := m.rows[row.Item]
	if found && current.Holder != row.Holder && !m.freeAny {
		return model.ErrRowMoved
	}

	if !m.keepFree {
		delete(m.rows, row.Item)
	}

	return m.err()
}

func (m *memTracker) OpenItems(context.Context) ([]model.Item, error) { return nil, m.err() }

func (m *memTracker) Containers(context.Context) ([]string, error) { return nil, m.err() }

func (m *memTracker) Children(context.Context, string) ([]string, error) { return nil, m.err() }

func (m *memTracker) Show(context.Context, string) (string, error) { return "", m.err() }

func (m *memTracker) Close(context.Context, string, string) error { return m.err() }

func (m *memTracker) Create(context.Context, model.CreateRequest) (string, error) { return "", m.err() }

func TestCheckClaimRows_AcceptsACorrectTracker(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	storetest.CheckClaimRows(r, newMem())
	assert.Empty(t, r.failures)
}

func TestCheckClaimRows_FailsATrackerThatLetsASecondHolderOverwrite(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.overwrite = true

	r := &recorder{}
	storetest.CheckClaimRows(r, mem)
	assert.Contains(t, r.failures, "a second TryHold leaves the first holder: got [session-b], want [session-a]")
}

func TestCheckClaimRows_FailsATrackerWhoseFreeKeepsTheRow(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.keepFree = true

	r := &recorder{}
	storetest.CheckClaimRows(r, mem)
	assert.NotEmpty(t, r.failures)
}

func TestCheckClaimRows_FailsATrackerWhoseFreeClearsAnotherHoldersRow(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.freeAny = true

	r := &recorder{}
	storetest.CheckClaimRows(r, mem)
	assert.Contains(t, r.failures, "Free of a row that another holder holds returns ErrRowMoved: got false, want true")
}

func TestCheckClaimRows_FailsATrackerThatBreaksTheLeaseRules(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		flaw func(*memTracker)
		want string
	}{
		"beat_any":     {func(m *memTracker) { m.beatAny = true }, "Beat of a holder with no row returns ErrNoClaim: got false, want true"},
		"reclaim_live": {func(m *memTracker) { m.reclaimLive = true }, "Reclaim leaves a row whose lease has not ended: got true, want false"},
		"no_lease":     {func(m *memTracker) { m.noLease = true }, "a take carries the end of its lease: got true, want false"},
	}

	for name, tt := range tests {
		mem := newMem()
		tt.flaw(mem)

		r := &recorder{}
		storetest.CheckClaimRows(r, mem)
		assert.Contains(t, r.failures, tt.want, name)
	}
}

func TestCheckClaimRows_ReportsAnErrorOfATracker(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.down = true

	r := &recorder{}
	storetest.CheckClaimRows(r, mem)
	assert.Contains(t, r.failures, "HeldRows: unexpected error: down")
}

func TestCheckTrackerFails_AcceptsATrackerThatErrorsEverywhere(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.down = true

	r := &recorder{}
	storetest.CheckTrackerFails(r, mem, filepath.Join(t.TempDir(), "body.md"))
	assert.Empty(t, r.failures)
}

func TestCheckTrackerFails_FailsATrackerThatAnswersAfterAFailure(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	storetest.CheckTrackerFails(r, newMem(), filepath.Join(t.TempDir(), "body.md"))
	assert.Contains(t, r.failures, "Close: want an error from a failed command, got none")
}

func TestCheckTrackerFails_ReportsABodyFileThatCannotBeWritten(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	storetest.CheckTrackerFails(r, newMem(), filepath.Join(t.TempDir(), "missing", "body.md"))
	assert.NotEmpty(t, r.failures)
}

func TestCheckClaimRowsFail_AcceptsRowsThatErrorEverywhere(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.down = true

	r := &recorder{}
	storetest.CheckClaimRowsFail(r, mem)
	assert.Empty(t, r.failures)
}

func TestCheckClaimRowsFail_FailsRowsThatAnswerAfterAFailure(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	storetest.CheckClaimRowsFail(r, newMem())
	assert.Contains(t, r.failures, "Free: want an error from a failed command, got none")
}

func TestTestTracker_RunsTheSuiteOnAFactory(t *testing.T) {
	t.Parallel()

	storetest.TestTracker(t, func(t *testing.T, broken bool) store.Tracker {
		t.Helper()

		mem := newMem()
		mem.down = broken

		return mem
	})
}

func TestTestClaimRows_RunsTheSuiteOnAFactory(t *testing.T) {
	t.Parallel()

	storetest.TestClaimRows(t, func(t *testing.T, broken bool) store.ClaimRows {
		t.Helper()

		mem := newMem()
		mem.down = broken

		return mem
	})
}
