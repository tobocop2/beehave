package remedy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/brief"
	"github.com/tobocop2/obee/examples/remediation/tools/remedy"
	"github.com/tobocop2/obee/internal/ledger"
)

// fakeBook is a fake ledger. It answers each question with a fixed number and records the folder that was opened.
type fakeBook struct {
	designs int
	prior   int
	rounds  ledger.Rounds
}

func (b fakeBook) Count(_, kinds string) (int, error) {
	if kinds == string(ledger.KindDesign) {
		return b.designs, nil
	}

	return 0, nil
}

func (b fakeBook) Rounds(_, _ string) (ledger.Rounds, error) { return b.rounds, nil }

func (b fakeBook) Prior(_, _ string) (int, error) { return b.prior, nil }

func (fakeBook) Add(ledger.Request) (ledger.Added, error) { return ledger.Added{}, nil }

// bookDeps gives deps whose ledger is the fake, and records the state folder that the command opened.
func bookDeps(book fakeBook, opened *string) remedy.Deps {
	return remedy.Deps{Book: func(dir string) brief.Book {
		*opened = dir

		return book
	}}
}

// laneArgs is a valid admission of an impl lane that changes no output.
func laneArgs(extra ...string) []string {
	base := []string{"admit", "impl", "--lane", "fix-1", "--key", "7", "--state", "/state", "--mechanism", "robots", "--acceptance", "none: a comment", "--base-result", "none: a comment"}

	return append(base, extra...)
}

func TestAdmit_AdmitsAnImplLaneAndNamesTheRowItWrites(t *testing.T) {
	t.Parallel()

	var opened string

	code, out, errOut := run(t, bookDeps(fakeBook{}, &opened), laneArgs()...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Equal(t, "ADMITTED kind=fix mechanism=robots\n", out)
	assert.Empty(t, errOut)
	assert.Equal(t, "/state", opened)
}

func TestAdmit_AHoldBackPrintsItsLinesAndExitsOne(t *testing.T) {
	t.Parallel()

	var opened string

	code, out, errOut := run(t, bookDeps(fakeBook{}, &opened), laneArgs("--new-behavior")...)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "REFUSED: lane fix-1 adds new behavior to 7, and the ledger holds no design lane for it.")
	assert.Empty(t, errOut)

	code, out, _ = run(t, bookDeps(fakeBook{designs: 1}, &opened), laneArgs("--new-behavior")...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "ADMITTED")
}

func TestAdmit_ATwoPatchRoundMechanismSendsTheLaneToADesignLane(t *testing.T) {
	t.Parallel()

	var opened string

	code, out, _ := run(t, bookDeps(fakeBook{rounds: ledger.Rounds{Count: 2}}, &opened), laneArgs()...)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "had 2 patch rounds on the mechanism robots")
}

func TestAdmit_ALaneWithNoAcceptanceFieldIsRefused(t *testing.T) {
	t.Parallel()

	var opened string

	code, out, errOut := run(t, bookDeps(fakeBook{}, &opened), "admit", "impl", "--lane", "fix-1", "--key", "7", "--state", "/state", "--mechanism", "robots")

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "remedy admit: the lane fix-1 has no acceptance")
}

func TestAdmit_ASecondLaneReadsTheHandoffBeforeIt(t *testing.T) {
	t.Parallel()

	var opened string

	dir := t.TempDir()
	good := filepath.Join(dir, "good.md")
	bad := filepath.Join(dir, "bad.md")

	require.NoError(t, os.WriteFile(good, []byte("Regressions of this pull request so far: 0\nTokens at handoff: 1000\n"), 0o600))
	require.NoError(t, os.WriteFile(bad, []byte("no counts here\n"), 0o600))

	book := fakeBook{prior: 1}

	code, out, _ := run(t, bookDeps(book, &opened), laneArgs("--handoff", good, "--cap", "150000")...)

	assert.Equal(t, remedy.ExitPass, code)
	assert.Contains(t, out, "ADMITTED")

	code, out, _ = run(t, bookDeps(book, &opened), laneArgs("--handoff", bad)...)

	assert.Equal(t, remedy.ExitDefect, code)
	assert.Contains(t, out, "REFUSED: "+bad+" has no line 'Regressions of this pull request so far: <n>'")

	code, _, errOut := run(t, bookDeps(book, &opened), laneArgs("--handoff", good, "--cap", "-5")...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, "is not a whole number of tokens above zero")
}

func TestAdmit_ABadCommandLineShowsTheUsageAndIsARefusal(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"no kind":        {"admit"},
		"unknown kind":   {"admit", "bake", "--lane", "a", "--state", "/s"},
		"no lane":        {"admit", "impl", "--state", "/s"},
		"no state":       {"admit", "impl", "--lane", "a"},
		"unknown flag":   {"admit", "impl", "--bogus"},
		"extra argument": {"admit", "impl", "--lane", "a", "--state", "/s", "extra"},
	}

	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			code, out, errOut := run(t, remedy.Deps{}, args...)

			assert.Equal(t, remedy.ExitRefused, code)
			assert.Empty(t, out)
			assert.NotEmpty(t, errOut)
		})
	}
}

func TestOpenBook_OpensTheLedgerOfAStateFolder(t *testing.T) {
	t.Parallel()

	count, err := remedy.OpenBook(ledger.Open)(t.TempDir()).Count("7", string(ledger.KindDesign))

	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestHelp_ListsAdmitAndSize(t *testing.T) {
	t.Parallel()

	_, out, _ := run(t, remedy.Deps{}, "--help")

	assert.True(t, strings.Contains(out, "admit <kind>") && strings.Contains(out, "size <brief>"))
}
