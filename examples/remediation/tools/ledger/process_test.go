package ledger_test

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/tobocop2/beehest/examples/remediation/tools/check"
	"github.com/tobocop2/beehest/examples/remediation/tools/ledger"
	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/lockfile"
)

// The tests below run the ledger in separate operating system processes. The test binary starts itself as the helper:
// TestHelperProcess does nothing unless the parent passed a mode after "--", then it does the job that the arguments name.
// The helper that holds a transaction tells the parent with a file in the ledger folder, and the parent releases it the same way.
const (
	readyFile   = "hold-ready"
	releaseFile = "hold-release"
	outcomeFile = "try-outcome"
)

func TestHelperProcess(t *testing.T) {
	t.Parallel()

	args := flag.Args()
	if len(args) == 0 {
		t.Skip("helper for the process tests")
	}

	switch args[0] {
	case "add":
		require.NoError(t, helperAdd(args[1], args[2], args[3]))
	case "hold":
		require.NoError(t, helperHold(t.Context(), args[1]))
	case "crash":
		require.NoError(t, helperCrash(t.Context(), args[1]))
	case "hold-lock":
		require.NoError(t, helperHoldLock(args[1]))
	case "try-write":
		require.NoError(t, helperTryWrite(t.Context(), args[1]))
	}
}

// helperTryWrite writes one row without waiting for a lock and records "committed", or the error of SQLite when the commit is refused, in the outcome file.
func helperTryWrite(ctx context.Context, dir string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, ledger.FileName)+"?_pragma=busy_timeout(0)")
	if err != nil {
		return err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}

	_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	if err == nil {
		_, err = conn.ExecContext(ctx, "INSERT INTO lanes (key, lane, kind, at) VALUES ('try', 'try-1', 'fix', 0)")
	}

	if err == nil {
		_, err = conn.ExecContext(ctx, "COMMIT")
	}

	outcome := "committed"
	if err != nil {
		outcome = "refused: " + err.Error()
	}

	return os.WriteFile(filepath.Join(dir, outcomeFile), []byte(outcome), 0o600)
}

// tryWrite runs the helper that writes one row without waiting, and returns what it says: "committed", or "refused: " and the error.
func tryWrite(t *testing.T, dir string) string {
	t.Helper()

	require.NoError(t, helper(t, "try-write", dir).Run())

	outcome, err := os.ReadFile(filepath.Join(dir, outcomeFile)) //nolint:gosec // the test names its own outcome file
	require.NoError(t, err)

	return string(outcome)
}

func TestTouch_AnExistingFileKeepsTheReadLockOfAReaderInTheSameProcess(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("5", "fix-1", ledger.KindFix, "")

	// A read transaction that has run a query holds a shared lock on the file until it ends.
	db, err := sql.Open("sqlite", "file:"+h.file()+"?mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	reader, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)

	var rows int

	require.NoError(t, reader.QueryRowContext(t.Context(), "SELECT count(*) FROM lanes").Scan(&rows))

	// The step of a write that makes the file runs while the reader holds its lock.
	require.NoError(t, ledger.Touch(h.ledger))

	assert.Contains(t, tryWrite(t, h.dir), "refused: database is locked", "the write of the other process must wait for the reader")

	require.NoError(t, reader.Rollback())
}

func TestAdd_MakingTheFileMakesItAtMode0600AndRefusesAFolderThatDoesNotExist(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, ledger.Touch(h.ledger))

	info, err := os.Stat(h.file())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.ErrorIs(t, ledger.Touch(ledger.Open(filepath.Join(h.dir, "gone"))), fs.ErrNotExist)
}

func TestAdd_AFolderThatCannotBeWrittenToRefusesTheAddThatMustMakeTheFile(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root writes to a folder of mode 0555")
	}

	h := newHarness(t)
	h.add("5", "fix-1", ledger.KindFix, "")

	// The lock file stays, so the add gets its turn; the ledger file is gone, so the add must make it.
	require.NoError(t, os.Remove(h.file()))
	require.NoError(t, exectest.ChmodFolder(h.dir, 0o555))
	t.Cleanup(func() {
		assert.NoError(t, exectest.ChmodFolder(h.dir, 0o700))
	})

	_, err := h.ledger.Add(ledger.Request{Key: "5", Lane: "fix-2", Kind: ledger.KindFix, At: at})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "cannot open "+h.file()+": ")
	assert.Contains(t, err.Error(), "permission denied")
	assert.NoFileExists(t, h.file())
}

func TestAdd_AWriteInAnotherProcessCommitsOnceTheReaderEnds(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("5", "fix-1", ledger.KindFix, "")

	assert.Equal(t, "committed", tryWrite(t, h.dir), "the helper commits when no reader holds the file")
}

// helperHoldLock takes the ledger lock, makes the ready file, and frees the lock when the release file exists.
func helperHoldLock(dir string) error {
	turn := lockfile.Open(filepath.Join(dir, ledger.LockName))
	if err := turn.Lock(); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(dir, readyFile), nil, 0o600); err != nil {
		return err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, releaseFile)); err == nil {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	return turn.Unlock()
}

// helperCrash starts a write transaction that is too big for the page cache, so SQLite writes the journal and part of the file.
// It makes the ready file from inside the transaction and then waits to be killed.
func helperCrash(ctx context.Context, dir string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, ledger.FileName)+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}

	for _, statement := range []string{"PRAGMA cache_size = 10", "BEGIN IMMEDIATE"} {
		if _, err = conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}

	for i := range 3000 {
		_, err = conn.ExecContext(ctx, "INSERT INTO lanes (key, lane, kind, at, words) VALUES ('crash', ?, 'fix', 0, ?)", "c"+strconv.Itoa(i), strings.Repeat("w", 200))
		if err != nil {
			return err
		}
	}

	if err = os.WriteFile(filepath.Join(dir, readyFile), nil, 0o600); err != nil {
		return err
	}

	<-ctx.Done()

	return errors.Join(conn.Close(), db.Close())
}

// crashed leaves a ledger with three committed rows and a hot journal: a helper process dies by SIGKILL inside a transaction.
func crashed(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)
	h.add("7", "fix-1", ledger.KindFix, "")
	h.add("7", "rev-1", ledger.KindReview, "")
	h.add("7", "des-1", ledger.KindDesign, "")

	cmd := helper(t, "crash", h.dir)
	require.NoError(t, cmd.Start())
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(h.dir, readyFile))

		return err == nil
	}, 30*time.Second, 5*time.Millisecond)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())

	_, err := os.Stat(h.file() + "-journal")
	require.NoError(t, err, "the killed helper must leave a hot journal")

	return h
}

func TestReads_AfterACrashThatLeavesAHotJournalAllReadsSucceedWithTheCommittedRows(t *testing.T) {
	t.Parallel()

	h := crashed(t)

	assert.Equal(t, 1, h.count("7", ""))
	assert.Equal(t, 1, h.count("7", "review"))
	assert.Zero(t, h.count("crash", ""))

	closes, err := h.ledger.Closes("7")
	require.NoError(t, err)
	assert.Equal(t, []string{"-"}, closes)

	_, found, err := h.ledger.Since("7", ledger.KindFix, at)
	require.NoError(t, err)
	assert.True(t, found)

	_, err = os.Stat(h.file() + "-journal")
	assert.ErrorIs(t, err, fs.ErrNotExist, "the retry rolled the journal back")
}

func TestReads_AHotJournalInAFolderThatCannotBeWrittenIsRefusedInWords(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root writes to a folder of mode 0555")
	}

	h := crashed(t)

	require.NoError(t, exectest.ChmodFolder(h.dir, 0o555))
	t.Cleanup(func() {
		assert.NoError(t, exectest.ChmodFolder(h.dir, 0o700))
	})

	_, err := h.ledger.Count("7", "")
	require.EqualError(t, err, "cannot read "+h.file()+": a crash left the ledger half written, and the folder cannot be written to repair it")
}

// helperAdd adds one lane of its own and one lane that every helper adds, as many times as count says.
func helperAdd(dir, id, count string) error {
	n, err := strconv.Atoi(count)
	if err != nil {
		return err
	}

	book := ledger.Open(dir)

	for i := range n {
		for _, lane := range []string{"own-" + id + "-" + strconv.Itoa(i), "shared-" + strconv.Itoa(i)} {
			_, err = book.Add(ledger.Request{Key: "9", Lane: lane, Kind: ledger.KindFix, At: at})
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// helperHold opens a write transaction, inserts ten lanes, makes the ready file, and commits when the release file exists.
func helperHold(ctx context.Context, dir string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, ledger.FileName)+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}

	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}

	for i := range 10 {
		_, err = conn.ExecContext(ctx, "INSERT INTO lanes (key, lane, kind, at) VALUES ('hold', ?, 'fix', 0)", "h"+strconv.Itoa(i))
		if err != nil {
			return err
		}
	}

	if err = os.WriteFile(filepath.Join(dir, readyFile), nil, 0o600); err != nil {
		return err
	}

	for {
		if _, err = os.Stat(filepath.Join(dir, releaseFile)); err == nil {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	_, err = conn.ExecContext(ctx, "COMMIT")

	return errors.Join(err, conn.Close(), db.Close())
}

func helper(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()

	return exec.CommandContext(t.Context(), os.Args[0], append([]string{"-test.run=^TestHelperProcess$", "--"}, args...)...) //nolint:gosec // the test starts its own binary
}

func TestAdd_FourProcessesAddingAtOnceGetNoErrorLoseNoRowAndDoubleNoRow(t *testing.T) {
	t.Parallel()

	const (
		processes = 4
		adds      = 300
	)

	h := newHarness(t)

	var group sync.WaitGroup

	outputs := make([]string, processes)
	failures := make([]error, processes)

	for id := range processes {
		group.Add(1)

		go func() {
			defer group.Done()

			out, err := helper(t, "add", h.dir, strconv.Itoa(id), strconv.Itoa(adds)).CombinedOutput()
			outputs[id], failures[id] = string(out), err
		}()
	}

	group.Wait()

	for id := range processes {
		require.NoError(t, failures[id], outputs[id])
	}

	// Each process adds its own lane and a lane the others add too: one row stays for each name.
	want := processes*adds + adds
	assert.Equal(t, want, h.count("9", ""))

	db, err := sql.Open("sqlite", h.file())
	require.NoError(t, err)

	var rows, names int

	err = db.QueryRowContext(t.Context(), "SELECT count(*), count(DISTINCT lane) FROM lanes WHERE key = '9'").Scan(&rows, &names)
	require.NoError(t, errors.Join(err, db.Close()))
	assert.Equal(t, want, rows)
	assert.Equal(t, want, names)
}

// settle is how long a test waits to show that an add has not landed. The proof of order is the lock, not this time:
// an add that ignores the lock lands within microseconds, so it fails the test long before this time ends.
const settle = 300 * time.Millisecond

// addInBackground adds one lane in a goroutine and returns the channel that gets the error of the add.
func addInBackground(h *harness, lane string) <-chan error {
	done := make(chan error, 1)

	go func() {
		_, err := h.ledger.Add(ledger.Request{Key: "5", Lane: lane, Kind: ledger.KindFix, At: at})
		done <- err
	}()

	return done
}

// requireWaiting fails the test if the add finished before the settle time.
func requireWaiting(t *testing.T, done <-chan error) {
	t.Helper()

	select {
	case err := <-done:
		require.FailNow(t, "the add did not wait for the ledger lock", "it returned %v", err)
	case <-time.After(settle):
	}
}

// requireLands waits up to 30 s for the add and fails the test if it errs or does not return.
func requireLands(t *testing.T, done <-chan error) {
	t.Helper()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		require.FailNow(t, "the add did not land after the lock was freed")
	}
}

func TestAdd_AnAddWaitsWhileTheLedgerLockIsHeldAndLandsWhenItIsReleased(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	turn := lockfile.Open(filepath.Join(h.dir, ledger.LockName))
	require.NoError(t, turn.Lock())

	done := addInBackground(h, "fix-1")
	requireWaiting(t, done)
	assert.Zero(t, h.count("5", ""), "the row landed while the lock was held")

	require.NoError(t, turn.Unlock())
	requireLands(t, done)
	assert.Equal(t, 1, h.count("5", ""))
}

func TestAdd_AnAddInAnotherProcessWaitsForTheLedgerLockAndLandsWhenItIsReleased(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	cmd := helper(t, "hold-lock", h.dir)
	require.NoError(t, cmd.Start())
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(h.dir, readyFile))

		return err == nil
	}, 30*time.Second, 5*time.Millisecond)

	done := addInBackground(h, "fix-1")
	requireWaiting(t, done)
	assert.Zero(t, h.count("5", ""), "the row landed while the other process held the lock")

	require.NoError(t, os.WriteFile(filepath.Join(h.dir, releaseFile), nil, 0o600))
	require.NoError(t, cmd.Wait())
	requireLands(t, done)
	assert.Equal(t, 1, h.count("5", ""))
}

func TestAdd_TheLedgerLockFileIsMadeAtMode0600(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("5", "fix-1", ledger.KindFix, "")

	info, err := os.Stat(filepath.Join(h.dir, ledger.LockName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestAdd_ARowThatLandedIsReportedWhenTheLedgerLockCannotBeFreed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ledger.FreeWith(h.ledger, func(turn *flock.Flock) error {
		require.NoError(t, turn.Unlock())

		return errors.New("bad file descriptor")
	})

	added, err := h.ledger.Add(ledger.Request{Key: "5", Lane: "fix-1", Kind: ledger.KindFix, At: at})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "the lane is recorded in "+h.file()+", but the lock file cannot be freed: bad file descriptor.")
	assert.Equal(t, 1, added.FixLanes, "the result of the write stays with the error")
	assert.Equal(t, 1, h.count("5", ""), "the row landed")
}

func TestAdd_AProcessThatDiesHoldingTheLedgerLockLetsTheNextAddLand(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	cmd := helper(t, "hold-lock", h.dir)
	require.NoError(t, cmd.Start())
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(h.dir, readyFile))

		return err == nil
	}, 30*time.Second, 5*time.Millisecond)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())

	requireLands(t, addInBackground(h, "fix-1"))
	assert.Equal(t, 1, h.count("5", ""))
}

func TestReads_AReadIsNotHeldByTheLedgerLock(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("5", "fix-1", ledger.KindFix, "")

	turn := lockfile.Open(filepath.Join(h.dir, ledger.LockName))
	require.NoError(t, turn.Lock())

	counted := make(chan int, 1)

	go func() {
		n, err := h.ledger.Count("5", "")
		if err != nil {
			n = -1
		}

		counted <- n
	}()

	select {
	case n := <-counted:
		assert.Equal(t, 1, n)
	case <-time.After(30 * time.Second):
		assert.Fail(t, "the read waited for the ledger lock")
	}

	require.NoError(t, turn.Unlock())
}

func TestReads_AReaderBesideAWriterInAnotherProcessSeesNoneOrAllOfItsTransaction(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("0", "seed", ledger.KindPapers, "")

	cmd := helper(t, "hold", h.dir)
	require.NoError(t, cmd.Start())
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(h.dir, readyFile))

		return err == nil
	}, 30*time.Second, 5*time.Millisecond)

	// The helper holds the transaction open: a read finds none of its ten rows, and does not wait for it.
	assert.Zero(t, h.count("hold", ""))

	seen := make(chan []int, 1)
	stop := make(chan struct{})

	go func() {
		var counts []int

		for {
			n, err := h.ledger.Count("hold", "")
			if err != nil {
				n = -1
			}

			counts = append(counts, n)

			select {
			case <-stop:
				seen <- counts

				return
			default:
			}
		}
	}()

	require.NoError(t, os.WriteFile(filepath.Join(h.dir, releaseFile), nil, 0o600))
	require.NoError(t, cmd.Wait())
	close(stop)

	for _, n := range <-seen {
		assert.Contains(t, []int{0, 10}, n, "a read saw part of a transaction, or failed")
	}

	assert.Equal(t, 10, h.count("hold", ""))
}

func TestReads_AFolderThatCannotBeWrittenStillReadsItsLedger(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root writes to a folder of mode 0555")
	}

	h := newHarness(t)
	h.add("7", "fix-1", ledger.KindFix, "")
	h.add("7", "rev-1", ledger.KindReview, "")

	require.NoError(t, exectest.ChmodFolder(h.dir, 0o555))
	t.Cleanup(func() {
		assert.NoError(t, exectest.ChmodFolder(h.dir, 0o700))
	})

	assert.Equal(t, 1, h.count("7", ""))

	closes, err := h.ledger.Closes("7")
	require.NoError(t, err)
	assert.Equal(t, []string{"-"}, closes)

	_, found, err := h.ledger.Since("7", ledger.KindFix, at)
	require.NoError(t, err)
	assert.True(t, found)
}

func TestAdd_AModelAndARoundAreStoredWithTheLane(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.ledger.Add(ledger.Request{Key: "7", Lane: "fix-1", Kind: ledger.KindFix, Model: "sonnet-4.5", Round: 2, At: at})
	require.NoError(t, err)

	_, err = h.ledger.Add(ledger.Request{Key: "7", Lane: "rev-1", Kind: ledger.KindReview, At: at, Closing: body("")})
	require.NoError(t, err)

	db, err := sql.Open("sqlite", h.file())
	require.NoError(t, err)

	type stored struct {
		Lane, Model string
		Round       int
	}

	var got []stored

	rows, err := db.QueryContext(t.Context(), "SELECT lane, model, round FROM lanes WHERE key = '7' ORDER BY rowid")
	require.NoError(t, err)

	for rows.Next() {
		var row stored

		require.NoError(t, rows.Scan(&row.Lane, &row.Model, &row.Round))
		got = append(got, row)
	}

	require.NoError(t, errors.Join(rows.Err(), rows.Close(), db.Close()))
	assert.Equal(t, []stored{{"fix-1", "sonnet-4.5", 2}, {"rev-1", "", 0}}, got)
}

func TestErrors_AFileThatIsLockedUnreadableOrNotADatabaseIsNamedInWords(t *testing.T) {
	t.Parallel()

	t.Run("locked", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.add("7", "fix-1", ledger.KindFix, "")
		ledger.WaitAtMost(h.ledger, 50)

		db, err := sql.Open("sqlite", h.file())
		require.NoError(t, err)

		conn, err := db.Conn(t.Context())
		require.NoError(t, err)

		_, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
		require.NoError(t, err)

		_, err = h.ledger.Add(ledger.Request{Key: "7", Lane: "fix-2", Kind: ledger.KindFix, At: at})
		require.EqualError(t, err, "cannot write "+h.file()+": the ledger stayed locked by another process until the wait ended. Run the command again")

		require.NoError(t, errors.Join(conn.Close(), db.Close()))
	})

	t.Run("unreadable", func(t *testing.T) {
		t.Parallel()

		if os.Geteuid() == 0 {
			t.Skip("root reads a file of mode 0")
		}

		h := newHarness(t)
		h.add("7", "fix-1", ledger.KindFix, "")
		require.NoError(t, os.Chmod(h.file(), 0))

		_, err := h.ledger.Count("7", "")
		require.EqualError(t, err, "cannot read "+h.file()+": the file cannot be opened")
	})

	t.Run("not a database", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		require.NoError(t, os.WriteFile(h.file(), []byte("792\tfix-1\tfix\n"), 0o600))

		_, err := h.ledger.Count("7", "")
		require.EqualError(t, err, "cannot read "+h.file()+": the file is not a database")

		_, err = h.ledger.Add(ledger.Request{Key: "7", Lane: "fix-2", Kind: ledger.KindFix, At: at})
		require.EqualError(t, err, "cannot write "+h.file()+": the file is not a database")
	})
}
