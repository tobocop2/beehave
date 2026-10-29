package ledger_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/ledger"
)

func (h *harness) approve(key, issue, words string) (ledger.Added, error) {
	h.t.Helper()

	return h.ledger.Add(ledger.Request{Key: key, Lane: issue, Kind: ledger.KindOwnerAdd, Words: words, At: at})
}

func TestKinds_ListOwnerAddLast(t *testing.T) {
	t.Parallel()

	kinds := ledger.Kinds()
	assert.Equal(t, ledger.KindOwnerAdd, kinds[len(kinds)-1])

	got, err := ledger.ParseKind("owner-add")
	require.NoError(t, err)
	assert.Equal(t, ledger.KindOwnerAdd, got)
}

// words returns the words of every owner-add row of the ledger file, read with SQL because the ledger gives no way to read them.
func (h *harness) words() []string {
	h.t.Helper()

	db, err := sql.Open("sqlite", h.file())
	require.NoError(h.t, err)

	rows, err := db.QueryContext(h.t.Context(), "SELECT words FROM lanes WHERE kind = 'owner-add' ORDER BY rowid")
	require.NoError(h.t, err)

	var got []string

	for rows.Next() {
		var words string

		require.NoError(h.t, rows.Scan(&words))
		got = append(got, words)
	}

	require.NoError(h.t, errors.Join(rows.Err(), rows.Close(), db.Close()))

	return got
}

func TestAdd_OwnerAddKeepsTheWordsOfTheOwner(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	added, err := h.approve("792", "795", "yes, keep it")
	require.NoError(t, err)
	assert.Equal(t, ledger.Added{}, added)
	assert.Equal(t, []string{"yes, keep it"}, h.words())
}

func TestAdd_OwnerAddForOneIssueTwiceWritesOneRow(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.approve("792", "795", "first")
	require.NoError(t, err)
	_, err = h.approve("792", "795", "second")
	require.NoError(t, err)

	assert.Equal(t, []string{"first"}, h.words())
}

func TestAdd_OwnerAddRefusesAnIssueOrWordsThatBreakTheRow(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ issue, words, message string }{
		"issue is not digits": {"79x", "ok", "the issue '79x' is not a number. Use digits only."},
		"issue is empty":      {"", "ok", "name the lane and its kind."},
		"words are empty":     {"795", "", "name the words of the owner for issue 795."},
		"words hold a tab":    {"795", "a\tb", "the words of the owner hold a tab or a line break. Write them on one line."},
		"words hold a break":  {"795", "a\nb", "the words of the owner hold a tab or a line break. Write them on one line."},
		"words hold a return": {"795", "a\rb", "the words of the owner hold a tab or a line break. Write them on one line."},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			_, err := h.approve("792", test.issue, test.words)
			require.EqualError(t, err, test.message)
		})
	}
}

func TestAdd_OwnerAddIsNotAFixLane(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.approve("792", "795", "ok")
	require.NoError(t, err)

	assert.Equal(t, 0, h.count("792", ""))
	assert.Equal(t, 1, h.count("792", "owner-add"))

	prior, err := h.ledger.Prior("792", "other")
	require.NoError(t, err)
	assert.Equal(t, 0, prior)
}

func TestOwnerAdds_ReturnsTheIssuesOfTheKeyOnly(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	for _, row := range []struct{ key, issue string }{{"792", "795"}, {"792", "800"}, {"793", "801"}} {
		_, err := h.approve(row.key, row.issue, "ok")
		require.NoError(t, err)
	}

	h.add("792", "795", ledger.KindFix, "") // a fix lane named like an issue is no approval

	got, err := h.ledger.OwnerAdds("792")
	require.NoError(t, err)
	assert.Equal(t, []string{"795", "800"}, got)

	none, err := h.ledger.OwnerAdds("794")
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestOwnerAdds_RefusesAKeyThatIsNotAToken(t *testing.T) {
	t.Parallel()

	_, err := newHarness(t).ledger.OwnerAdds("a b")
	require.Error(t, err)
}

func TestClosingNumbers_ListsEachIssueOnceInNumericOrder(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"7", "012", "100"}, ledger.ClosingNumbers("Closes #100, fixes #012, Closes #12, resolved #7"))
	assert.Empty(t, ledger.ClosingNumbers("no keyword #5"))
}

// GitHub: a keyword counts as a word of its own. "unfixed #3" and "prefix #4" close nothing.
func TestClosingNumbers_AKeywordInsideAWordIsNotAKeyword(t *testing.T) {
	t.Parallel()

	assert.Empty(t, ledger.ClosingNumbers("unfixed #3, prefix #4, disclosed #5, reclose #6, foo_fixes #7, 9fixes #8"))
	assert.Equal(t, []string{"1", "2", "3"}, ledger.ClosingNumbers("unfixed #9\nFixes #1; (closes #2) - resolved #3"))
	assert.Equal(t, []string{"1"}, ledger.ClosingNumbers("Fixes #1"), "a keyword at the start of the body")
}
