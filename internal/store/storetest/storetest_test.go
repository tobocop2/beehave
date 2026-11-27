package storetest_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/ifacetest"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/store"
	"github.com/tobocop2/beehest/internal/store/storetest"
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
	fields      []string
	dropped     string
	extra       bool
	wrongID     bool
	// ignoresLabel makes ItemList answer every item whatever label it is asked for. noItem leaves the item ItemID out of the list.
	ignoresLabel bool
	noItem       bool
	// failsOnLabel makes ItemList fail when it is asked for a label.
	failsOnLabel bool
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

// ItemList serves item 5 with the label round and item 6 with none.
func (m *memTracker) ItemList(_ context.Context, label string) ([]model.Item, error) {
	if err := m.err(); err != nil {
		return nil, err
	}

	if label != "" && m.failsOnLabel {
		return nil, errDown
	}

	var kept []model.Item

	for _, one := range []model.Item{{ID: storetest.ItemID, Labels: []string{"round"}}, {ID: "6"}} {
		if one.ID == storetest.ItemID && m.noItem {
			continue
		}

		if label == "" || m.ignoresLabel || slices.Contains(one.Labels, label) {
			kept = append(kept, one)
		}
	}

	return kept, nil
}

func (m *memTracker) ItemFields() []string {
	if m.fields != nil {
		return m.fields
	}

	return []string{"id", "title", "body"}
}

func (m *memTracker) Item(context.Context, string) (map[string]string, error) {
	if err := m.err(); err != nil {
		return nil, err
	}

	got := map[string]string{}

	for _, name := range m.ItemFields() {
		got[name] = "value of " + name
	}

	got["id"] = storetest.ItemID
	if m.wrongID {
		got["id"] = "other"
	}

	delete(got, m.dropped)

	if m.extra {
		got["undeclared"] = "x"
	}

	return got, nil
}

func TestItemList_ReturnsTheItemsOfALabel(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	storetest.CheckItemList(r, newMem())
	assert.Empty(t, r.failures)

	mem := newMem()
	mem.ignoresLabel = true

	r = &recorder{}
	storetest.CheckItemList(r, mem)
	assert.Equal(t, []string{
		"ItemList of the label round: got [5 6], want [5]",
		"ItemList of a label that no item carries: got [5 6], want []",
	}, r.failures)

	mem = newMem()
	mem.noItem = true

	r = &recorder{}
	storetest.CheckItemList(r, mem)
	assert.Equal(t, []string{`ItemList of no label: want the item "5" among [6]`}, r.failures)

	mem = newMem()
	mem.down = true

	r = &recorder{}
	storetest.CheckItemList(r, mem)
	assert.Equal(t, []string{"ItemList: unexpected error: down"}, r.failures)

	mem = newMem()
	mem.failsOnLabel = true

	r = &recorder{}
	storetest.CheckItemList(r, mem)
	assert.Equal(t, []string{"ItemList: unexpected error: down", "ItemList: unexpected error: down"}, r.failures)
}

func TestCheckItem_AcceptsATrackerThatReturnsTheDeclaredFields(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	storetest.CheckItem(r, newMem())
	assert.Empty(t, r.failures)
}

func TestCheckItem_FailsATrackerThatDropsADeclaredField(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.dropped = "body"

	r := &recorder{}
	storetest.CheckItem(r, mem)
	assert.Equal(t, []string{`Item: want a value for the declared field "body", got map[id:5 title:value of title]`}, r.failures)
}

func TestCheckItem_FailsATrackerThatReturnsAnUndeclaredField(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.extra = true

	r := &recorder{}
	storetest.CheckItem(r, mem)
	assert.Equal(t, []string{`Item: got the field "undeclared", which ItemFields does not declare ([id title body])`}, r.failures)
}

func TestCheckItem_FailsATrackerThatReturnsAnotherID(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.wrongID = true

	r := &recorder{}
	storetest.CheckItem(r, mem)
	assert.Equal(t, []string{`Item: want id "5", got "other"`}, r.failures)
}

func TestCheckItem_ReportsAnErrorOfATracker(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.down = true

	r := &recorder{}
	storetest.CheckItem(r, mem)
	assert.Equal(t, []string{"Item: unexpected error: down"}, r.failures)
}

func TestCheckItemFields_AcceptsATrackerThatDeclaresIDTitleAndBody(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	storetest.CheckItemFields(r, newMem())
	assert.Empty(t, r.failures)
}

func TestCheckItemFields_FailsATrackerThatLeavesOutAField(t *testing.T) {
	t.Parallel()

	mem := newMem()
	mem.fields = []string{"id", "title"}

	r := &recorder{}
	storetest.CheckItemFields(r, mem)
	assert.Equal(t, []string{`ItemFields: want "body" among the declared fields, got [id title]`}, r.failures)
}

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
	storetest.CheckTrackerFails(r, mem)
	assert.Empty(t, r.failures)
}

func TestCheckTrackerFails_FailsATrackerThatAnswersAfterAFailure(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	storetest.CheckTrackerFails(r, newMem())
	assert.Contains(t, r.failures, "ItemList: want an error from a failed command, got none")
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

// memReader serves the reader rules. empty answers an empty list and an empty parent and no error.
type memReader struct{ empty bool }

func (m memReader) Comments(context.Context, string) ([]string, error) {
	if m.empty {
		return nil, nil
	}

	return nil, errDown
}

func (m memReader) Parent(context.Context, string) (string, error) {
	if m.empty {
		return "", nil
	}

	return "", errDown
}

func TestCheckReaderFails_AcceptsAReaderThatErrorsAndFailsOneThatReadsAnErrorAsEmpty(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	storetest.CheckReaderFails(rec, memReader{})
	assert.Empty(t, rec.failures)

	rec = &recorder{}
	storetest.CheckReaderFails(rec, memReader{empty: true})
	assert.Equal(t, []string{
		"Comments: want an error from a failed command, got none",
		"Parent: want an error from a failed command, got none",
	}, rec.failures)
}

// TestFailedCommandMethods_CoverEveryMethodOfTheStoreInterfaces fails when a method of the store interfaces has no failed-command case.
func TestFailedCommandMethods_CoverEveryMethodOfTheStoreInterfaces(t *testing.T) {
	t.Parallel()

	ran := map[string]bool{}
	for _, name := range storetest.FailedCommandMethods() {
		ran[name] = true
	}

	declared, err := ifacetest.Methods([]string{"../tracker.go", "../writer.go"}, "Tracker", "Reader", "ClaimRows", "Writer")
	require.NoError(t, err)

	for _, name := range declared {
		if name != "ItemFields" {
			assert.True(t, ran[name], "store method %s has no failed-command case", name)
		}
	}

	for name := range ran {
		assert.Contains(t, declared, name, "a failed-command case runs %s, which no store interface has", name)
	}
}
