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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/tobocop2/beehave/examples/remediation/tools/ledger"
)

// The tests below run the ledger in separate operating system processes. The test binary starts itself as the helper:
// TestHelperProcess does nothing unless the parent passed a mode after "--", then it does the job that the arguments name.
// The helper that holds a transaction tells the parent with a file in the ledger folder, and the parent releases it the same way.
const (
	readyFile   = "hold-ready"
	releaseFile = "hold-release"
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
	}
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

	require.NoError(t, os.Chmod(h.dir, 0o555)) //nolint:gosec // a folder needs the execute bit
	t.Cleanup(func() {
		assert.NoError(t, os.Chmod(h.dir, 0o700)) //nolint:gosec // a folder needs the execute bit
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
		// Each add is a durable write that takes the file lock. The number of adds is small, so that a loaded host never holds a writer
		// past the 30 s busy wait of the ledger: the test checks that processes share the file, not how fast it takes writes.
		adds = 40
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

	require.NoError(t, os.Chmod(h.dir, 0o555)) //nolint:gosec // a folder needs the execute bit
	t.Cleanup(func() {
		assert.NoError(t, os.Chmod(h.dir, 0o700)) //nolint:gosec // a folder needs the execute bit
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
		require.EqualError(t, err, "cannot write "+h.file()+": the ledger is locked by another process")

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
