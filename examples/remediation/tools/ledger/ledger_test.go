package ledger_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/tobocop2/obee/examples/remediation/tools/check"
	"github.com/tobocop2/obee/examples/remediation/tools/ledger"
)

var at = time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC)

type harness struct {
	t      *testing.T
	dir    string
	ledger *ledger.Ledger
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	dir := t.TempDir()

	return &harness{t: t, dir: dir, ledger: ledger.Open(dir)}
}

func (h *harness) file() string { return filepath.Join(h.dir, ledger.FileName) }

func (h *harness) add(key, lane string, kind ledger.Kind, mechanism string) ledger.Added {
	h.t.Helper()

	added, err := h.ledger.Add(ledger.Request{Key: key, Lane: lane, Kind: kind, Mechanism: mechanism, At: at, Body: body("")})
	require.NoError(h.t, err)

	return added
}

func (h *harness) count(key, kinds string) int {
	h.t.Helper()

	n, err := h.ledger.Count(key, kinds)
	require.NoError(h.t, err)

	return n
}

func (h *harness) rounds(key, mechanism string) ledger.Rounds {
	h.t.Helper()

	got, err := h.ledger.Rounds(key, mechanism)
	require.NoError(h.t, err)

	return got
}

func (h *harness) prior(key, lane string) int {
	h.t.Helper()

	n, err := h.ledger.Prior(key, lane)
	require.NoError(h.t, err)

	return n
}

func body(text string) func() (string, error) {
	return func() (string, error) { return text, nil }
}

func TestAdd_OneRowForEachLaneFixLanesCountedAndASecondAddAddsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	assert.Equal(t, ledger.Added{FixLanes: 1}, h.add("792", "fix-1", ledger.KindFix, ""))
	h.add("792", "fix-2", ledger.KindRework, "")
	h.add("792", "fix-3", ledger.KindRelay, "")
	h.add("792", "des-1", ledger.KindDesign, "")
	assert.Equal(t, ledger.Added{FixLanes: 3}, h.add("792", "fix-1", ledger.KindFix, ""))
	h.add("790", "other-1", ledger.KindFix, "")

	assert.Equal(t, 3, h.count("792", ""))
	assert.Equal(t, 1, h.count("792", "rework"))
	assert.Equal(t, 1, h.count("792", "design"))
	assert.Equal(t, 2, h.prior("792", "fix-3"))
	assert.Equal(t, 5, h.count("792", "fix|rework|relay|design")+h.count("790", ""))
}

func TestAdd_APapersRowIsRecordedAndCountsAsNoFixLane(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("792", "fix-1", ledger.KindFix, "")

	assert.Equal(t, ledger.Added{FixLanes: 1}, h.add("792", "pap-1", ledger.KindPapers, ""))
	assert.Equal(t, 1, h.count("792", ""))
	assert.Equal(t, 1, h.count("792", "papers"))
	assert.Equal(t, 0, h.prior("792", "fix-1"))
}

func TestAdd_ADesignRowIgnoresTheMechanismAndAReviewRowIsNotAFixLane(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	assert.Equal(t, ledger.Added{}, h.add("792", "des-1", ledger.KindDesign, "robots"))
	assert.Equal(t, ledger.Added{FirstReview: true}, h.add("792", "rev-1", ledger.KindReview, "robots"))
	assert.Equal(t, 0, h.count("792", ""))
	assert.Equal(t, ledger.Rounds{}, h.rounds("792", "robots"))
}

func TestAdd_ABadAddIsARefusalThatNamesTheFixAndWritesNothing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		request ledger.Request
		message string
	}{
		{"unknown_kind", ledger.Request{Key: "792", Lane: "fix-1", Kind: "survey"}, "'survey' is not a lane kind. Use fix, rework, relay, review, design, papers or owner-add."},
		{"key_with_a_blank", ledger.Request{Key: "a b", Lane: "fix-1", Kind: ledger.KindFix}, "'a b' is not a pull request key. Use the number, or the item's name: "},
		{"lane_with_a_blank", ledger.Request{Key: "792", Lane: "fix 1", Kind: ledger.KindFix}, "the lane name 'fix 1' holds a blank."},
		{"lane_with_a_tab", ledger.Request{Key: "792", Lane: "fix\t1", Kind: ledger.KindFix}, "the lane name 'fix\t1' holds a blank."},
		{"no_lane", ledger.Request{Key: "792", Kind: ledger.KindFix}, "name the lane and its kind."},
		{"no_kind", ledger.Request{Key: "792", Lane: "fix-1"}, "name the lane and its kind."},
		{"mechanism_with_a_blank", ledger.Request{Key: "792", Lane: "fix-1", Kind: ledger.KindFix, Mechanism: "a b"}, "the mechanism 'a b' is not one token."},
		{"mechanism_with_a_slash", ledger.Request{Key: "792", Lane: "fix-1", Kind: ledger.KindFix, Mechanism: "a/b"}, "the mechanism 'a/b' is not one token."},
		{"model_with_a_blank", ledger.Request{Key: "792", Lane: "fix-1", Kind: ledger.KindFix, Model: "a b"}, "the lane model 'a b' is not one token."},
		{"negative_round", ledger.Request{Key: "792", Lane: "fix-1", Kind: ledger.KindFix, Round: -1}, "the round -1 is below zero."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			tc.request.At = at
			_, err := h.ledger.Add(tc.request)

			var refusal *check.Refusal

			require.ErrorAs(t, err, &refusal)
			assert.Contains(t, err.Error(), tc.message)
			assert.NoFileExists(t, h.file())
		})
	}
}

func TestReads_ABadKeyOrArgumentIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	cases := []struct {
		name    string
		call    func() error
		message string
	}{
		{"count_slash_key", func() error { _, err := h.ledger.Count("a/b", ""); return err }, "'a/b' is not a pull request key"},
		{"count_empty_key", func() error { _, err := h.ledger.Count("", ""); return err }, "'' is not a pull request key"},
		{"rounds_empty_mechanism", func() error { _, err := h.ledger.Rounds("792", ""); return err }, "the mechanism '' is not one token"},
		{"rounds_blank_key", func() error { _, err := h.ledger.Rounds("a b", "m"); return err }, "'a b' is not a pull request key"},
		{"prior_no_lane", func() error { _, err := h.ledger.Prior("792", ""); return err }, "name the lane to compare with."},
		{"prior_slash_key", func() error { _, err := h.ledger.Prior("a/b", "x"); return err }, "'a/b' is not a pull request key"},
		{"closes_slash_key", func() error { _, err := h.ledger.Closes("a/b"); return err }, "'a/b' is not a pull request key"},
		{"since_slash_key", func() error { _, _, err := h.ledger.Since("a/b", ledger.KindReview, at); return err }, "'a/b' is not a pull request key"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var refusal *check.Refusal

			err := tc.call()
			require.ErrorAs(t, err, &refusal)
			assert.Contains(t, err.Error(), tc.message)
		})
	}
}

func TestRounds_CountPatchRoundsOnAMechanismSinceTheLastDesignRow(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	assert.Equal(t, ledger.Rounds{}, h.rounds("792", "robots"))

	h.add("792", "fix-1", ledger.KindFix, "robots")
	h.add("792", "rev-1", ledger.KindReview, "")
	h.add("792", "fix-2", ledger.KindRework, "robots")
	h.add("792", "fix-2b", ledger.KindFix, "robots")
	h.add("792", "fix-t", ledger.KindFix, "title")
	h.add("791", "fix-x", ledger.KindFix, "robots")

	assert.Equal(t, ledger.Rounds{Count: 2, Open: true}, h.rounds("792", "robots"))
	assert.Equal(t, ledger.Rounds{Count: 1, Open: true}, h.rounds("792", "title"))
	assert.Equal(t, ledger.Rounds{}, h.rounds("792", "robot"))

	h.add("792", "rev-2", ledger.KindReview, "")
	assert.Equal(t, ledger.Rounds{Count: 2}, h.rounds("792", "robots"))

	h.add("792", "des-1", ledger.KindDesign, "")
	assert.Equal(t, ledger.Rounds{}, h.rounds("792", "robots"))

	h.add("792", "fix-4", ledger.KindFix, "robots")
	h.add("792", "fix-5", ledger.KindRelay, "robots")
	assert.Equal(t, ledger.Rounds{Count: 1, Open: true}, h.rounds("792", "robots"))

	h.add("792", "rev-3", ledger.KindReview, "")
	h.add("792", "fix-6", ledger.KindRelay, "robots")
	assert.Equal(t, ledger.Rounds{Count: 1}, h.rounds("792", "robots"))
}

func TestRounds_AReworkRowAloneOpensARoundAndARelayRowDoesNot(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("792", "rev-1", ledger.KindReview, "")
	h.add("792", "fix-1", ledger.KindRework, "robots")

	assert.Equal(t, ledger.Rounds{Count: 1, Open: true}, h.rounds("792", "robots"))

	h.add("792", "rev-2", ledger.KindReview, "")
	h.add("792", "fix-2", ledger.KindRelay, "robots")
	assert.Equal(t, ledger.Rounds{Count: 1}, h.rounds("792", "robots"))

	h.add("792", "fix-3", ledger.KindRework, "robots")
	assert.Equal(t, ledger.Rounds{Count: 2, Open: true}, h.rounds("792", "robots"))
}

func TestAdd_TheSameLaneNameUnderTwoKeysHasARowUnderEach(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("790", "fix-1", ledger.KindFix, "")

	assert.Equal(t, ledger.Added{FixLanes: 1}, h.add("792", "fix-1", ledger.KindFix, ""))
	assert.Equal(t, 1, h.count("792", ""))
	assert.Equal(t, 1, h.count("790", ""))
}

func TestCount_MatchesAKindListByBarsAroundEachKind(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("1", "a", ledger.KindFix, "")
	h.add("1", "b", ledger.KindPapers, "")

	assert.Equal(t, 2, h.count("1", "fix|papers"))
	assert.Equal(t, 0, h.count("1", "pap"))
	assert.Equal(t, 0, h.count("1", "ix"))
	assert.Equal(t, 1, h.count("1", ""))
	assert.Equal(t, 1, h.count("1", "fix|"))
	assert.Equal(t, 1, h.count("1", "fix"))
}

func TestAdd_TheFirstReviewRecordsTheIssuesTheBodyCloses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     string
		recorded []string
	}{
		{"closes_fixes_resolves_sorted_once", "Closes #12\nFixes #7 and resolves #12", []string{"7", "12"}},
		{"every_verb_form", "closed #3, FIXED #4, resolved: #5", []string{"3", "4", "5"}},
		{"fix_with_and_without_colon", "Fix #1\nFix: #2", []string{"1", "2"}},
		{"verb_inside_a_word", "prefix #9 disclose #8", []string{"8", "9"}},
		{"long_s_does_not_fold", "cloſes #5 and reſolved #6", nil},
		{"first_spelling_of_a_number_wins", "Closes #012 and closes #12", []string{"012"}},
		{"several_blanks", "Closes   #6", []string{"6"}},
		{"hash_apart_or_other_verb", "Closes  # 6 and see #7 and closing #8", nil},
		{"carriage_returns", "Closes #1\r\nCloses #2\r\n", []string{"1", "2"}},
		{"no_issue", "Draft.", nil},
		{"empty_body", "", nil},
		{"huge_number_sorts_by_value", "Closes #100000000000000000000 and closes #9", []string{"9", "100000000000000000000"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			added, err := h.ledger.Add(ledger.Request{Key: "792", Lane: "rev-1", Kind: ledger.KindReview, At: at, Body: body(tc.body)})
			require.NoError(t, err)

			assert.Equal(t, ledger.Added{FirstReview: true, Closes: tc.recorded}, added)

			want := tc.recorded
			if len(want) == 0 {
				want = []string{"-"}
			}

			got, err := h.ledger.Closes("792")
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestAdd_ASecondReviewReadsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	calls := 0
	read := func() (string, error) { calls++; return "Closes #5", nil }

	for _, lane := range []string{"rev-1", "rev-2"} {
		_, err := h.ledger.Add(ledger.Request{Key: "h2m-spacing", Lane: lane, Kind: ledger.KindReview, At: at, Body: read})
		require.NoError(t, err)
	}

	assert.Equal(t, 1, calls)

	got, err := h.ledger.Closes("h2m-spacing")
	require.NoError(t, err)
	assert.Equal(t, []string{"5"}, got)

	other, err := h.ledger.Closes("other")
	require.NoError(t, err)
	assert.Empty(t, other)
}

func TestAdd_ABodyThatCannotBeReadWritesNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	failure := errors.New("no body")
	_, err := h.ledger.Add(ledger.Request{Key: "792", Lane: "rev-1", Kind: ledger.KindReview, At: at, Body: func() (string, error) { return "", failure }})

	require.ErrorIs(t, err, failure)
	assert.NoFileExists(t, h.file())
}

func TestAdd_AReviewWithNoBodyReaderIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.ledger.Add(ledger.Request{Key: "792", Lane: "rev-1", Kind: ledger.KindReview, At: at})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "the first review of '792' needs the pull request body.")
}

func TestAdd_AFolderThatDoesNotExistIsARefusalThatNamesTheFile(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "gone")
	_, err := ledger.Open(missing).Add(ledger.Request{Key: "1", Lane: "a", Kind: ledger.KindFix, At: at})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "cannot open "+filepath.Join(missing, ledger.FileName)+": ")
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.NotContains(t, err.Error(), "open "+filepath.Join(missing, ledger.FileName)+": open")
}

func TestReads_AFolderThatDoesNotExistIsARefusalAndAMissingFileHasNoRows(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "gone")
	_, err := ledger.Open(missing).Count("1", "")

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "cannot read "+filepath.Join(missing, ledger.FileName)+": no such file or directory")

	count, err := ledger.Open(t.TempDir()).Count("1", "")
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestReads_AFileThatIsNotALedgerIsARefusalThatNamesIt(t *testing.T) {
	t.Parallel()

	for name, prepare := range map[string]func(h *harness){
		"a directory": func(h *harness) { require.NoError(t, os.Mkdir(h.file(), 0o700)) },
		"not sqlite":  func(h *harness) { require.NoError(t, os.WriteFile(h.file(), []byte("792\tfix-1\tfix\n"), 0o600)) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			prepare(h)

			var refusal *check.Refusal

			_, err := h.ledger.Count("792", "")
			require.ErrorAs(t, err, &refusal)
			assert.Contains(t, err.Error(), h.file())

			_, err = h.ledger.Closes("792")
			require.ErrorAs(t, err, &refusal)

			_, err = h.ledger.Add(ledger.Request{Key: "1", Lane: "a", Kind: ledger.KindFix, At: at})
			require.ErrorAs(t, err, &refusal)
		})
	}
}

func TestReads_KeysThatDifferOnlyInLeadingZerosAreDifferentKeys(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("012", "fix-a", ledger.KindFix, "")
	h.add("12", "fix-b", ledger.KindFix, "")
	h.add("12", "fix-c", ledger.KindFix, "")

	assert.Equal(t, 1, h.count("012", ""))
	assert.Equal(t, 2, h.count("12", ""))
	assert.Equal(t, 1, h.prior("12", "fix-c"))
	assert.Equal(t, 1, h.prior("012", "none"))
}

func TestAdd_ANonAsciiDigitIsNotAPullRequestKey(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.ledger.Add(ledger.Request{Key: "٣", Lane: "rv-1", Kind: ledger.KindReview, At: at, Body: body("")})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "is not a pull request key")
	assert.NoFileExists(t, h.file())
}

func TestSince_TheRowTimeIsUTCToTheMinute(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		when time.Time
		now  time.Time
		days int
	}{
		{"utc", at, at.Add(48 * time.Hour), 2},
		{"seconds_dropped", time.Date(2026, 1, 2, 3, 4, 59, 0, time.UTC), time.Date(2026, 1, 3, 3, 4, 0, 0, time.UTC), 1},
		{"zone_converted", time.Date(2026, 1, 2, 3, 4, 0, 0, time.FixedZone("plus-two", 2*60*60)), time.Date(2026, 1, 3, 1, 4, 0, 0, time.UTC), 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			_, err := h.ledger.Add(ledger.Request{Key: "7", Lane: "fix-1", Kind: ledger.KindFix, At: tc.when})
			require.NoError(t, err)

			days, found, err := h.ledger.Since("7", ledger.KindFix, tc.now)
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, tc.days, days)
		})
	}
}

func TestAdd_TheFileIsCreatedWithMode0600(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.ledger.Add(ledger.Request{Key: "7", Lane: "rev-1", Kind: ledger.KindReview, At: at, Body: body("Closes #1")})
	require.NoError(t, err)

	info, err := os.Stat(h.file())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestAdd_TwoHundredConcurrentAddsWriteEachLaneOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	const (
		lanes    = 200
		inFlight = 8
	)

	// An Add waits at most 30 seconds for the lock, then returns the locked error. SQLite polls the lock with a sleep, so 400 Adds
	// that all wait at once starve each other, and the last ones come close to the 30 seconds on a loaded machine.
	// The semaphore bounds the Adds in flight; every lane still gets both of its writers.
	slots := make(chan struct{}, inFlight)

	var group sync.WaitGroup

	failures := make(chan error, 2*lanes)

	for i := range lanes {
		for range 2 {
			group.Add(1)

			go func() {
				defer group.Done()

				slots <- struct{}{}
				defer func() { <-slots }()

				_, err := ledger.Open(h.dir).Add(ledger.Request{Key: "9", Lane: "fix-" + strconv.Itoa(i), Kind: ledger.KindFix, At: at})
				failures <- err
			}()
		}
	}

	group.Wait()
	close(failures)

	adds := 0

	for err := range failures {
		require.NoError(t, err)

		adds++
	}

	assert.Equal(t, 2*lanes, adds)
	assert.Equal(t, lanes, h.count("9", ""))
}

func (h *harness) addAt(key, lane string, kind ledger.Kind, when time.Time) {
	h.t.Helper()

	_, err := h.ledger.Add(ledger.Request{Key: key, Lane: lane, Kind: kind, At: when})
	require.NoError(h.t, err)
}

func TestSince_CountsWholeDaysFromTheLatestRowOfAKind(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.addAt("792", "m-1", ledger.KindPapers, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	h.addAt("792", "m-2", ledger.KindPapers, time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC))
	h.addAt("792", "f-1", ledger.KindFix, time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC))
	h.addAt("790", "m-3", ledger.KindPapers, time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC))

	now := time.Date(2026, 10, 10, 9, 59, 0, 0, time.UTC)
	days, found, err := h.ledger.Since("792", ledger.KindPapers, now)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 4, days)

	days, found, err = h.ledger.Since("792", ledger.KindPapers, now.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 5, days)

	_, found, err = h.ledger.Since("792", ledger.KindReview, now)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestSince_ATimeInTheFutureIsZeroDays(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.addAt("792", "m-1", ledger.KindPapers, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))

	days, found, err := h.ledger.Since("792", ledger.KindPapers, at)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Zero(t, days)
}

func TestSince_AFileThatCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, os.Mkdir(h.file(), 0o700))

	_, _, err := h.ledger.Since("792", ledger.KindPapers, at)
	require.Error(t, err)
}

func TestParseKind(t *testing.T) {
	t.Parallel()

	for _, kind := range ledger.Kinds() {
		got, err := ledger.ParseKind(string(kind))
		require.NoError(t, err)
		assert.Equal(t, kind, got)
	}

	_, err := ledger.ParseKind("survey")
	require.EqualError(t, err, "'survey' is not a lane kind. Use fix, rework, relay, review, design, papers or owner-add.")
}

// breakWith adds a trigger to the ledger file that fails every insert into the table.
func (h *harness) breakWith(table string) {
	h.t.Helper()

	h.add("0", "seed", ledger.KindPapers, "")

	db, err := sql.Open("sqlite", h.file())
	require.NoError(h.t, err)

	triggers := map[string]string{
		"lanes":  "CREATE TRIGGER broken BEFORE INSERT ON lanes BEGIN SELECT RAISE(ABORT, 'disk full'); END",
		"closes": "CREATE TRIGGER broken BEFORE INSERT ON closes BEGIN SELECT RAISE(ABORT, 'disk full'); END",
	}

	_, err = db.ExecContext(h.t.Context(), triggers[table])
	require.NoError(h.t, errors.Join(err, db.Close()))
}

func TestAdd_AFailedWriteLeavesNeitherTheLaneRowNorTheCloses(t *testing.T) {
	t.Parallel()

	for _, table := range []string{"lanes", "closes"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.breakWith(table)

			request := ledger.Request{Key: "7", Lane: "rev-1", Kind: ledger.KindReview, At: at, Body: body("Closes #3")}

			var refusal *check.Refusal

			_, err := h.ledger.Add(request)
			require.ErrorAs(t, err, &refusal)
			assert.Contains(t, err.Error(), "disk full")
			assert.Equal(t, 0, h.count("7", "review"))

			closes, err := h.ledger.Closes("7")
			require.NoError(t, err)
			assert.Empty(t, closes)
		})
	}
}

func TestAdd_ACloseRecordedWhileTheBodyWasReadWinsOverTheLaterWriter(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	other := func() (string, error) {
		_, err := h.ledger.Add(ledger.Request{Key: "7", Lane: "rev-2", Kind: ledger.KindReview, At: at, Body: body("Closes #1")})

		return "Closes #3", err
	}

	added, err := h.ledger.Add(ledger.Request{Key: "7", Lane: "rev-1", Kind: ledger.KindReview, At: at, Body: other})
	require.NoError(t, err)
	assert.Equal(t, ledger.Added{}, added)

	closes, err := h.ledger.Closes("7")
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, closes)
	assert.Equal(t, 2, h.count("7", "review"))
}

func TestReads_AQueryThatFailsIsARefusalThatNamesTheFile(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	db, err := sql.Open("sqlite", h.file())
	require.NoError(t, err)

	_, err = db.ExecContext(t.Context(), "CREATE VIEW closes AS SELECT key, issue FROM gone")
	require.NoError(t, errors.Join(err, db.Close()))

	var refusal *check.Refusal

	_, err = h.ledger.Closes("7")
	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "cannot read "+h.file())
}

func TestReads_OfAFileThatDoesNotExistFindNothingAndCreateNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	assert.Equal(t, 0, h.count("7", ""))

	closes, err := h.ledger.Closes("7")
	require.NoError(t, err)
	assert.Empty(t, closes)
	assert.NoFileExists(t, h.file())
}

func TestReads_OfAFileWithNoBytesFindNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, os.WriteFile(h.file(), nil, 0o600))

	assert.Equal(t, 0, h.count("7", ""))

	closes, err := h.ledger.Closes("7")
	require.NoError(t, err)
	assert.Empty(t, closes)
}

func TestAdd_ARequestWithNoTimeIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.ledger.Add(ledger.Request{Key: "7", Lane: "fix-1", Kind: ledger.KindFix})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "name the time of the lane")
	assert.NoFileExists(t, h.file())
}
