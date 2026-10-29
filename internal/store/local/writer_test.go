package local_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/local"
	"github.com/tobocop2/obee/internal/store/storetest"
)

var _ store.Writer = (*local.Store)(nil)

// probe reads the file behind the back of the store.
type probe struct {
	t  *testing.T
	db *sql.DB
}

func (p probe) count(query string, args ...any) int {
	p.t.Helper()

	var n int
	require.NoError(p.t, p.db.QueryRowContext(p.t.Context(), query, args...).Scan(&n))

	return n
}

func (p probe) Items() int { return p.count(`SELECT count(*) FROM item_keys`) }

func (p probe) Status(id string) store.Status {
	p.t.Helper()

	var state string
	require.NoError(p.t, p.db.QueryRowContext(p.t.Context(), `SELECT state FROM items WHERE id = ?`, id).Scan(&state))

	return store.Status(state)
}

func (p probe) Comments(id string) int {
	return p.count(`SELECT count(*) FROM comments WHERE item = ?`, id)
}

func newWriter(t *testing.T) (*local.Store, probe) {
	t.Helper()

	dir := t.TempDir()
	s := open(t, dir, project, newClock())

	return s, probe{t: t, db: raw(t, dir)}
}

func TestWriter_FollowsTheContract(t *testing.T) {
	t.Parallel()

	s, p := newWriter(t)
	storetest.CheckWriter(t, s, p)
}

func TestWriter_AFailedFileIsAnErrorEverywhere(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)
	require.NoError(t, s.Close())
	storetest.CheckWriterFails(t, s)
}

func TestCreate_FilesAnOpenItemTheTrackerReads(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)

	id, created, err := s.Create(t.Context(), "run:a", store.NewItem{Title: "plan", Body: "the body", Parent: "epic"})
	require.NoError(t, err)
	assert.True(t, created)

	fields, err := s.Item(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"id": id, "title": "plan", "body": "the body"}, fields)

	items, err := s.OpenItems(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "epic", items[0].Parent)
	assert.False(t, items[0].Hold)
}

func TestCreate_NumbersTheNewIdsPastTheIdsInUse(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)
	require.NoError(t, s.Add(t.Context(), project+"-2", "taken", ""))

	id, _, err := s.Create(t.Context(), "run:a", store.NewItem{Title: "plan"})
	require.NoError(t, err)
	assert.Equal(t, project+"-3", id, "one item makes the first try project-2, which is taken")
}

func TestCreate_RefusesAnEmptyTitleAndAnEmptyKey(t *testing.T) {
	t.Parallel()

	s, p := newWriter(t)

	_, _, err := s.Create(t.Context(), "run:a", store.NewItem{})
	require.Error(t, err)

	_, _, err = s.Create(t.Context(), "", store.NewItem{Title: "plan"})
	require.Error(t, err)
	assert.Zero(t, p.Items())
}

func TestCreate_TwoWritersWithOneKeyFileOneItem(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, b := open(t, dir, project, newClock()), open(t, dir, project, newClock())
	ids := make(chan string, 2)

	for _, s := range []*local.Store{a, b} {
		go func() {
			id, _, err := s.Create(t.Context(), "run:a", store.NewItem{Title: "plan"})
			assert.NoError(t, err)
			ids <- id
		}()
	}

	first, second := <-ids, <-ids
	assert.Equal(t, first, second)
	assert.Equal(t, 1, probe{t: t, db: raw(t, dir)}.Items())
}

func TestCreate_AFileThatCannotTakeARowIsAnErrorAndFilesNothing(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ damage, parent string }{
		"items table is gone":      {`DROP TABLE items`, ""},
		"item insert is refused":   {`CREATE TRIGGER no BEFORE INSERT ON items BEGIN SELECT RAISE(ABORT, 'no'); END`, ""},
		"key insert is refused":    {`CREATE TRIGGER no BEFORE INSERT ON item_keys BEGIN SELECT RAISE(ABORT, 'no'); END`, ""},
		"parent insert is refused": {`CREATE TRIGGER no BEFORE INSERT ON item_links BEGIN SELECT RAISE(ABORT, 'no'); END`, "epic"},
		"key table is gone":        {`DROP TABLE item_keys`, ""},
	} {
		s, p := newWriter(t)
		execRaw(t, p.db, tc.damage)

		_, _, err := s.Create(t.Context(), "run:a", store.NewItem{Title: "plan", Parent: tc.parent})
		require.Error(t, err, name)
	}

	s, p := newWriter(t)
	execRaw(t, p.db, `DROP TABLE items`)
	require.Error(t, s.Comment(t.Context(), "x", "k", "t"))
}

func TestCreate_ARefusedInsertRollsTheItemBack(t *testing.T) {
	t.Parallel()

	s, p := newWriter(t)
	execRaw(t, p.db, `CREATE TRIGGER no BEFORE INSERT ON item_keys BEGIN SELECT RAISE(ABORT, 'no'); END`)

	_, _, err := s.Create(t.Context(), "run:a", store.NewItem{Title: "plan"})
	require.Error(t, err)
	assert.Zero(t, p.count(`SELECT count(*) FROM items`))
}

func TestSetStatus_AFileThatCannotBeWrittenIsAnError(t *testing.T) {
	t.Parallel()

	s, p := newWriter(t)
	execRaw(t, p.db, `DROP TABLE items`)
	require.Error(t, s.SetStatus(t.Context(), "x", store.StatusOpen))
}

func TestSetStatus_HoldShowsAsHeldInTheQueueAndClosedLeavesIt(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)
	id, _, err := s.Create(t.Context(), "run:a", store.NewItem{Title: "plan"})
	require.NoError(t, err)

	require.NoError(t, s.SetStatus(t.Context(), id, store.StatusHold))

	items, err := s.OpenItems(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.True(t, items[0].Hold)

	require.NoError(t, s.SetStatus(t.Context(), id, store.StatusClosed))

	items, err = s.OpenItems(t.Context())
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestSetStatus_AnItemTheProjectLacksIsARefusal(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)
	err := s.SetStatus(t.Context(), "nope", store.StatusClosed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no item nope")
}

func TestComment_AnItemTheProjectLacksIsARefusal(t *testing.T) {
	t.Parallel()

	s, _ := newWriter(t)
	err := s.Comment(t.Context(), "nope", "k", "text")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no item nope")
}
