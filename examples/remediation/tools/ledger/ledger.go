// Package ledger keeps one row for each lane of each pull request and counts them.
// The rows are a TSV file with a sidecar for the issues the first review recorded. Add holds an exclusive lock and every read shares it.
package ledger

import (
	"cmp"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/examples/remediation/tools/check"
)

const (
	// FileName is the ledger file in the state folder.
	FileName = "lane-ledger.tsv"

	closesSuffix = ".closes"
	stampLayout  = "2006-01-02T15:04Z"
	fixKinds     = "fix|rework|relay"
	blanks       = " \t\n\r\f\v"
	day          = 24 * time.Hour
	noIssue      = "-"
)

// The columns of a row. The last three belong to a fix lane.
const (
	colKey = iota
	colLane
	colKind
	colWhen
	colMechanism
	colModel
	colRound
)

// colIssue is the issue number column of a row in the closes file.
const colIssue = 1

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

// Request is one lane to record. Mechanism, Model and Round belong to a fix lane and are ignored for another kind.
// Words belongs to an owner-add row, which names the issue in Lane and the words of the owner in Words.
// Body returns the pull request body; the first review of a key reads it for the issues it closes.
type Request struct {
	Key       string
	Lane      string
	Kind      Kind
	Mechanism string
	Model     string
	Round     int
	Words     string
	At        time.Time
	Body      func() (string, error)
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
	rows   Rows
	closes Rows
}

// New returns a ledger over a table of lane rows and a table of closed issues.
func New(rows, closes Rows) *Ledger {
	return &Ledger{rows: rows, closes: closes}
}

// Open returns the ledger in the state folder dir.
func Open(dir string) *Ledger {
	path := filepath.Join(dir, FileName)

	return New(NewTable(path), NewTable(path+closesSuffix))
}

func refuse(format string, args ...any) error {
	return &check.Refusal{Message: fmt.Sprintf(format, args...)}
}

func col(row []string, index int) string {
	if index < len(row) {
		return row[index]
	}

	return ""
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

// line is the row of the request. Only a fix lane writes the lane columns, and the empty ones at the end are left out.
func (r Request) line() string {
	cells := []string{r.Key, r.Lane, string(r.Kind), r.At.UTC().Format(stampLayout)}

	if r.Kind == KindOwnerAdd {
		cells = append(cells, r.Words)
	}

	if r.Kind.fixLane() {
		round := ""
		if r.Round > 0 {
			round = strconv.Itoa(r.Round)
		}

		cells = append(cells, r.Mechanism, r.Model, round)

		for len(cells) > colMechanism && cells[len(cells)-1] == "" {
			cells = cells[:len(cells)-1]
		}
	}

	return strings.Join(cells, "\t") + "\n"
}

// Add records the lane unless its key already has a row for it. The first review of a key also records the issues its pull request body closes.
func (l *Ledger) Add(request Request) (Added, error) {
	if err := request.check(); err != nil {
		return Added{}, err
	}

	var added Added

	err := l.rows.Hold(func() error {
		var err error

		added, err = l.write(request)

		return err
	})

	return added, err
}

// write appends the lane row, and then publishes the closes of a first review. A retry after a failed publish
// finds the lane row and no closes, so it publishes them again and returns them.
func (l *Ledger) write(request Request) (Added, error) {
	added, closes, err := l.firstReview(request)
	if err != nil {
		return added, err
	}

	added.FixLanes, err = l.appendLane(request)
	if err != nil || closes == "" {
		return added, err
	}

	return added, l.closes.Publish(closes)
}

// appendLane writes the row unless the key has one for the lane. It returns the fix lanes of the key.
func (l *Ledger) appendLane(request Request) (int, error) {
	rows, err := l.rows.List()
	if err != nil {
		return 0, err
	}

	lanes := tally(rows, request.Key, fixKinds)
	if hasLane(rows, request) {
		return lanes, nil
	}

	if request.Kind.fixLane() {
		lanes++
	}

	return lanes, l.rows.Append(request.line())
}

func hasLane(rows [][]string, request Request) bool {
	return slices.ContainsFunc(rows, func(row []string) bool {
		return col(row, colKey) == request.Key && col(row, colLane) == request.Lane
	})
}

// firstReview reads the issues the body closes when the request is the first review of its key. It returns them and the text of their rows.
// It writes nothing.
func (l *Ledger) firstReview(request Request) (Added, string, error) {
	if request.Kind != KindReview {
		return Added{}, "", nil
	}

	seen, err := l.closesOf(request.Key)
	if err != nil || slices.ContainsFunc(seen, func(issue string) bool { return issue != "" }) {
		return Added{}, "", err
	}

	numbers, err := request.closing()
	if err != nil {
		return Added{}, "", err
	}

	return Added{FirstReview: true, Closes: numbers}, closesText(request.Key, numbers), nil
}

// closing returns the issue numbers that the pull request body of the request closes.
func (r Request) closing() ([]string, error) {
	if r.Body == nil {
		return nil, refuse("the first review of '%s' needs the pull request body.", r.Key)
	}

	text, err := r.Body()

	return ClosingNumbers(text), err
}

func closesText(key string, numbers []string) string {
	if len(numbers) == 0 {
		return key + "\t" + noIssue + "\n"
	}

	var text strings.Builder

	for _, number := range numbers {
		text.WriteString(key + "\t" + number + "\n")
	}

	return text.String()
}

// tally counts the rows of key whose kind is one of kinds, written as a list with a bar between kinds.
// The match is on the kind with a bar on each side, so a list that ends in a bar also matches a row with no kind.
func tally(rows [][]string, key, kinds string) int {
	wanted := "|" + kinds + "|"
	count := 0

	for _, row := range rows {
		if col(row, colKey) == key && strings.Contains(wanted, "|"+col(row, colKind)+"|") {
			count++
		}
	}

	return count
}

// Count returns the rows of key whose kind is in kinds, a list with a bar between kinds. An empty list means the fix lanes.
func (l *Ledger) Count(key, kinds string) (int, error) {
	rows, err := l.keyRows(key)

	return tally(rows, key, cmp.Or(kinds, fixKinds)), err
}

// keyRows reads every row under a shared lock, so a read never sees part of a write.
func (l *Ledger) keyRows(key string) ([][]string, error) {
	if err := keyOK(key); err != nil {
		return nil, err
	}

	var rows [][]string

	err := l.rows.Share(func() error {
		var err error

		rows, err = l.rows.List()

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
		if col(row, colKey) == key {
			got = advance(got, row, mechanism)
		}
	}

	return got, err
}

func advance(got Rounds, row []string, mechanism string) Rounds {
	switch col(row, colKind) {
	case string(KindDesign):
		return Rounds{}
	case string(KindReview):
		got.Open = false
	case string(KindFix), string(KindRework):
		if col(row, colMechanism) == mechanism {
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
		if col(row, colKey) == key && col(row, colLane) != lane && Kind(col(row, colKind)).fixLane() {
			count++
		}
	}

	return count, err
}

// Closes returns the issues the first review of key recorded. It reads under a shared lock, so it never sees part of a write.
func (l *Ledger) Closes(key string) ([]string, error) {
	var issues []string

	err := l.rows.Share(func() error {
		var err error

		issues, err = l.closesOf(key)

		return err
	})

	return issues, err
}

// closesOf is Closes with no lock: the caller holds the ledger or shares it.
func (l *Ledger) closesOf(key string) ([]string, error) {
	if err := keyOK(key); err != nil {
		return nil, err
	}

	rows, err := l.closes.List()

	var issues []string

	for _, row := range rows {
		if col(row, colKey) == key {
			issues = append(issues, col(row, colIssue))
		}
	}

	return issues, err
}

// OwnerAdds returns the issues the owner approved for key.
func (l *Ledger) OwnerAdds(key string) ([]string, error) {
	rows, err := l.keyRows(key)

	var issues []string

	for _, row := range rows {
		if col(row, colKey) == key && Kind(col(row, colKind)) == KindOwnerAdd {
			issues = append(issues, col(row, colLane))
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

	latest, found, err := latestOf(rows, key, kind)
	if err != nil || !found {
		return 0, false, err
	}

	return max(0, int(now.Sub(latest)/day)), true, nil
}

func latestOf(rows [][]string, key string, kind Kind) (time.Time, bool, error) {
	var latest time.Time

	found := false

	for _, row := range rows {
		if col(row, colKey) != key || Kind(col(row, colKind)) != kind {
			continue
		}

		when, err := time.Parse(stampLayout, col(row, colWhen))
		if err != nil {
			return latest, false, refuse("the time '%s' of a %s row of '%s' is not a UTC stamp such as 2026-10-10T12:30Z.", col(row, colWhen), kind, key)
		}

		if !found || when.After(latest) {
			latest, found = when, true
		}
	}

	return latest, found, nil
}
