package flow

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

// hangingBeats is a tracker that never answers: each renewal ends only when its context ends.
type hangingBeats struct{}

func (hangingBeats) Beat(ctx context.Context, _, _ string) (string, error) {
	<-ctx.Done()

	return "", ctx.Err()
}

// cancelledRunWaitingForTheOwner runs the item to its owner wait, cancels it, and lets the tick release it. It returns the harness, the engine and a client.
func cancelledRunWaitingForTheOwner(t *testing.T) (*harness, *Engine, *Client) {
	t.Helper()

	harness := newHarness(t)
	harness.verdicts.reviews = changes(6)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)

	require.NoError(t, client.Cancel(testItem))
	harness.tick(t)

	select {
	case outcome := <-done:
		require.Error(t, outcome.err)
	case <-time.After(eventTimeout):
		t.Fatal("the run did not end after the cancel")
	}

	eventually(t, func() bool { return len(harness.life.releasedBy()) == 1 })

	return harness, engine, client
}

func TestResume_TakesAFreshClaimAndTheRunUsesItsTokenForBeatAndRelease(t *testing.T) {
	t.Parallel()

	harness, _, client := cancelledRunWaitingForTheOwner(t)
	require.Equal(t, []string{"owner-12"}, harness.life.releasedBy(), "the cancel releases the first claim")

	require.NoError(t, client.Resume(testItem))
	assert.Equal(t, 2, countOf(harness.life.seen(), "claim"), "the resume takes a new claim")

	eventually(t, func() bool {
		harness.tick(t)

		return slices.Contains(harness.beats.seen(), testProject+"/owner-12#2")
	})

	require.NoError(t, client.Answer(testItem, "go on"))
	eventually(t, func() bool {
		rows, err := client.Status()

		return err == nil && rows[0].State == runDone
	})

	assert.Equal(t, []string{"owner-12", "owner-12#2"}, harness.life.releasedBy(), "the release uses the fresh token")
}

func TestResume_RefusesWhenTheFreshClaimCannotBeTaken(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	harness.life.mu.Lock()
	harness.life.failing["claim"] = true
	harness.life.mu.Unlock()

	err = client.Resume(testItem)

	require.ErrorContains(t, err, "claim of item 12")
	require.ErrorContains(t, err, "If a canceled run holds the claim, the next obee run frees it. Then run obee resume 12 again")

	rows, err := client.Status()
	require.NoError(t, err)
	assert.Equal(t, runFailed, rows[0].State, "a refused resume leaves the run as it was")
}

func TestResume_GivesTheFreshClaimBackWhenItCannotSaveTheToken(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(harness.opts.StateDir, ownersDir), nil, ownerMode))

	require.ErrorContains(t, client.Resume(testItem), "save the claim of run item-12-r1")
	assert.Equal(t, []string{"owner-12", "owner-12#2"}, harness.life.releasedBy())
}

func TestTakeClaim_ReportsARunWhoseInputIsNotReadable(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)

	_, _, err := client.takeClaim(testItem, run{Status: dbos.WorkflowStatus{ID: "item-12-r1", Input: "not json"}})

	require.ErrorContains(t, err, "read the input of run item-12-r1")
}

func TestOwners_SaveReadAndFindTheTokenOfARun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)
	dir := harness.opts.StateDir

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	owner, err := currentOwner(client.flow, dir, "item-12-r1")
	require.NoError(t, err)
	assert.Equal(t, "owner-12", owner, "with no saved token, the token of the recorded claim step")

	owner, err = currentOwner(engine.ctx, dir, "item-12-r1")
	require.NoError(t, err)
	assert.Equal(t, "owner-12", owner, "the engine reads the same step")

	assert.Empty(t, workflowID(engine.ctx), "outside a workflow there is no run id")
	require.NoError(t, saveOwner(dir, "item-12-r1", "fresh"))

	owner, err = currentOwner(client.flow, dir, "item-12-r1")
	require.NoError(t, err)
	assert.Equal(t, "fresh", owner, "a saved token wins over the recorded one")

	_, err = currentOwner(client.flow, dir, "item-99-r1")
	require.ErrorIs(t, err, errNoOwner)

	require.NoError(t, os.MkdirAll(ownerPath(dir, "item-98-r1"), ownersMode))

	_, err = currentOwner(client.flow, dir, "item-98-r1")
	require.ErrorContains(t, err, "read the claim of run item-98-r1")
	require.ErrorContains(t, saveOwner(dir, "item-98-r1", "x"), "save the claim of run item-98-r1")

	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, ownersDir), nil, ownerMode))
	require.ErrorContains(t, saveOwner(other, "item-12-r1", "x"), "save the claim of run item-12-r1")

	closed, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	_, err = currentOwner(closed.flow, dir, "item-97-r1")
	require.ErrorContains(t, err, "read the steps of item-97-r1")
}

func TestClaimOutput_ReadsOnlyTheJSONTextOfAClaimStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		step dbos.StepInfo
		want string
	}{
		{"a_claim_step", dbos.StepInfo{StepName: "claim", Output: `"tok"`}, "tok"},
		{"another_step", dbos.StepInfo{StepName: "plan", Output: `"tok"`}, ""},
		{"an_output_that_is_not_text", dbos.StepInfo{StepName: "claim", Output: 7}, ""},
		{"an_output_that_is_not_json", dbos.StepInfo{StepName: "claim", Output: "tok"}, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, claimOutput(test.step))
		})
	}
}

func TestGiveBack_ARunThatNeverClaimedHasNothingToRelease(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	require.NoError(t, client.giveBack(testProject, testItem, "item-99-r1"))
	engine.giveBack(t.Context(), openRun{id: "item-99-r1", in: ItemInput{Item: testItem, Project: testProject}})

	require.NoError(t, os.MkdirAll(ownerPath(harness.opts.StateDir, "item-98-r1"), ownersMode))
	require.ErrorContains(t, client.giveBack(testProject, testItem, "item-98-r1"), "read the claim of run item-98-r1")

	assert.Empty(t, harness.life.seen())
}

func TestRun_AClaimTokenThatCannotBeReadEndsTheRunBeforeAnyAgentStarts(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	require.NoError(t, os.MkdirAll(ownerPath(harness.opts.StateDir, "item-12-r1"), ownersMode))

	_, err := engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "read the claim of run item-12-r1")
	assert.Empty(t, harness.agents.seen())
}

func TestAnswer_RefusesARunThatAsksNoQuestion(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	release := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(release)

	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })

	err := client.Answer(testItem, "go on")

	require.ErrorIs(t, err, errWrongState)
	require.ErrorContains(t, err, "asks no question")

	release()
	require.NoError(t, (<-done).err)
}

func TestRun_AReviewWithNoReadableVerdictGetsOneMoreReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		failFirst int
		wantErr   string
		steps     []model.Step
	}{
		{"one_missing_verdict_is_reviewed_again", 1, "", []model.Step{model.StepPlan, model.StepImplement, model.StepReview, model.StepReview}},
		{"two_missing_verdicts_end_the_run", 2, "read the verdict of item 12 after 2 reviews: fake failure", []model.Step{model.StepPlan, model.StepImplement, model.StepReview, model.StepReview}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.verdicts.failFirst = test.failFirst
			engine := harness.engine(t)

			_, err := engine.Run(testItem, testProject)

			if test.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.wantErr)
			}

			assert.Equal(t, test.steps, stepsOf(harness.agents.seen()))
		})
	}
}

func TestRun_AGateThatCannotRunEndsTheRunAndSpendsNoFixRound(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.gate.err = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "gate of item 12")
	assert.Equal(t, []model.Step{model.StepPlan, model.StepImplement}, stepsOf(harness.agents.seen()), "no review and no fix round ran")
	assert.Zero(t, harness.verdicts.count())
}

func TestRun_AGateAndAReviewOnDifferentHeadsEndTheRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.gate.results = []GateResult{{Passed: true, Head: "aaa"}}
	harness.verdicts.reviews = []Review{{Verdict: VerdictApprove, Head: "bbb"}}
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errHeadMoved)
	require.ErrorContains(t, err, `the gate ran on "aaa" and the review read "bbb"`)
	assert.Equal(t, []string{"claim:12", "release:12"}, harness.life.seen(), "the pull request is not marked ready")
}

func TestRun_TheReadyStepRefusesAPullRequestThatMovedAfterTheReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		head string
		want string
	}{
		{"the_head_is_the_reviewed_one", "aaa", ""},
		{"a_push_after_the_review", "ccc", `moved to "ccc" after the review read "aaa"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.gate.results = []GateResult{{Passed: true, Head: "aaa"}}
			harness.verdicts.reviews = []Review{{Verdict: VerdictApprove, Head: "aaa"}}
			harness.forge.state = review.Standing{Number: testPull, Head: test.head}
			engine := harness.engine(t)

			_, err := engine.Run(testItem, testProject)

			if test.want == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, errNotReady)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestRun_AnEmptyLoginIsRefusedBeforeTheClaimAndLeavesNoRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.logins.login = ""
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errEmptyLogin)
	assert.Empty(t, harness.life.seen(), "nothing is claimed")

	rows, err := client.Status()
	require.NoError(t, err)
	assert.Empty(t, rows, "no failed attempt is stored")
}

func TestRun_TheWaitForTheOwnerHasNoDeadline(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.verdicts.reviews = changes(6)
	engine := launchedWith(t, harness, func(deps *Deps) { deps.Clock.OwnerWait = 20 * time.Millisecond })
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)

	before := len(harness.agents.seen())

	time.Sleep(300 * time.Millisecond) // many waits of 20 ms end with no answer

	assert.Len(t, harness.agents.seen(), before, "a wait that timed out is not an answer")

	select {
	case outcome := <-done:
		t.Fatalf("the run ended without an answer: %v", outcome.err)
	default:
	}

	require.NoError(t, client.Answer(testItem, "go on"))

	select {
	case outcome := <-done:
		require.NoError(t, outcome.err)
	case <-time.After(eventTimeout):
		t.Fatal("the run did not end after the answer")
	}
}

func TestTick_AClaimRenewalThatHangsEndsAtItsOwnTimeout(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	release := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(release)

	clk := &leaseClock{now: time.Unix(1_000_000, 0)}

	engine := launchedWith(t, harness, func(deps *Deps) {
		deps.Beats = hangingBeats{}
		deps.Clock.Beat = 20 * time.Millisecond
		clk.install(&deps.Clock)
	})

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })

	clk.advance(trustLimit - time.Second)

	tickEnds := func() {
		ticked := make(chan struct{})

		go func() {
			harness.tick(t)
			close(ticked)
		}()

		select {
		case <-ticked:
		case <-time.After(eventTimeout):
			t.Fatal("one hanging renewal stalled the tick")
		}
	}

	tickEnds()
	assert.NotEmpty(t, engine.holds.snapshot(), "a renewal that timed out before the deadline is tried again")

	clk.advance(time.Second)
	tickEnds()

	assert.Empty(t, engine.holds.snapshot(), "the claim whose renewals timed out until the deadline is marked lost")

	release()

	outcome := <-done
	require.ErrorIs(t, outcome.err, errClaimLost)
	require.ErrorContains(t, outcome.err, "deadline exceeded")
}

func TestCanceledIDs_ReadsOnlyTheRunsThisEngineExecutes(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })
	require.NoError(t, client.Cancel(testItem))

	none, err := engine.canceledIDs(nil)
	require.NoError(t, err)
	assert.Empty(t, none, "an engine with no local run reads nothing")

	other, err := engine.canceledIDs([]string{"item-99-r1"})
	require.NoError(t, err)
	assert.Empty(t, other, "a canceled run that this engine does not execute is not listed")

	local, err := engine.canceledIDs(engine.cancels.ids())
	require.NoError(t, err)
	assert.Equal(t, []string{"item-12-r1"}, local)

	harness.tick(t)

	select {
	case outcome := <-done:
		require.Error(t, outcome.err)
	case <-time.After(eventTimeout):
		t.Fatal("the run did not end after the cancel")
	}
}

// launchOn builds and launches an engine over the harness with the given deps. The cleanup shuts it down; the returned function does it at once.
func launchOn(t *testing.T, harness *harness, deps Deps) func() {
	t.Helper()

	engine, err := NewEngine(t.Context(), harness.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	stop := sync.OnceFunc(func() { require.NoError(t, engine.Shutdown()) })
	t.Cleanup(stop)

	return stop
}

func TestLaunch_DeletesFinishedRunsPastTheRetentionAndKeepsTheOthers(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	harness.agents.mu.Lock()
	harness.agents.err = errFake
	harness.agents.mu.Unlock()

	_, err = first.Run("13", testProject)
	require.Error(t, err)

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	_, err = first.Run(testItem, testProject)
	require.NoError(t, err)
	require.NoError(t, first.Shutdown())

	client := harness.client(t, testVersion)

	stop := launchOn(t, harness, harness.deps())

	rows, err := client.Status()
	require.NoError(t, err)
	assert.Len(t, rows, 2, "a run that finished a moment ago stays")

	stop()

	deps := harness.deps()
	deps.Clock.Retain = time.Nanosecond
	launchOn(t, harness, deps)

	rows, err = client.Status()
	require.NoError(t, err)
	require.Len(t, rows, 1, "the finished run is deleted")
	assert.Equal(t, "item-13-r1", rows[0].ID, "a failed run stays: obee resume can reopen it")
}

func TestRun_ALoginThatFailsInsideTheRunEndsItBeforeTheClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	logins := &loginOnce{later: errFake}
	engine := launchedWith(t, harness, func(deps *Deps) { deps.Logins = logins })

	_, err := engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "pick the login of item 12")
	assert.Empty(t, harness.life.seen(), "nothing is claimed")
}
