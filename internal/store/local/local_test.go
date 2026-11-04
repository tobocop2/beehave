package local_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/local"
	"github.com/tobocop2/obee/internal/store/storetest"
)

const (
	project  = "alpha"
	fileMode = os.FileMode(0o600)
	dirMode  = os.FileMode(0o700)
	database = "local.db"
)

// clock is the fake clock of a store under test.
type clock struct{ now time.Time }

func newClock() *clock { return &clock{now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time { return c.now }

func open(t *testing.T, dir, name string, clk *clock) *local.Store {
	t.Helper()

	s, err := local.Open(t.Context(), dir, name, clk.Now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	return s
}

// raw opens the database file of dir behind the back of the store.
func raw(t *testing.T, dir string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(dir, database))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	return db
}

func execRaw(t *testing.T, db *sql.DB, statement string) {
	t.Helper()

	_, err := db.ExecContext(t.Context(), statement)
	require.NoError(t, err)
}

func TestStore_ConformsToTracker(t *testing.T) {
	t.Parallel()

	storetest.TestTracker(t, func(t *testing.T, broken bool) store.Tracker {
		t.Helper()

		s := open(t, t.TempDir(), project, newClock())
		if broken {
			require.NoError(t, s.Close())

			return s
		}

		require.NoError(t, s.Add(t.Context(), storetest.ItemID, "A finding", "The body."))

		return s
	})
}

func TestStore_ConformsToClaimRows(t *testing.T) {
	t.Parallel()

	storetest.TestClaimRows(t, func(t *testing.T, broken bool) store.ClaimRows {
		t.Helper()

		s := open(t, t.TempDir(), project, newClock())
		if broken {
			require.NoError(t, s.Close())
		}

		return s
	})
}

func TestOpen_KeepsTheDatabaseAndItsSideFilesPrivate(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "state")
	s := open(t, dir, project, newClock())
	require.NoError(t, s.Add(t.Context(), "1", "A title", "A body"))
	require.NoError(t, s.TryHold(t.Context(), model.ClaimRow{Item: "1", Holder: "a"}))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, dirMode, info.Mode().Perm(), "state folder")

	for _, name := range []string{database, database + "-wal", database + "-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err, name)
		require.Equal(t, fileMode, info.Mode().Perm(), name)
	}
}

func TestOpen_KeepsTheDataAcrossOpens(t *testing.T) {
	t.Parallel()

	dir, clk := t.TempDir(), newClock()
	first, err := local.Open(t.Context(), dir, project, clk.Now)
	require.NoError(t, err)
	require.NoError(t, first.Add(t.Context(), "1", "A title", "A body"))
	require.NoError(t, first.Close())

	second := open(t, dir, project, clk)
	got, err := second.Item(t.Context(), "1")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"id": "1", "title": "A title", "body": "A body", "severity": "P?"}, got)
}

func TestOpen_Refuses(t *testing.T) {
	t.Parallel()

	t.Run("a_state_folder_that_is_a_file", func(t *testing.T) {
		t.Parallel()

		file := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(file, nil, fileMode))

		_, err := local.Open(t.Context(), file, project, newClock().Now)
		require.ErrorContains(t, err, "state folder")
	})

	t.Run("a_database_path_that_is_a_folder", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, database), dirMode))

		_, err := local.Open(t.Context(), dir, project, newClock().Now)
		require.ErrorContains(t, err, "create")
	})

	t.Run("a_file_that_is_no_database", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, database), []byte("this is not a database, this is text"), fileMode))

		_, err := local.Open(t.Context(), dir, project, newClock().Now)
		require.ErrorContains(t, err, "local store")
	})

	t.Run("a_schema_version_it_does_not_know", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		open(t, dir, project, newClock())
		execRaw(t, raw(t, dir), "PRAGMA user_version = 99")

		_, err := local.Open(t.Context(), dir, project, newClock().Now)
		require.ErrorContains(t, err, "schema version 99")
	})

	t.Run("a_file_whose_names_clash_with_the_schema", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		db := raw(t, dir)
		execRaw(t, db, "CREATE TABLE other(x)")
		execRaw(t, db, "CREATE INDEX items ON other(x)")

		_, err := local.Open(t.Context(), dir, project, newClock().Now)
		require.ErrorContains(t, err, "local store")
	})
}

func TestAdd_Refuses(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ id, title, want string }{
		"an_empty_id":    {"", "A title", "needs an id"},
		"an_empty_title": {"1", "", "needs a title"},
		"a_duplicate_id": {"1", "Another title", "item 1 already exists in project alpha"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := open(t, t.TempDir(), project, newClock())
			require.NoError(t, s.Add(t.Context(), "1", "A title", ""))

			err := s.Add(t.Context(), tt.id, tt.title, "")
			require.ErrorContains(t, err, tt.want)

			var refusal *store.Error
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, store.CodeRefused, refusal.Code)
		})
	}
}

func TestAdd_KeepsTheOriginalOnADuplicate(t *testing.T) {
	t.Parallel()

	s := open(t, t.TempDir(), project, newClock())
	require.NoError(t, s.Add(t.Context(), "1", "First", "first body"))
	require.Error(t, s.Add(t.Context(), "1", "Second", "second body"))

	got, err := s.Item(t.Context(), "1")
	require.NoError(t, err)
	require.Equal(t, "First", got["title"])
	require.Equal(t, "first body", got["body"])
}

func TestItem_NamesTheMissingItem(t *testing.T) {
	t.Parallel()

	s := open(t, t.TempDir(), project, newClock())

	_, err := s.Item(t.Context(), "9")
	require.EqualError(t, err, "no item 9 in project alpha. Add it: obee item add")

	var refusal *store.Error
	require.ErrorAs(t, err, &refusal)
}

func TestOpenItems_ListsTheOpenItemsOfTheProject(t *testing.T) {
	t.Parallel()

	dir, clk := t.TempDir(), newClock()
	alpha, beta := open(t, dir, "alpha", clk), open(t, dir, "beta", clk)

	got, err := alpha.OpenItems(t.Context())
	require.NoError(t, err)
	require.Empty(t, got)

	require.NoError(t, alpha.Add(t.Context(), "b", "Second filed first", ""))
	require.NoError(t, alpha.Add(t.Context(), "a", "First filed second", ""))
	require.NoError(t, beta.Add(t.Context(), "c", "Other project", ""))
	require.NoError(t, alpha.Add(t.Context(), "x", "Closed", ""))
	execRaw(t, raw(t, dir), "UPDATE items SET state = 'closed' WHERE id = 'x'")

	got, err = alpha.OpenItems(t.Context())
	require.NoError(t, err)
	require.Equal(t, []model.Item{
		{ID: "b", Severity: model.SeverityUnknown, Title: "Second filed first"},
		{ID: "a", Severity: model.SeverityUnknown, Title: "First filed second"},
	}, got)

	_, err = beta.Item(t.Context(), "a")
	require.Error(t, err)
}

func TestClaims_AreOfOneProject(t *testing.T) {
	t.Parallel()

	dir, clk := t.TempDir(), newClock()
	alpha, beta := open(t, dir, "alpha", clk), open(t, dir, "beta", clk)

	require.NoError(t, alpha.TryHold(t.Context(), model.ClaimRow{Item: "1", Holder: "a"}))
	require.NoError(t, beta.TryHold(t.Context(), model.ClaimRow{Item: "1", Holder: "b"}))

	held, err := alpha.HeldRows(t.Context())
	require.NoError(t, err)
	require.Equal(t, "a", held[0].Holder)
	require.Len(t, held, 1)

	require.ErrorIs(t, beta.Beat(t.Context(), "a"), model.ErrNoClaim)
}

func TestTryHold_StampsTheLeaseAndRefusesBlankRows(t *testing.T) {
	t.Parallel()

	clk := newClock()
	s := open(t, t.TempDir(), project, clk)

	require.NoError(t, s.TryHold(t.Context(), model.ClaimRow{Item: "1", Holder: "a"}))

	held, err := s.HeldRows(t.Context())
	require.NoError(t, err)
	require.Equal(t, []model.ClaimRow{{Item: "1", Holder: "a", LeaseEnds: clk.now.Add(model.ClaimLease)}}, held)

	var refusal *store.Error

	require.ErrorAs(t, s.TryHold(t.Context(), model.ClaimRow{Item: "", Holder: "a"}), &refusal)
	require.ErrorAs(t, s.TryHold(t.Context(), model.ClaimRow{Item: "2", Holder: ""}), &refusal)
}

func TestBeat_MovesEveryLeaseOfTheHolder(t *testing.T) {
	t.Parallel()

	clk := newClock()
	start := clk.now
	s := open(t, t.TempDir(), project, clk)

	require.NoError(t, s.TryHold(t.Context(), model.ClaimRow{Item: "1", Holder: "a"}))
	require.NoError(t, s.TryHold(t.Context(), model.ClaimRow{Item: "2", Holder: "a"}))
	require.NoError(t, s.TryHold(t.Context(), model.ClaimRow{Item: "3", Holder: "b"}))

	clk.now = start.Add(4 * time.Minute)
	require.NoError(t, s.Beat(t.Context(), "a"))

	held, err := s.HeldRows(t.Context())
	require.NoError(t, err)
	require.Equal(t, []model.ClaimRow{
		{Item: "1", Holder: "a", LeaseEnds: clk.now.Add(model.ClaimLease)},
		{Item: "2", Holder: "a", LeaseEnds: clk.now.Add(model.ClaimLease)},
		{Item: "3", Holder: "b", LeaseEnds: start.Add(model.ClaimLease)},
	}, held)
}

func TestBeat_OfAnEmptyHolderIsNoClaim(t *testing.T) {
	t.Parallel()

	s := open(t, t.TempDir(), project, newClock())
	require.NoError(t, s.TryHold(t.Context(), model.ClaimRow{Item: "1", Holder: "a"}))
	require.NoError(t, s.Free(t.Context(), model.ClaimRow{Item: "1", Holder: "a"}))

	require.ErrorIs(t, s.Beat(t.Context(), ""), model.ErrNoClaim)
}

func TestReclaim_FreesARowOnlyWhenItsLeaseEnded(t *testing.T) {
	t.Parallel()

	clk := newClock()
	start := clk.now
	s := open(t, t.TempDir(), project, clk)

	require.NoError(t, s.TryHold(t.Context(), model.ClaimRow{Item: "1", Holder: "a"}))

	clk.now = start.Add(model.ClaimLease - time.Second)
	freed, err := s.Reclaim(t.Context(), "1")
	require.NoError(t, err)
	require.False(t, freed)

	clk.now = start.Add(model.ClaimLease)
	freed, err = s.Reclaim(t.Context(), "1")
	require.NoError(t, err)
	require.True(t, freed)

	freed, err = s.Reclaim(t.Context(), "1")
	require.NoError(t, err)
	require.False(t, freed, "a free row has no lease to reclaim")

	freed, err = s.Reclaim(t.Context(), "missing")
	require.NoError(t, err)
	require.False(t, freed)

	require.NoError(t, s.TryHold(t.Context(), model.ClaimRow{Item: "1", Holder: "b"}))

	held, err := s.HeldRows(t.Context())
	require.NoError(t, err)
	require.Equal(t, "b", held[0].Holder)
}

func TestFree_OfAMissingRowIsNotAnError(t *testing.T) {
	t.Parallel()

	s := open(t, t.TempDir(), project, newClock())
	require.NoError(t, s.Free(t.Context(), model.ClaimRow{Item: "missing", Holder: "a"}))
}

func TestTryHold_HasOneWinnerAcrossStores(t *testing.T) {
	t.Parallel()

	const contenders = 8

	dir, clk := t.TempDir(), newClock()
	stores := make([]*local.Store, contenders)

	for i := range stores {
		stores[i] = open(t, dir, project, clk)
	}

	var wg sync.WaitGroup

	errs := make([]error, contenders)

	for i, s := range stores {
		wg.Go(func() {
			errs[i] = s.TryHold(context.Background(), model.ClaimRow{Item: "1", Holder: "holder-" + strconv.Itoa(i)})
		})
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	held, err := stores[0].HeldRows(t.Context())
	require.NoError(t, err)
	require.Len(t, held, 1)
}

func TestMethods_RefuseWhenTheTablesAreGone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := open(t, dir, project, newClock())
	db := raw(t, dir)
	execRaw(t, db, "DROP TABLE claims")
	execRaw(t, db, "DROP TABLE items")

	ctx := t.Context()
	row := model.ClaimRow{Item: "1", Holder: "a"}

	require.Error(t, s.Add(ctx, "1", "A title", ""))
	require.Error(t, s.TryHold(ctx, row))
	require.Error(t, s.Beat(ctx, "a"))
	require.Error(t, s.Free(ctx, row))

	_, err := s.Reclaim(ctx, "1")
	require.Error(t, err)

	_, err = s.Item(ctx, "1")
	require.Error(t, err)

	_, err = s.OpenItems(ctx)
	require.Error(t, err)

	_, err = s.HeldRows(ctx)
	require.Error(t, err)
}

func TestMethods_RefuseWhenAWriteFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := open(t, dir, project, newClock())
	row := model.ClaimRow{Item: "1", Holder: "a"}
	require.NoError(t, s.TryHold(t.Context(), row))

	execRaw(t, raw(t, dir), "CREATE TRIGGER no_update BEFORE UPDATE ON claims BEGIN SELECT RAISE(ABORT, 'no update'); END")

	require.ErrorContains(t, s.Free(t.Context(), row), "no update")
	require.ErrorContains(t, s.TryHold(t.Context(), model.ClaimRow{Item: "2", Holder: "b"}), "no update")
}

func TestHeldRows_RefusesARowItCannotRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := open(t, dir, project, newClock())
	execRaw(t, raw(t, dir), "INSERT INTO claims(project, item, holder, lease_ends) VALUES ('alpha', '1', 'a', 'not a time')")

	_, err := s.HeldRows(t.Context())
	require.ErrorContains(t, err, "local store")
}
