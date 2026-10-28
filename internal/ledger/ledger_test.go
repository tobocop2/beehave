package ledger_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/check"
	"github.com/tobocop2/obee/internal/ledger"
)

const (
	tab   = "\t"
	stamp = "2026-10-10T12:30Z"
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

func (h *harness) closesFile() string { return h.file() + ".closes" }

func (h *harness) write(path, text string) {
	h.t.Helper()
	require.NoError(h.t, os.WriteFile(path, []byte(text), 0o600))
}

func (h *harness) text(path string) string {
	h.t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // a file inside the temporary folder of the test
	require.NoError(h.t, err)

	return string(data)
}

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
	assert.Equal(t, "792"+tab+"fix-1"+tab+"fix"+tab+stamp, h.text(h.file())[:len("792"+tab+"fix-1"+tab+"fix"+tab+stamp)])
	assert.Equal(t, 5, countLines(h.text(h.file())))
}

func countLines(text string) int {
	n := 0

	for _, r := range text {
		if r == '\n' {
			n++
		}
	}

	return n
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
	h.write(h.closesFile(), "792"+tab+"-\n")
	h.add("792", "des-1", ledger.KindDesign, "robots")
	h.add("792", "rev-1", ledger.KindReview, "robots")

	assert.Equal(t, "792"+tab+"des-1"+tab+"design"+tab+stamp+"\n792"+tab+"rev-1"+tab+"review"+tab+stamp+"\n", h.text(h.file()))
}

func TestAdd_ABadAddIsARefusalThatNamesTheFixAndWritesNothing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		request ledger.Request
		message string
	}{
		{"unknown_kind", ledger.Request{Key: "792", Lane: "fix-1", Kind: "survey"}, "'survey' is not a lane kind. Use fix, rework, relay, review, design or papers."},
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
	h.write(h.closesFile(), "792"+tab+"-\n")

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
	h.write(h.closesFile(), "792"+tab+"-\n")
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
	assert.Equal(t, 2, countLines(h.text(h.file())))
}

func TestReads_ARowEndingInACarriageReturnKeepsItInTheLastColumn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write(h.file(), "792\tfix-1\tfix\tT\trobots\r\n792\tfix-2\tfix\tT\trobots\r\n")

	assert.Equal(t, ledger.Rounds{}, h.rounds("792", "robots"), "the mechanism column reads robots with the carriage return")
	assert.Equal(t, 2, h.count("792", ""))

	h.write(h.file(), "792\tfix-1\tfix\tT\rx\n")
	assert.Equal(t, 1, h.count("792", ""), "a lone carriage return does not split a row")
}

func TestCloses_AFileWithCarriageReturnsPrintsThem(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write(h.closesFile(), "792\t41\r\n792\t42\n")

	got, err := h.ledger.Closes("792")
	require.NoError(t, err)
	assert.Equal(t, []string{"41\r", "42"}, got)
}

func TestCount_ALastRowWithNoNewlineIsStillARow(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write(h.file(), "1\ta\tfix\tT")

	assert.Equal(t, 1, h.count("1", ""))
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

	h.write(h.file(), "1\n")
	assert.Equal(t, 1, h.count("1", "fix|"), "a list that ends in a bar also matches a row with no kind")
	assert.Equal(t, 0, h.count("1", "fix"))
}

func TestReads_AShortRowHasEmptyColumnsAndCountsAsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write(h.file(), "1\n\n1\ta\n")

	assert.Equal(t, 0, h.count("1", ""))
	assert.Equal(t, 0, h.prior("1", "z"))
	assert.Equal(t, ledger.Rounds{}, h.rounds("1", "m"))
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

			var want strings.Builder
			for _, number := range tc.recorded {
				want.WriteString("792" + tab + number + "\n")
			}

			if want.Len() == 0 {
				want.WriteString("792" + tab + "-\n")
			}

			assert.Equal(t, want.String(), h.text(h.closesFile()))
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
	assert.NoFileExists(t, h.closesFile())
}

func TestAdd_AReviewWithNoBodyReaderIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.ledger.Add(ledger.Request{Key: "792", Lane: "rev-1", Kind: ledger.KindReview, At: at})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "the first review of '792' needs the pull request body.")
}

func TestAdd_AClosesRowWithAnEmptyIssueColumnIsNotAFirstReview(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write(h.closesFile(), "12"+tab+"\n")

	added, err := h.ledger.Add(ledger.Request{Key: "12", Lane: "rv-1", Kind: ledger.KindReview, At: at, Body: body("Closes #5")})
	require.NoError(t, err)
	assert.Equal(t, ledger.Added{FirstReview: true, Closes: []string{"5"}}, added)

	got, err := h.ledger.Closes("12")
	require.NoError(t, err)
	assert.Equal(t, []string{"", "5"}, got)
}

func TestAdd_ALedgerThatCannotBeWrittenIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.NoError(t, os.Symlink(filepath.Join(h.dir, "gone", "x"), h.file()))

	_, err := h.ledger.Add(ledger.Request{Key: "1", Lane: "a", Kind: ledger.KindFix, At: at})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "cannot write "+h.file()+": no such file or directory")
}

func TestAdd_AFolderThatDoesNotExistIsARefusalThatNamesTheLock(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "gone")
	_, err := ledger.Open(missing).Add(ledger.Request{Key: "1", Lane: "a", Kind: ledger.KindFix, At: at})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "cannot lock "+filepath.Join(missing, ledger.FileName)+".lock: no such file or directory")
}

func TestReads_AFileThatCannotBeReadIsARefusalThatNamesIt(t *testing.T) {
	t.Parallel()

	for _, which := range []string{"ledger", "closes"} {
		t.Run(which, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			target := h.file()

			if which == "closes" {
				target = h.closesFile()
			}

			require.NoError(t, os.Mkdir(target, 0o700))

			var err error
			if which == "ledger" {
				_, err = h.ledger.Count("792", "")
			} else {
				_, err = h.ledger.Closes("792")
			}

			var refusal *check.Refusal

			require.ErrorAs(t, err, &refusal)
			assert.Contains(t, err.Error(), "cannot read "+target+": is a directory")
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

func TestAdd_TheRowTimeIsUTCToTheMinuteWithAColon(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{"utc", at, "2026-10-10T12:30Z"},
		{"seconds_dropped", time.Date(2026, 1, 2, 3, 4, 59, 0, time.UTC), "2026-01-02T03:04Z"},
		{"zone_converted", time.Date(2026, 1, 2, 3, 4, 0, 0, time.FixedZone("plus-two", 2*60*60)), "2026-01-02T01:04Z"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			_, err := h.ledger.Add(ledger.Request{Key: "7", Lane: "fix-1", Kind: ledger.KindFix, At: tc.when})
			require.NoError(t, err)
			assert.Equal(t, "7"+tab+"fix-1"+tab+"fix"+tab+tc.want+"\n", h.text(h.file()))
		})
	}
}

func TestAdd_ALaneRowKeepsTheMechanismTheModelAndTheRoundInOrder(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		request ledger.Request
		want    string
	}{
		{"all_three", ledger.Request{Mechanism: "robots", Model: "sonnet", Round: 2}, tab + "robots" + tab + "sonnet" + tab + "2"},
		{"mechanism_only", ledger.Request{Mechanism: "robots"}, tab + "robots"},
		{"model_without_mechanism", ledger.Request{Model: "sonnet"}, tab + tab + "sonnet"},
		{"round_without_the_rest", ledger.Request{Round: 3}, tab + tab + tab + "3"},
		{"none", ledger.Request{}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			tc.request.Key, tc.request.Lane, tc.request.Kind, tc.request.At = "7", "fix-1", ledger.KindRework, at
			_, err := h.ledger.Add(tc.request)
			require.NoError(t, err)
			assert.Equal(t, "7"+tab+"fix-1"+tab+"rework"+tab+stamp+tc.want+"\n", h.text(h.file()))
		})
	}
}

func TestAdd_AReviewRowKeepsNoLaneColumns(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write(h.closesFile(), "7"+tab+"-\n")
	_, err := h.ledger.Add(ledger.Request{Key: "7", Lane: "rev-1", Kind: ledger.KindReview, Mechanism: "m", Model: "opus", Round: 4, At: at})
	require.NoError(t, err)
	assert.Equal(t, "7"+tab+"rev-1"+tab+"review"+tab+stamp+"\n", h.text(h.file()))
}

func TestAdd_FilesAreCreatedWithMode0600(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.ledger.Add(ledger.Request{Key: "7", Lane: "rev-1", Kind: ledger.KindReview, At: at, Body: body("Closes #1")})
	require.NoError(t, err)

	for _, path := range []string{h.file(), h.closesFile()} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), path)
	}
}

func TestAdd_TwoHundredConcurrentAddsWriteEachLaneOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	const lanes = 200

	var group sync.WaitGroup

	failures := make(chan error, 2*lanes)

	for i := range lanes {
		for range 2 {
			group.Add(1)

			go func() {
				defer group.Done()

				_, err := ledger.Open(h.dir).Add(ledger.Request{Key: "9", Lane: "fix-" + strconv.Itoa(i), Kind: ledger.KindFix, At: at})
				failures <- err
			}()
		}
	}

	group.Wait()
	close(failures)

	for err := range failures {
		require.NoError(t, err)
	}

	assert.Equal(t, lanes, h.count("9", ""))
	assert.Equal(t, lanes, countLines(h.text(h.file())))
}

func TestSince_CountsWholeDaysFromTheLatestRowOfAKind(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write(h.file(), "792\tm-1\tpapers\t2026-10-01T00:00Z\n"+
		"792\tm-2\tpapers\t2026-10-05T10:00Z\n"+
		"792\tf-1\tfix\t2026-10-09T00:00Z\n"+
		"790\tm-3\tpapers\t2026-10-09T00:00Z\n")

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

func TestSince_ARowWithATimeThatIsNotUTCIsARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write(h.file(), "792\tm-1\tpapers\tyesterday\n")

	_, _, err := h.ledger.Since("792", ledger.KindPapers, at)

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Contains(t, err.Error(), "'yesterday'")
}

func TestSince_ATimeInTheFutureIsZeroDays(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write(h.file(), "792\tm-1\tpapers\t2027-01-01T00:00Z\n")

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

	for _, kind := range []ledger.Kind{ledger.KindFix, ledger.KindRework, ledger.KindRelay, ledger.KindReview, ledger.KindDesign, ledger.KindPapers} {
		got, err := ledger.ParseKind(string(kind))
		require.NoError(t, err)
		assert.Equal(t, kind, got)
	}

	_, err := ledger.ParseKind("survey")
	require.EqualError(t, err, "'survey' is not a lane kind. Use fix, rework, relay, review, design or papers.")
}

type failingRows struct {
	listErr, appendErr, holdErr error
}

func (f failingRows) List() ([][]string, error) { return nil, f.listErr }

func (f failingRows) Append(string) error { return f.appendErr }

func (f failingRows) Publish(string) error { return f.appendErr }

func (f failingRows) Hold(run func() error) error {
	if f.holdErr != nil {
		return f.holdErr
	}

	return run()
}

func (f failingRows) Share(run func() error) error { return f.Hold(run) }

// flakyRows is a table whose publish fails while fail is set.
type flakyRows struct {
	ledger.Rows
	fail *bool
}

func (f flakyRows) Publish(text string) error {
	if *f.fail {
		return errors.New("disk full")
	}

	return f.Rows.Publish(text)
}

func TestAdd_TheLaneRowIsWrittenBeforeTheClosesSoARetryIsComplete(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	fail := true
	flaky := ledger.New(ledger.NewTable(h.file()), flakyRows{Rows: ledger.NewTable(h.closesFile()), fail: &fail})
	request := ledger.Request{Key: "7", Lane: "rev-1", Kind: ledger.KindReview, At: at, Body: body("Closes #3")}

	_, err := flaky.Add(request)
	require.ErrorContains(t, err, "disk full")
	assert.Equal(t, "7"+tab+"rev-1"+tab+"review"+tab+stamp+"\n", h.text(h.file()))

	fail = false

	added, err := flaky.Add(request)
	require.NoError(t, err)
	assert.Equal(t, ledger.Added{FirstReview: true, Closes: []string{"3"}}, added)
	assert.Equal(t, "7"+tab+"3\n", h.text(h.closesFile()))
}

func TestAdd_TheClosesFileIsReplacedByRenameNotAppendedInPlace(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.ledger.Add(ledger.Request{Key: "7", Lane: "rev-1", Kind: ledger.KindReview, At: at, Body: body("Closes #3")})
	require.NoError(t, err)

	before, err := os.Stat(h.closesFile())
	require.NoError(t, err)

	_, err = h.ledger.Add(ledger.Request{Key: "8", Lane: "rev-1", Kind: ledger.KindReview, At: at, Body: body("Closes #4")})
	require.NoError(t, err)

	after, err := os.Stat(h.closesFile())
	require.NoError(t, err)
	assert.False(t, os.SameFile(before, after), "the second write made a new file")
	assert.Equal(t, "7"+tab+"3\n8"+tab+"4\n", h.text(h.closesFile()))

	entries, err := os.ReadDir(h.dir)
	require.NoError(t, err)

	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	assert.ElementsMatch(t, []string{ledger.FileName, ledger.FileName + ".closes", ledger.FileName + ".lock"}, names)
}

func TestReads_WaitForAWriterThatHoldsTheLock(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.add("7", "fix-1", ledger.KindFix, "")

	reads := map[string]func() error{
		"closes": func() error { _, err := h.ledger.Closes("7"); return err },
		"count":  func() error { _, err := h.ledger.Count("7", ""); return err },
		"rounds": func() error { _, err := h.ledger.Rounds("7", "m"); return err },
		"prior":  func() error { _, err := h.ledger.Prior("7", "x"); return err },
		"since":  func() error { _, _, err := h.ledger.Since("7", ledger.KindFix, at); return err },
	}

	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			done := make(chan error, 1)

			err := ledger.NewTable(h.file()).Hold(func() error {
				go func() { done <- read() }()

				time.Sleep(150 * time.Millisecond)
				assert.Empty(t, done, "the read did not wait for the writer")

				return nil
			})
			require.NoError(t, err)
			require.NoError(t, <-done)
		})
	}
}

func TestShare_ReadersRunAtTheSameTimeAndAWriterWaitsForThem(t *testing.T) {
	t.Parallel()

	table := ledger.NewTable(filepath.Join(t.TempDir(), "rows.tsv"))
	inside := make(chan struct{})
	release := make(chan struct{})
	shared := make(chan error, 1)

	go func() {
		shared <- table.Share(func() error {
			close(inside)
			<-release

			return nil
		})
	}()

	<-inside
	require.NoError(t, table.Share(func() error { return nil }))

	written := make(chan error, 1)

	go func() { written <- table.Hold(func() error { return nil }) }()

	select {
	case err := <-written:
		t.Fatalf("the writer did not wait for the reader: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-shared)
	require.NoError(t, <-written)
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

func TestAdd_AFailureOfTheRowStoreIsReturned(t *testing.T) {
	t.Parallel()

	failure := errors.New("disk full")
	request := ledger.Request{Key: "1", Lane: "a", Kind: ledger.KindFix, At: at}
	review := ledger.Request{Key: "1", Lane: "a", Kind: ledger.KindReview, At: at, Body: body("Closes #1")}

	cases := []struct {
		name    string
		rows    failingRows
		closes  failingRows
		request ledger.Request
	}{
		{"hold", failingRows{holdErr: failure}, failingRows{}, request},
		{"list_before_the_row", failingRows{listErr: failure}, failingRows{}, request},
		{"append_the_row", failingRows{appendErr: failure}, failingRows{}, request},
		{"list_the_closes", failingRows{}, failingRows{listErr: failure}, review},
		{"append_the_closes", failingRows{}, failingRows{appendErr: failure}, review},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := ledger.New(tc.rows, tc.closes).Add(tc.request)
			require.ErrorIs(t, err, failure)
		})
	}
}
