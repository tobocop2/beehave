// Package ledger keeps one row for each lane of each pull request and counts them.
// The rows are a SQLite file with a table for the lanes and a table for the issues the first review recorded.
// Add writes in one BEGIN IMMEDIATE transaction, so two writers never lose a row.
package ledger

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	// The driver registers itself under the name sqlite, as it does for the workflow store.
	"modernc.org/sqlite"

	"github.com/tobocop2/obee/examples/remediation/tools/check"
)

// access is how a connection opens the file.
type access int

const (
	reading    access = iota // read-only: writes nothing, works in a folder that cannot be written
	recovering               // read-write without the right to make the file: rolls a hot journal back
	writing                  // makes the file if it is missing and starts transactions with BEGIN IMMEDIATE
)

// readOnlyRollback is the extended result code SQLITE_READONLY_ROLLBACK (8 | 2<<8): the file has a hot journal and the connection may not roll it back.
const readOnlyRollback = 776

const (
	// FileName is the ledger file in the state folder.
	FileName = "lane-ledger.db"

	fileMode      fs.FileMode = 0o600
	busyTimeoutMs             = 30000
	fixKinds                  = "fix|rework|relay"
	blanks                    = " \t\n\r\f\v"
	day                       = 24 * time.Hour
	noIssue                   = "-"
)

// The first write of a transaction makes the tables, so a reader never finds one table without the other.
const schema = `
CREATE TABLE IF NOT EXISTS lanes (
	key TEXT NOT NULL, lane TEXT NOT NULL, kind TEXT NOT NULL, at INTEGER NOT NULL,
	words TEXT NOT NULL DEFAULT '', mechanism TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '', round INTEGER NOT NULL DEFAULT 0,
	UNIQUE (key, lane)
);
CREATE TABLE IF NOT EXISTS closes (key TEXT NOT NULL, issue TEXT NOT NULL);
`

// codePattern matches the numeric codes that the driver puts after its messages, such as " (5) (SQLITE_BUSY)".
var codePattern = regexp.MustCompile(`\s\(\d+\)(\s\(SQLITE_[A-Z_0-9]+\))?`)

// driverWords says in plain words what a driver message that a caller can meet means. Another message stays as the driver wrote it.
func driverWords(text string) string {
	switch text {
	case "database is locked":
		return "the ledger is locked by another process"
	case "unable to open database file":
		return "the file cannot be opened"
	case "file is not a database":
		return "the file is not a database"
	}

	return text
}

var (
	tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	issuePattern = regexp.MustCompile(`^[0-9]+$`)
)

// Kind is what a lane does.
type Kind string

// The kinds of lane. A fix lane is a fix, a rework or a relay.
const (
	KindFix    Kind = "fix"
	KindRework Kind = "rework"
	KindRelay  Kind = "relay"
	KindReview Kind = "review"
	KindDesign Kind = "design"
	KindPapers Kind = "papers"
	// KindOwnerAdd is the approval of the owner to keep one more issue in a pull request. Its Lane is the issue number.
	KindOwnerAdd Kind = "owner-add"
)

const kindList = "fix, rework, relay, review, design, papers or owner-add"

// Kinds lists every kind of lane.
func Kinds() []Kind {
	return []Kind{KindFix, KindRework, KindRelay, KindReview, KindDesign, KindPapers, KindOwnerAdd}
}

// ParseKind returns the kind that text names.
func ParseKind(text string) (Kind, error) {
	for _, kind := range Kinds() {
		if string(kind) == text {
			return kind, nil
		}
	}

	return "", refuse("'%s' is not a lane kind. Use %s.", text, kindList)
}

func (k Kind) fixLane() bool {
	return k == KindFix || k == KindRework || k == KindRelay
}

// Request is one lane to record. Mechanism, Model and Round name the code path, the model and the round of the lane; the ledger stores them for any kind.
// Words belongs to an owner-add row, which names the issue in Lane and the words of the owner in Words.
// Closing returns the issue numbers that merging the pull request closes; the first review of a key records them.
// The caller gives the same read that the scope check uses, so the two lists agree.
type Request struct {
	Key       string
	Lane      string
	Kind      Kind
	Mechanism string
	Model     string
	Round     int
	Words     string
	At        time.Time
	Closing   func() ([]string, error)
}

// Added says what an Add did. Closes holds the issues the first review recorded.
type Added struct {
	FixLanes    int
	FirstReview bool
	Closes      []string
}

// Rounds is the count of patch rounds on a mechanism and whether the last one has no review yet.
type Rounds struct {
	Count int
	Open  bool
}

// Ledger reads and writes the rows of the lanes.
type Ledger struct {
	path   string
	busyMs int
}

// Open returns the ledger in the state folder dir. The file is made by the first Add.
func Open(dir string) *Ledger {
	return &Ledger{path: filepath.Join(dir, FileName), busyMs: busyTimeoutMs}
}

func refuse(format string, args ...any) error {
	return &check.Refusal{Message: fmt.Sprintf(format, args...)}
}

// laneRow is one lane row as the reads see it.
type laneRow struct {
	lane, kind, mechanism string
	at                    time.Time
}

// connect opens the file. A writer makes the file, and the tables in its first transaction; a reader opens the file read-only and writes nothing, so it works in a folder that cannot be written.
// The journal is SQLite's default rollback journal: the ledger is small and written rarely, and the rollback journal needs no shared memory file.
// Each connection waits for a lock; a writer starts a transaction with BEGIN IMMEDIATE.
// A reader that meets a hot journal (see readOnlyRollback) opens again with mode rw, which rolls the journal back and makes no file and no table.
func (l *Ledger) connect(access access) (*sql.DB, error) {
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", l.busyMs))

	switch access {
	case writing:
		if err := l.touch(); err != nil {
			return nil, err
		}

		query.Set("_txlock", "immediate")
	case recovering:
		query.Set("mode", "rw")
	case reading:
		query.Set("mode", "ro")
	}

	return sql.Open("sqlite", (&url.URL{Scheme: "file", Path: l.path, RawQuery: query.Encode()}).String())
}

// touch makes the file at mode 0600 if it is missing, so SQLite does not make it with a wider mode.
func (l *Ledger) touch() error {
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return err
	}

	return file.Close()
}

// fail returns a refusal that names the file and the cause, or nil for a nil error.
func (l *Ledger) fail(verb string, err error) error {
	if err == nil {
		return nil
	}

	var pathErr *fs.PathError

	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}

	return refuse("cannot %s %s: %s", verb, l.path, driverWords(codePattern.ReplaceAllString(err.Error(), "")))
}

// read runs a query on the file. A file that does not exist has no rows.
func (l *Ledger) read(query string, args []any, scan func(*sql.Rows) error) error {
	ctx := context.Background()

	info, err := os.Stat(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		// A missing file has no rows; a missing state folder is a mistake in the caller's path.
		_, err = os.Stat(filepath.Dir(l.path))

		return l.fail("read", err)
	}

	// A file with no bytes is a ledger that a writer has made and not yet filled: it has no rows.
	if err == nil && info.Size() == 0 {
		return nil
	}

	return l.fail("read", l.scanReading(ctx, query, args, scan))
}

// errCrashed is the refusal when a crash left a hot journal and the folder does not let a read-write connection roll it back.
var errCrashed = errors.New("a crash left the ledger half written, and the folder cannot be written to repair it")

// scanReading runs the query on a read-only connection. A crash can leave a journal that a read-only connection cannot roll back:
// the read then runs once more on a read-write connection, which rolls it back. If that fails too, the folder cannot be written.
func (l *Ledger) scanReading(ctx context.Context, query string, args []any, scan func(*sql.Rows) error) error {
	err := l.scanWith(ctx, reading, query, args, scan)
	if !hotJournal(err) {
		return err
	}

	if l.scanWith(ctx, recovering, query, args, scan) != nil {
		return errCrashed
	}

	return nil
}

// scanWith opens the file with the access, runs the query on it and closes it.
func (l *Ledger) scanWith(ctx context.Context, access access, query string, args []any, scan func(*sql.Rows) error) error {
	db, err := l.connect(access)
	if err == nil {
		err = errors.Join(each(ctx, db, query, args, scan), db.Close())
	}

	return err
}

// hotJournal says whether the error is the refusal of SQLite to read a file that needs a rollback it may not do.
func hotJournal(err error) bool {
	var driverErr *sqlite.Error

	return errors.As(err, &driverErr) && driverErr.Code() == readOnlyRollback
}

// each calls scan for every row the query finds and stops at the first error.
func each(ctx context.Context, db *sql.DB, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}

	for err == nil && rows.Next() {
		err = scan(rows)
	}

	return errors.Join(err, rows.Err(), rows.Close())
}

func keyOK(key string) error {
	if tokenPattern.MatchString(key) {
		return nil
	}

	return refuse("'%s' is not a pull request key. Use the number, or the item's name: letters, digits, dot, dash and underscore.", key)
}

func (r Request) check() error {
	if err := keyOK(r.Key); err != nil {
		return err
	}

	if err := r.checkLane(); err != nil {
		return err
	}

	return r.checkColumns()
}

// checkLane checks the lane name and the kind. The lane of an owner-add row is an issue number with the words of the owner.
func (r Request) checkLane() error {
	if r.Lane == "" || r.Kind == "" {
		return refuse("name the lane and its kind.")
	}

	if strings.ContainsAny(r.Lane, blanks) {
		return refuse("the lane name '%s' holds a blank.", r.Lane)
	}

	if _, err := ParseKind(string(r.Kind)); err != nil {
		return err
	}

	if r.Kind == KindOwnerAdd {
		return r.checkApproval()
	}

	return nil
}

// checkApproval checks the owner-add row: the lane is an issue number, and the words fit on one line.
func (r Request) checkApproval() error {
	if !issuePattern.MatchString(r.Lane) {
		return refuse("the issue '%s' is not a number. Use digits only.", r.Lane)
	}

	if r.Words == "" {
		return refuse("name the words of the owner for issue %s.", r.Lane)
	}

	if strings.ContainsAny(r.Words, "\t\r\n") {
		return refuse("the words of the owner hold a tab or a line break. Write them on one line.")
	}

	return nil
}

func (r Request) checkColumns() error {
	if r.At.IsZero() {
		return refuse("name the time of the lane. A row with no time counts days from the year 1.")
	}

	if r.Mechanism != "" {
		if err := mechanismOK(r.Mechanism); err != nil {
			return err
		}
	}

	if r.Model != "" && !tokenPattern.MatchString(r.Model) {
		return refuse("the lane model '%s' is not one token. Use letters, digits, dot, dash and underscore.", r.Model)
	}

	if r.Round < 0 {
		return refuse("the round %d is below zero. Use the number of the round the lane started in.", r.Round)
	}

	return nil
}

func mechanismOK(mechanism string) error {
	if tokenPattern.MatchString(mechanism) {
		return nil
	}

	return refuse("the mechanism '%s' is not one token. Name the code path in letters, digits, dot, dash and underscore, such as robots-classifier.", mechanism)
}

// Add records the lane unless its key already has a row for it. The first review of a key also records the issues its pull request closes.
// The lane row and the closes go in one transaction, so a reader sees both or neither.
func (l *Ledger) Add(request Request) (Added, error) {
	if err := request.check(); err != nil {
		return Added{}, err
	}

	// The issues are read before the transaction, so a slow forge holds no lock.
	numbers, first, err := l.firstReview(request)
	if err != nil {
		return Added{}, err
	}

	ctx := context.Background()

	db, err := l.connect(writing)
	if err != nil {
		return Added{}, l.fail("open", err)
	}

	added, err := l.insert(ctx, db, request, numbers, first)

	return added, l.fail("write", errors.Join(err, db.Close()))
}

// insert writes the lane row and, for a first review that is still first, its closes.
func (l *Ledger) insert(ctx context.Context, db *sql.DB, request Request, numbers []string, first bool) (Added, error) {
	var added Added

	tx, err := db.BeginTx(ctx, nil)
	if err == nil {
		added, err = write(ctx, tx, request, numbers, first)
		if err == nil {
			err = tx.Commit()
		} else {
			err = errors.Join(err, tx.Rollback())
		}
	}

	if err != nil {
		return Added{}, err
	}

	return added, nil
}

// steps runs statements in one transaction and keeps the first error, so a failed statement skips the rest.
type steps struct {
	ctx context.Context //nolint:containedctx // the value lives for one transaction
	tx  *sql.Tx
	err error
}

func (s *steps) exec(query string, args ...any) {
	if s.err == nil {
		_, s.err = s.tx.ExecContext(s.ctx, query, args...)
	}
}

func (s *steps) count(query string, args ...any) (found int) {
	if s.err == nil {
		s.err = s.tx.QueryRowContext(s.ctx, query, args...).Scan(&found)
	}

	return found
}

func write(ctx context.Context, tx *sql.Tx, request Request, numbers []string, first bool) (Added, error) {
	var added Added

	run := &steps{ctx: ctx, tx: tx}
	run.exec(schema)
	run.exec(`INSERT OR IGNORE INTO lanes (key, lane, kind, at, words, mechanism, model, round) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		request.Key, request.Lane, string(request.Kind), request.At.Truncate(time.Minute).Unix(), request.Words, request.Mechanism, request.Model, request.Round)
	added.FixLanes = run.count(`SELECT count(*) FROM lanes WHERE key = ? AND kind IN ('fix', 'rework', 'relay')`, request.Key)

	// Another writer may have recorded the closes since the issues were read; the first one to commit wins.
	if first && run.count(`SELECT count(*) FROM closes WHERE key = ?`, request.Key) == 0 {
		for _, issue := range orNoIssue(numbers) {
			run.exec(`INSERT INTO closes (key, issue) VALUES (?, ?)`, request.Key, issue)
		}

		added.FirstReview, added.Closes = true, numbers
	}

	return added, run.err
}

// orNoIssue returns the issues, or the one mark that says the first review closed none.
func orNoIssue(numbers []string) []string {
	if len(numbers) == 0 {
		return []string{noIssue}
	}

	return numbers
}

// firstReview reads the issues the pull request closes when the request is the first review of its key. It writes nothing.
func (l *Ledger) firstReview(request Request) ([]string, bool, error) {
	if request.Kind != KindReview {
		return nil, false, nil
	}

	seen, err := l.Closes(request.Key)
	if err != nil || len(seen) > 0 {
		return nil, false, err
	}

	numbers, err := request.closing()

	return numbers, err == nil, err
}

// closing returns the issue numbers that the pull request of the request closes.
func (r Request) closing() ([]string, error) {
	if r.Closing == nil {
		return nil, refuse("the first review of '%s' needs the issues the pull request closes.", r.Key)
	}

	return r.Closing()
}

// tally counts the rows whose kind is one of kinds, written as a list with a bar between kinds.
// The match is on the kind with a bar on each side.
func tally(rows []laneRow, kinds string) int {
	wanted := "|" + kinds + "|"
	count := 0

	for _, row := range rows {
		if strings.Contains(wanted, "|"+row.kind+"|") {
			count++
		}
	}

	return count
}

// Count returns the rows of key whose kind is in kinds, a list with a bar between kinds. An empty list means the fix lanes.
func (l *Ledger) Count(key, kinds string) (int, error) {
	rows, err := l.keyRows(key)

	return tally(rows, cmp.Or(kinds, fixKinds)), err
}

// keyRows reads the rows of key in the order they were written. A read sees a whole transaction or none of it.
func (l *Ledger) keyRows(key string) ([]laneRow, error) {
	if err := keyOK(key); err != nil {
		return nil, err
	}

	var rows []laneRow

	err := l.read(`SELECT lane, kind, mechanism, at FROM lanes WHERE key = ? ORDER BY rowid`, []any{key}, func(found *sql.Rows) error {
		var (
			row  laneRow
			unix int64
		)

		err := found.Scan(&row.lane, &row.kind, &row.mechanism, &unix)
		row.at = time.Unix(unix, 0).UTC()
		rows = append(rows, row)

		return err
	})

	return rows, err
}

// Rounds returns the patch rounds on a mechanism since the last design row of key. A round is the fix and rework rows on the mechanism between two review rows.
func (l *Ledger) Rounds(key, mechanism string) (Rounds, error) {
	if err := mechanismOK(mechanism); err != nil {
		return Rounds{}, err
	}

	rows, err := l.keyRows(key)

	var got Rounds

	for _, row := range rows {
		got = advance(got, row, mechanism)
	}

	return got, err
}

func advance(got Rounds, row laneRow, mechanism string) Rounds {
	switch row.kind {
	case string(KindDesign):
		return Rounds{}
	case string(KindReview):
		got.Open = false
	case string(KindFix), string(KindRework):
		if row.mechanism == mechanism {
			if !got.Open {
				got.Count++
			}

			got.Open = true
		}
	}

	return got
}

// Prior returns the fix lanes of key other than lane, so a caller knows whether lane is a successor.
func (l *Ledger) Prior(key, lane string) (int, error) {
	if lane == "" {
		return 0, refuse("name the lane to compare with.")
	}

	rows, err := l.keyRows(key)
	count := 0

	for _, row := range rows {
		if row.lane != lane && Kind(row.kind).fixLane() {
			count++
		}
	}

	return count, err
}

// Closes returns the issues the first review of key recorded, or "-" when it recorded none.
func (l *Ledger) Closes(key string) ([]string, error) {
	if err := keyOK(key); err != nil {
		return nil, err
	}

	var issues []string

	err := l.read(`SELECT issue FROM closes WHERE key = ? ORDER BY rowid`, []any{key}, func(found *sql.Rows) error {
		var issue string

		err := found.Scan(&issue)
		issues = append(issues, issue)

		return err
	})

	return issues, err
}

// OwnerAdds returns the issues the owner approved for key.
func (l *Ledger) OwnerAdds(key string) ([]string, error) {
	rows, err := l.keyRows(key)

	var issues []string

	for _, row := range rows {
		if Kind(row.kind) == KindOwnerAdd {
			issues = append(issues, row.lane)
		}
	}

	return issues, err
}

// Since returns the whole days from the latest row of the kind under key to now, and whether the key has such a row.
func (l *Ledger) Since(key string, kind Kind, now time.Time) (int, bool, error) {
	rows, err := l.keyRows(key)
	if err != nil {
		return 0, false, err
	}

	var latest time.Time

	found := false

	for _, row := range rows {
		if Kind(row.kind) == kind && (!found || row.at.After(latest)) {
			latest, found = row.at, true
		}
	}

	if !found {
		return 0, false, nil
	}

	return max(0, int(now.Sub(latest)/day)), true, nil
}
