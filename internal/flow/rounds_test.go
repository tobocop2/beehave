package flow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/roundcut"
	"github.com/tobocop2/beehave/internal/roundintegration"
	"github.com/tobocop2/beehave/internal/roundsync"
	"github.com/tobocop2/beehave/internal/takeover"
)

var errForge = errors.New("the forge answered 502")

// spyRounds answers each round write with the value that the test sets, and keeps the request of the last call.
type spyRounds struct {
	sync       roundsync.Result
	land       roundsync.Result
	cut        roundcut.Result
	integrate  roundintegration.Result
	shown      string
	take       takeover.Result
	err        error
	syncReq    roundsync.Request
	landReq    roundsync.LandRequest
	cutReq     roundcut.Request
	integrReq  roundintegration.Request
	card       model.Card
	takeReq    TakeRequest
	projectOf  string
	callsCount int
}

func (s *spyRounds) Sync(_ context.Context, project string, req roundsync.Request) (roundsync.Result, error) {
	s.syncReq, s.projectOf, s.callsCount = req, project, s.callsCount+1

	return s.sync, s.err
}

func (s *spyRounds) Land(_ context.Context, _ string, req roundsync.LandRequest) (roundsync.Result, error) {
	s.landReq, s.callsCount = req, s.callsCount+1

	return s.land, s.err
}

func (s *spyRounds) Cut(_ context.Context, _ string, req roundcut.Request) (roundcut.Result, error) {
	s.cutReq, s.callsCount = req, s.callsCount+1

	return s.cut, s.err
}

func (s *spyRounds) Integrate(_ context.Context, _ string, req roundintegration.Request) (roundintegration.Result, error) {
	s.integrReq, s.callsCount = req, s.callsCount+1

	return s.integrate, s.err
}

func (s *spyRounds) Place(_ context.Context, _ string, card model.Card) (string, error) {
	s.card, s.callsCount = card, s.callsCount+1

	return s.shown, s.err
}

func (s *spyRounds) Take(_ context.Context, _ string, req TakeRequest) (takeover.Result, error) {
	s.takeReq, s.callsCount = req, s.callsCount+1

	return s.take, s.err
}

// roundCall is a workflow file whose main makes the call and passes one field of its result to the command seen.
func roundCall(call, field string) string {
	return "def main(item):\n    out = " + call + "\n    command(name = \"seen\", run = [\"seen\", str(out." + field + ")])\n"
}

func runRound(t *testing.T, spy *spyRounds, text string) (*harness, *Engine, ItemResult, error) {
	t.Helper()

	h := newHarness(t)
	h.rounds = spy
	engine := h.engine(t)
	result, err := engine.RunFile(testItem, testProject, writeFlow(t, text))

	return h, engine, result, err
}

func TestRoundSyncStep_PassesTheBaseAndTheTrunkAndReturnsTheFieldsOfTheResult(t *testing.T) {
	t.Parallel()

	spy := &spyRounds{sync: roundsync.Result{State: roundsync.Synced, Branch: "sync/b", PR: 7, Head: "h1", BaseTip: "t1"}}

	h, engine, _, err := runRound(t, spy, roundCall(`round_sync(base = "integration", trunk = "main", name = "s")`, "head"))

	require.NoError(t, err)
	assert.Equal(t, roundsync.Request{Base: "integration", Trunk: "main"}, spy.syncReq)
	assert.Equal(t, []string{"h1"}, seenBy(t, h))
	assert.Contains(t, stepNames(t, engine), "round_sync:s#1")
}

func TestRoundLandStep_PassesThePullRequestTheHeadAndTheBaseTip(t *testing.T) {
	t.Parallel()

	spy := &spyRounds{land: roundsync.Result{State: roundsync.Landed}}

	h, _, _, err := runRound(t, spy, roundCall(`round_land(pr = 7, head = "h1", base_tip = "t1")`, "state"))

	require.NoError(t, err)
	assert.Equal(t, roundsync.LandRequest{PR: 7, Head: "h1", BaseTip: "t1"}, spy.landReq)
	assert.Equal(t, []string{"landed"}, seenBy(t, h))
}

func TestRoundCutStep_TakesTheRomanNumeralOfTheNewBaseAfterItsLastDash(t *testing.T) {
	t.Parallel()

	spy := &spyRounds{cut: roundcut.Result{Stop: roundcut.Done, Lines: []string{"cut"}}}

	h, _, result, err := runRound(t, spy, roundCall(`round_cut(old = "round-iv", new = "round-v", pr = 9, trunk = "main")`, "lines"))

	require.NoError(t, err)
	assert.Nil(t, result.Stopped)
	assert.Equal(t, roundcut.Cut{Roman: "v", OldPR: 9, OldBase: "round-iv", NewBase: "round-v", Trunk: "main"}, spy.cutReq.Cut)
	assert.Equal(t, []string{"[\"cut\"]"}, seenBy(t, h))
}

func TestRoundCutStep_ARefusedCutEndsTheItemWithTheLinesOfTheRefusal(t *testing.T) {
	t.Parallel()

	spy := &spyRounds{cut: roundcut.Result{Stop: roundcut.Refused, Lines: []string{"REFUSE: not merged", "read it"}}}

	_, _, result, err := runRound(t, spy, `def main(item):
    round_cut(old = "round-iv", new = "round-v", pr = 9, trunk = "main")
`)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Equal(t, "REFUSE: not merged\nread it", result.Stopped.Output)
}

func TestRoundCutStep_ACutWithNoLinesRecordsAnEmptyList(t *testing.T) {
	t.Parallel()

	h, _, _, err := runRound(t, &spyRounds{}, roundCall(`round_cut(old = "round-iv", new = "round-v", pr = 9, trunk = "main")`, "lines"))

	require.NoError(t, err)
	assert.Equal(t, []string{"[]"}, seenBy(t, h))
}

func TestIntegrationPullStep_ReturnsTheNumberAndTheStateOfThePullRequest(t *testing.T) {
	t.Parallel()

	spy := &spyRounds{integrate: roundintegration.Result{PR: 9, State: "OPEN"}}

	h, _, _, err := runRound(t, spy, roundCall(`integration_pull(base = "integration", into = "main", pr = 4)`, "number"))

	require.NoError(t, err)
	assert.Equal(t, roundintegration.Request{Base: "integration", Into: "main", PR: 4}, spy.integrReq)
	assert.Equal(t, []string{"9"}, seenBy(t, h))
}

func TestBoardCardStep_APullRequestCardIsPlacedAndReadBack(t *testing.T) {
	t.Parallel()

	spy := &spyRounds{shown: "Done"}

	h, _, result, err := runRound(t, spy, roundCall(`board_card(board = 3, status = "Done", owner = "acme", pr = 7)`, "status"))

	require.NoError(t, err)
	assert.Nil(t, result.Stopped)
	assert.Equal(t, model.Card{Board: 3, Status: "Done", Owner: "acme", Number: 7, Pull: true}, spy.card)
	assert.Equal(t, []string{"Done"}, seenBy(t, h))
}

func TestBoardCardStep_AnIssueCardNamesTheIssueAndNoPullRequest(t *testing.T) {
	t.Parallel()

	spy := &spyRounds{shown: "Done"}

	_, _, _, err := runRound(t, spy, `def main(item):
    board_card(board = 3, status = "Done", issue = 5)
`)

	require.NoError(t, err)
	assert.Equal(t, model.Card{Board: 3, Status: "Done", Number: 5}, spy.card)
}

func TestBoardCardStep_ABoardThatShowsAnotherColumnEndsTheItem(t *testing.T) {
	t.Parallel()

	_, _, result, err := runRound(t, &spyRounds{shown: "Todo"}, `def main(item):
    board_card(board = 3, status = "Done", pr = 7)
`)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Contains(t, result.Stopped.Output, `shows "Todo" for card 7 after the write; wanted "Done"`)
}

func TestBoardCardStep_ACallThatNamesBothOrNeitherOfPrAndIssueIsRefusedBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"both":     `board_card(board = 3, status = "Done", pr = 7, issue = 5)`,
		"neither":  `board_card(board = 3, status = "Done")`,
		"no board": `board_card(board = 0, status = "Done", pr = 7)`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			spy := &spyRounds{}
			_, _, _, err := runRound(t, spy, "def main(item):\n    "+call+"\n")

			require.ErrorIs(t, err, errCardArguments)
			assert.Zero(t, spy.callsCount)
		})
	}
}

func TestPRTakeStep_TheStepPicksTheModeAndTheHoursBecomeDurations(t *testing.T) {
	t.Parallel()

	for step, mode := range map[string]takeover.Mode{"apply": takeover.ModeApply, "take": takeover.ModeTake} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()

			spy := &spyRounds{take: takeover.Result{Lines: []string{"ok"}}}

			h, _, _, err := runRound(t, spy, roundCall(`pr_take(pr = 7, me = "owner", idle = "1", grace = "0.5", step = "`+step+`")`, "code"))

			require.NoError(t, err)
			assert.Equal(t, mode, spy.takeReq.Request.Mode)
			assert.Equal(t, 7, spy.takeReq.Request.PR)
			assert.Equal(t, "owner", spy.takeReq.Me)
			assert.Equal(t, time.Hour, spy.takeReq.Idle)
			assert.Equal(t, 30*time.Minute, spy.takeReq.Grace)
			assert.Equal(t, []string{"0"}, seenBy(t, h))
		})
	}
}

func TestPRTakeStep_ARefusalOfTheTakeEndsTheItemWithItsLines(t *testing.T) {
	t.Parallel()

	spy := &spyRounds{take: takeover.Result{Code: takeover.CodeRule, Lines: []string{"REFUSE: replies"}}}

	_, _, result, err := runRound(t, spy, `def main(item):
    pr_take(pr = 7, me = "owner", idle = "1", grace = "1", step = "take")
`)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, "REFUSE: replies", result.Stopped.Output)
}

func TestPRTakeStep_AnIdleOrGraceThatIsNotANumberOfHoursIsRefusedBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"idle words":    `pr_take(pr = 7, me = "owner", idle = "soon", grace = "1", step = "take")`,
		"grace words":   `pr_take(pr = 7, me = "owner", idle = "1", grace = "soon", step = "take")`,
		"idle negative": `pr_take(pr = 7, me = "owner", idle = "-1", grace = "1", step = "take")`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			spy := &spyRounds{}
			_, _, _, err := runRound(t, spy, "def main(item):\n    "+call+"\n")

			require.ErrorContains(t, err, "must be a number of hours")
			assert.Zero(t, spy.callsCount)
		})
	}
}

func TestRoundSteps_AFailedWriteEndsTheStepWithTheErrorOfTheForge(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"sync":        `round_sync(base = "b", trunk = "main")`,
		"land":        `round_land(pr = 7, head = "h", base_tip = "t")`,
		"cut":         `round_cut(old = "round-i", new = "round-ii", pr = 9, trunk = "main")`,
		"integration": `integration_pull(base = "b", into = "main")`,
		"board":       `board_card(board = 3, status = "Done", pr = 7)`,
		"take":        `pr_take(pr = 7, me = "owner", idle = "1", grace = "1", step = "apply")`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, _, _, err := runRound(t, &spyRounds{err: errForge}, "def main(item):\n    "+call+"\n")

			require.ErrorIs(t, err, errForge)
		})
	}
}

func TestRoundSteps_AKeywordOfTheWrongTypeIsRefusedBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"sync":        `round_sync(base = wrong_text, trunk = "main")`,
		"land":        `round_land(pr = wrong_number, head = "h", base_tip = "t")`,
		"cut":         `round_cut(old = wrong_text, new = "round-ii", pr = 9, trunk = "main")`,
		"integration": `integration_pull(base = wrong_text, into = "main")`,
		"board":       `board_card(board = wrong_number, status = "Done", pr = 7)`,
		"take":        `pr_take(pr = wrong_number, me = "owner", idle = "1", grace = "1", step = "apply")`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			spy := &spyRounds{}
			_, _, _, err := runRound(t, spy, "def main(item):\n    wrong_text = 1\n    wrong_number = \"7\"\n    "+call+"\n")

			require.ErrorContains(t, err, "got ")
			assert.Zero(t, spy.callsCount)
		})
	}
}

func TestRoundSteps_ABuildWithNoRoundWriterFailsTheCall(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"sync":        `round_sync(base = "b", trunk = "main")`,
		"land":        `round_land(pr = 7, head = "h", base_tip = "t")`,
		"cut":         `round_cut(old = "round-i", new = "round-ii", pr = 9, trunk = "main")`,
		"integration": `integration_pull(base = "b", into = "main")`,
		"board":       `board_card(board = 3, status = "Done", pr = 7)`,
		"take":        `pr_take(pr = 7, me = "owner", idle = "1", grace = "1", step = "apply")`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			engine := h.engine(t)
			engine.deps.Rounds = nil

			_, err := engine.RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    "+call+"\n"))

			require.ErrorIs(t, err, errNoRounds)
		})
	}
}

func TestDryRounds_ShowEveryWriteAsDoneAndWriteNothing(t *testing.T) {
	t.Parallel()

	var dry dryRounds

	synced, err := dry.Sync(context.Background(), "p", roundsync.Request{})
	require.NoError(t, err)
	assert.Equal(t, roundsync.Current, synced.State)

	landed, err := dry.Land(context.Background(), "p", roundsync.LandRequest{PR: 7, Head: "h", BaseTip: "t"})
	require.NoError(t, err)
	assert.Equal(t, roundsync.Result{State: roundsync.Landed, PR: 7, Head: "h", BaseTip: "t"}, landed)

	cut, err := dry.Cut(context.Background(), "p", roundcut.Request{})
	require.NoError(t, err)
	assert.Equal(t, roundcut.Done, cut.Stop)

	opened, err := dry.Integrate(context.Background(), "p", roundintegration.Request{})
	require.NoError(t, err)
	assert.Equal(t, dryPull, opened.PR)

	shown, err := dry.Place(context.Background(), "p", model.Card{Status: "Done"})
	require.NoError(t, err)
	assert.Equal(t, "Done", shown)

	taken, err := dry.Take(context.Background(), "p", TakeRequest{})
	require.NoError(t, err)
	assert.Zero(t, taken.Code)
}
