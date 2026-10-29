package brief_test

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/brief"
	obeecheck "github.com/tobocop2/obee/examples/remediation/tools/check"
	"github.com/tobocop2/obee/examples/remediation/tools/ledger"
)

var when = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func noBody() ([]string, error) { return nil, nil }

func addRow(t *testing.T, book *ledger.Ledger, key, lane string, kind ledger.Kind, mechanism string) {
	t.Helper()

	_, err := book.Add(ledger.Request{Key: key, Lane: lane, Kind: kind, Mechanism: mechanism, At: when, Closing: noBody})
	require.NoError(t, err)
}

func passes(lines []string, refused bool) brief.HandoffCheck {
	return func(string, string, string, int) ([]string, bool, error) { return lines, refused, nil }
}

func neverCalled(t *testing.T) brief.HandoffCheck {
	t.Helper()

	return func(string, string, string, int) ([]string, bool, error) {
		t.Error("the handoff check ran for a lane with no earlier lane")

		return nil, false, nil
	}
}

func implLane() brief.Lane {
	return brief.Lane{Key: "1", Acceptance: "none: x", BaseResult: "none: x", Mechanism: "m"}
}

func admit(kind brief.Kind, lane brief.Lane, book brief.Book, handoff brief.HandoffCheck) (brief.Admission, error) {
	return brief.Admit(kind, "fix-1", lane, book, handoff)
}

func TestAdmit_AnImplLaneNamesItsKeyBeforeAnythingElse(t *testing.T) {
	t.Parallel()

	lane := implLane()
	lane.Key = ""

	_, err := admit(brief.KindImpl, lane, ledger.Open(t.TempDir()), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the lane has no PR number; give one with the key field")
}

func TestAdmit_AReviewLaneNeedsNoSlotAndWritesNoLaneRow(t *testing.T) {
	t.Parallel()

	got, err := admit(brief.KindRev, brief.Lane{Mechanism: "title"}, ledger.Open(t.TempDir()), nil)

	require.NoError(t, err)
	assert.Equal(t, brief.Admission{Kind: "", Mechanism: "title"}, got)
}

func TestAdmit_AnImplLaneNeedsItsAcceptanceSlots(t *testing.T) {
	t.Parallel()

	lane := implLane()
	lane.Acceptance = ""

	_, err := admit(brief.KindImpl, lane, ledger.Open(t.TempDir()), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the lane fix-1 has no acceptance; give a path with the acceptance field")

	lane = implLane()
	lane.BaseResult = ""

	_, err = admit(brief.KindImpl, lane, ledger.Open(t.TempDir()), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the lane fix-1 has no base_result; give a path with the base_result field")
}

func TestAdmit_AScriptNeedsItsResultAtTheBase(t *testing.T) {
	t.Parallel()

	lane := implLane()
	lane.Acceptance = "/s/accept.sh"

	_, err := admit(brief.KindImpl, lane, ledger.Open(t.TempDir()), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the acceptance field names a script and the base_result field says none")
	assert.Contains(t, err.Error(), "Run the script at the base first and give the path of that result with the base_result field")

	lane.BaseResult = "/s/base.txt"
	_, err = admit(brief.KindImpl, lane, ledger.Open(t.TempDir()), nil)
	require.NoError(t, err)
}

func TestAdmit_ANewBehaviorNeedsADesignLaneInTheLedger(t *testing.T) {
	t.Parallel()

	book := ledger.Open(t.TempDir())
	lane := implLane()
	lane.NewBehavior = true

	_, err := admit(brief.KindImpl, lane, book, nil)

	var stop *brief.Stopped

	require.ErrorAs(t, err, &stop)
	assert.Contains(t, stop.Error(), "REFUSED: lane fix-1 adds new behavior to 1, and the ledger holds no design lane for it.")
	// The refusal names no command: it says in plain words that the next lane is a design lane.
	assert.Contains(t, stop.Error(), "dispatch a design lane next")

	addRow(t, book, "1", "des-1", ledger.KindDesign, "")

	_, err = admit(brief.KindImpl, lane, book, nil)
	require.NoError(t, err)
}

func TestAdmit_AFixLaneNamesItsMechanismAsOneToken(t *testing.T) {
	t.Parallel()

	lane := implLane()
	lane.Mechanism = ""

	_, err := admit(brief.KindImpl, lane, ledger.Open(t.TempDir()), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the lane fix-1 has no mechanism; give one token with the mechanism field")

	lane.Mechanism = "robots classifier"
	_, err = admit(brief.KindImpl, lane, ledger.Open(t.TempDir()), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the mechanism 'robots classifier' is not one token")
}

func TestAdmit_ARelayLaneNeedsNoMechanism(t *testing.T) {
	t.Parallel()

	lane := implLane()
	lane.Mechanism = ""
	lane.Kind = brief.LaneRelay

	got, err := admit(brief.KindImpl, lane, ledger.Open(t.TempDir()), nil)

	require.NoError(t, err)
	assert.Equal(t, ledger.KindRelay, got.Kind)
}

func TestAdmit_TheLaneKindDefaultsToFixAndNamesTheLedgerKind(t *testing.T) {
	t.Parallel()

	for kind, want := range map[brief.LaneKind]ledger.Kind{
		"":               ledger.KindFix,
		brief.LaneFix:    ledger.KindFix,
		brief.LaneRework: ledger.KindRework,
	} {
		lane := implLane()
		lane.Kind = kind

		got, err := admit(brief.KindImpl, lane, ledger.Open(t.TempDir()), nil)

		require.NoError(t, err)
		assert.Equal(t, want, got.Kind)
		assert.Equal(t, "m", got.Mechanism)
	}
}

func TestAdmit_ADesignAndAPapersLaneAreNoFixLane(t *testing.T) {
	t.Parallel()

	design, err := admit(brief.KindDesign, implLane(), ledger.Open(t.TempDir()), nil)
	require.NoError(t, err)
	assert.Equal(t, ledger.KindDesign, design.Kind)

	papers, err := admit(brief.KindPapers, implLane(), ledger.Open(t.TempDir()), nil)
	require.NoError(t, err)
	assert.Equal(t, ledger.KindPapers, papers.Kind)
}

func TestAdmit_AThirdPatchRoundIsStoppedUntilADesignLane(t *testing.T) {
	t.Parallel()

	book := ledger.Open(t.TempDir())
	lane := implLane()
	lane.Mechanism = "robots"
	handoff := writeFile(t, "prev.md", "# Handoff\n")
	lane.Handoff = handoff

	addRow(t, book, "1", "fix-0", ledger.KindFix, "robots")
	addRow(t, book, "1", "rev-1", ledger.KindReview, "")
	addRow(t, book, "1", "fix-0b", ledger.KindFix, "robots")
	addRow(t, book, "1", "rev-2", ledger.KindReview, "")

	_, err := admit(brief.KindImpl, lane, book, passes(nil, false))

	var stop *brief.Stopped

	require.ErrorAs(t, err, &stop)
	assert.Contains(t, stop.Error(), "REFUSED: pull request 1 had 2 patch rounds on the mechanism robots, with no design lane since.")
	assert.Contains(t, stop.Error(), "on the opus:stuck model tier by default")
	assert.Contains(t, stop.Error(), "A third patch round is a redesign that nobody wrote down.")

	lane.Mechanism = "title"
	_, err = admit(brief.KindImpl, lane, book, passes(nil, false))
	require.NoError(t, err)
}

func TestAdmit_AThirdPatchRoundIsAdmittedAfterADesignLane(t *testing.T) {
	t.Parallel()

	book := ledger.Open(t.TempDir())
	lane := implLane()
	lane.Mechanism = "robots"
	lane.Handoff = writeFile(t, "prev.md", "# Handoff\n")

	addRow(t, book, "1", "fix-0", ledger.KindFix, "robots")
	addRow(t, book, "1", "rev-1", ledger.KindReview, "")
	addRow(t, book, "1", "fix-0b", ledger.KindFix, "robots")
	addRow(t, book, "1", "rev-2", ledger.KindReview, "")
	addRow(t, book, "1", "des-1", ledger.KindDesign, "robots")

	_, err := admit(brief.KindImpl, lane, book, passes(nil, false))

	require.NoError(t, err)
}

func TestAdmit_ALaneInsideTheOpenSecondPatchRoundIsAdmitted(t *testing.T) {
	t.Parallel()

	book := ledger.Open(t.TempDir())
	lane := implLane()
	lane.Mechanism = "robots"
	lane.Handoff = writeFile(t, "prev.md", "# Handoff\n")

	addRow(t, book, "1", "fix-0", ledger.KindFix, "robots")
	addRow(t, book, "1", "rev-1", ledger.KindReview, "")
	addRow(t, book, "1", "fix-0b", ledger.KindFix, "robots")

	_, err := admit(brief.KindImpl, lane, book, passes(nil, false))

	require.NoError(t, err)
}

func TestAdmit_AnUnknownLaneKindIsRefusedWithTheParseMessage(t *testing.T) {
	t.Parallel()

	lane := implLane()
	lane.Kind = brief.LaneKind("bogus")

	_, err := admit(brief.KindImpl, lane, ledger.Open(t.TempDir()), nil)

	require.Error(t, err)
	assert.Equal(t, "the lane kind 'bogus' is not fix, rework or relay. Use one of them.", err.Error())
}

func TestAdmit_ASuccessorNeedsAHandoffThatExists(t *testing.T) {
	t.Parallel()

	book := ledger.Open(t.TempDir())
	addRow(t, book, "1", "fix-0", ledger.KindFix, "m")

	_, err := admit(brief.KindImpl, implLane(), book, neverCalled(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the lane fix-1 has no handoff, and the ledger already holds a fix lane for 1. Give the path of the previous lane's handoff with the handoff field.")

	lane := implLane()
	lane.Handoff = "/no/such/handoff.md"
	_, err = admit(brief.KindImpl, lane, book, neverCalled(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no handoff at /no/such/handoff.md")
}

func TestAdmit_TheHandoffCheckGetsTheTextTheNameTheTranscriptAndTheCap(t *testing.T) {
	t.Parallel()

	book := ledger.Open(t.TempDir())
	addRow(t, book, "1", "fix-0", ledger.KindFix, "m")

	lane := implLane()
	lane.Handoff = writeFile(t, "prev.md", "the handoff text")
	lane.Transcript = writeFile(t, "lane.jsonl", "{}")
	lane.Cap = 90000

	var got []string

	handoff := func(text, name, transcript string, tokens int) ([]string, bool, error) {
		got = []string{text, name, transcript, strconv.Itoa(tokens)}

		return nil, false, nil
	}

	_, err := admit(brief.KindImpl, lane, book, handoff)

	require.NoError(t, err)
	assert.Equal(t, []string{"the handoff text", lane.Handoff, lane.Transcript, "90000"}, got)
}

func TestAdmit_ATranscriptThatCannotBeReadIsARefusal(t *testing.T) {
	t.Parallel()

	book := ledger.Open(t.TempDir())
	addRow(t, book, "1", "fix-0", ledger.KindFix, "m")

	lane := implLane()
	lane.Handoff = writeFile(t, "prev.md", "x")
	lane.Transcript = writeFile(t, "lane.jsonl", "a\x00b")

	_, err := admit(brief.KindImpl, lane, book, neverCalled(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds a NUL byte")

	lane.Handoff = writeFile(t, "nul.md", "a\x00b")
	_, err = admit(brief.KindImpl, lane, book, neverCalled(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), lane.Handoff+" holds a NUL byte")
}

func TestAdmit_AHandoffThatTheCheckRefusesStopsTheBrief(t *testing.T) {
	t.Parallel()

	book := ledger.Open(t.TempDir())
	addRow(t, book, "1", "fix-0", ledger.KindFix, "m")

	lane := implLane()
	lane.Handoff = writeFile(t, "prev.md", "x")

	_, err := admit(brief.KindImpl, lane, book, passes([]string{"REFUSED: no regressions line"}, true))

	var stop *brief.Stopped

	require.ErrorAs(t, err, &stop)
	assert.Equal(t, []string{"REFUSED: no regressions line"}, stop.Lines)
}

func TestAdmit_AHandoffCheckThatCannotRunIsItsOwnError(t *testing.T) {
	t.Parallel()

	book := ledger.Open(t.TempDir())
	addRow(t, book, "1", "fix-0", ledger.KindFix, "m")

	lane := implLane()
	lane.Handoff = writeFile(t, "prev.md", "x")
	broken := func(string, string, string, int) ([]string, bool, error) {
		return nil, false, &obeecheck.Refusal{Message: "the context cap is not a number"}
	}

	_, err := admit(brief.KindImpl, lane, book, broken)

	var refusal *obeecheck.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "the context cap is not a number", refusal.Message)
}

func TestAdmit_TwoRegressionsSendTheNextBriefToADesignLane(t *testing.T) {
	t.Parallel()

	for regressions, stops := range map[string]bool{"1": false, "2": true, "": false} {
		book := ledger.Open(t.TempDir())
		addRow(t, book, "1", "fix-0", ledger.KindFix, "m")

		lane := implLane()
		lane.Handoff = writeFile(t, "prev.md", "x")
		lines := []string{"OK: handoff", "regressions=" + regressions + " tokens=90000"}

		_, err := admit(brief.KindImpl, lane, book, passes(lines, false))

		if !stops {
			require.NoError(t, err, regressions)

			continue
		}

		var stop *brief.Stopped

		require.ErrorAs(t, err, &stop)
		assert.Contains(t, stop.Error(), "REFUSED: "+lane.Handoff+" counts 2 regressions of pull request 1, and the ledger holds no design lane for it.")
		assert.Contains(t, stop.Error(), "dispatch a design lane next")

		_, err = admit(brief.KindDesign, lane, book, passes(lines, false))
		require.NoError(t, err)

		addRow(t, book, "1", "des-1", ledger.KindDesign, "")
		_, err = admit(brief.KindImpl, lane, book, passes(lines, false))
		require.NoError(t, err)
	}
}

func TestAdmit_ABookThatCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	boom := errors.New("the ledger cannot be read")

	for name, lane := range map[string]brief.Lane{
		"count":  {Key: "1", Acceptance: "none: x", BaseResult: "none: x", Mechanism: "m", NewBehavior: true},
		"rounds": implLane(),
		"prior":  {Key: "1", Acceptance: "none: x", BaseResult: "none: x", Kind: brief.LaneRelay},
	} {
		_, err := admit(brief.KindImpl, lane, brokenBook{boom: boom, fails: name}, nil)

		require.ErrorIs(t, err, boom, name)
	}
}

func TestParseLaneKind_NamesTheClosedSet(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"fix", "rework", "relay"} {
		kind, err := brief.ParseLaneKind(name)

		require.NoError(t, err)
		assert.Equal(t, name, string(kind))
	}

	_, err := brief.ParseLaneKind("other")

	require.Error(t, err)
	assert.Equal(t, "the lane kind 'other' is not fix, rework or relay. Use one of them.", err.Error())
}

// brokenBook fails the call that fails names and answers the others as an empty ledger.
type brokenBook struct {
	boom  error
	fails string
}

func (b brokenBook) Count(string, string) (int, error) {
	return 0, b.failIf("count")
}

func (b brokenBook) Rounds(string, string) (ledger.Rounds, error) {
	return ledger.Rounds{}, b.failIf("rounds")
}

func (b brokenBook) Prior(string, string) (int, error) {
	return 0, b.failIf("prior")
}

func (b brokenBook) Add(ledger.Request) (ledger.Added, error) {
	return ledger.Added{}, b.failIf("add")
}

func (b brokenBook) failIf(call string) error {
	if b.fails == call {
		return b.boom
	}

	return nil
}
