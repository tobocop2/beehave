package flow

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/review"
)

const gatedHead = "0123456789abcdef0123456789abcdef01234567"

// fakeMerge answers each merge with result and err, and records what it was asked.
type fakeMerge struct {
	mu     sync.Mutex
	result merge.Result
	err    error
	asked  []string
	given  []MergeSettings
	polls  []merge.Result
	onPoll func()
	lasts  []bool
	// disarms records the items whose queued merge was turned off; disarmErr is the answer of each call.
	disarms   []string
	disarmErr error
	// over replaces result and err when set. A test sets it to run the real merge over a forge fake.
	over func(ctx context.Context, head string, settings MergeSettings) (merge.Result, error)
}

// Poll answers the reads of a queued merge: each read takes the next result of polls, and the last one repeats. lasts records the last flag of each read.
func (f *fakeMerge) Poll(_ context.Context, _, _, _ string, _ MergeSettings, last bool) (merge.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lasts = append(f.lasts, last)

	if f.onPoll != nil {
		f.onPoll()
	}

	if len(f.polls) == 0 {
		return merge.Result{}, errFake
	}

	answer := f.polls[min(len(f.lasts), len(f.polls))-1]

	return answer, nil
}

// askedCount is the number of merge calls so far.
func (f *fakeMerge) askedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.asked)
}

// snapshot is the items whose queued merge was turned off so far.
func (f *fakeMerge) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.disarms)
}

func (f *fakeMerge) Disarm(ctx context.Context, _, item, _ string, _ MergeSettings) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	// A real forge call on a done context fails at once, so the fake does too.
	if err := ctx.Err(); err != nil {
		return err
	}

	f.disarms = append(f.disarms, item)

	return f.disarmErr
}

func (f *fakeMerge) Merge(ctx context.Context, project, item, head string, settings MergeSettings) (merge.Result, error) {
	f.mu.Lock()
	f.asked = append(f.asked, project+" "+item+" "+head)
	f.given = append(f.given, settings)
	f.mu.Unlock()

	if f.over != nil {
		return f.over(ctx, head, settings)
	}

	return f.result, f.err
}

// mergeHarness is a harness whose gate, review and forge all see gatedHead, with a merger in the dependencies.
func mergeHarness(t *testing.T, result merge.Result) *harness {
	t.Helper()

	harness := newHarness(t)
	harness.gate.results = []GateResult{{Passed: true, Head: gatedHead}}
	harness.forge.state = review.Standing{Number: testPull, Head: gatedHead}
	harness.merge = &fakeMerge{result: result}

	return harness
}

func TestRun_AMergeThatCannotBeAskedEndsTheItemAndNamesTheStep(t *testing.T) {
	t.Parallel()

	harness := mergeHarness(t, merge.Result{})
	harness.merge.err = errFake
	harness.flowIs(t, shipFlow("merge = True, "))
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "merge of item 12")
	assert.Empty(t, harness.watch.reads, "the watch never starts")
}

func queuedHarness(t *testing.T, polls ...merge.Result) (*harness, *repoHost) {
	t.Helper()

	harness := mergeHarness(t, merge.Result{Stop: merge.Refused, Reason: merge.Queued, Lines: []string{"queued"}})
	harness.merge.polls = polls
	host := &repoHost{}
	harness.batch = host
	harness.flowIs(t, shipFlow("merge = True, "))

	return harness, host
}

func TestRun_AQueuedMergeIsReadAfterDurableSleepsWithoutQueueingAgain(t *testing.T) {
	t.Parallel()

	queued := merge.Result{Stop: merge.Refused, Reason: merge.Queued}
	harness, host := queuedHarness(t, queued, queued, merge.Result{Stop: merge.Done})
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Len(t, harness.merge.asked, 1, "the merge is queued once; the reads are polls")
	assert.Equal(t, []bool{false, false, false}, harness.merge.lasts)
	assert.Equal(t, []string{testItem}, host.intents, "the intent is written before the merge call")
	require.NotNil(t, harness.merge.given[0].Intent)
}

func TestRun_AQueuedMergeThatNeverMergesGetsALastReadThenEndsTheItem(t *testing.T) {
	t.Parallel()

	harness, _ := queuedHarness(t, merge.Result{Stop: merge.Refused, Reason: merge.Queued})
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errMergeRefused)
	assert.Len(t, harness.merge.lasts, queuedReads)
	assert.True(t, harness.merge.lasts[queuedReads-1], "the last read says it is the last")
	assert.NotContains(t, harness.merge.lasts[:queuedReads-1], true)
}

func TestRun_AQueuedReadThatFailsEndsTheItem(t *testing.T) {
	t.Parallel()

	harness, _ := queuedHarness(t)
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "merge of item 12")
}

func TestRun_ACancelDuringTheWaitForAQueuedMergeEndsTheRun(t *testing.T) {
	t.Parallel()

	harness, _ := queuedHarness(t, merge.Result{Stop: merge.Refused, Reason: merge.Queued})
	harness.sleep = 2 * time.Second

	done := make(chan struct{})

	harness.merge.onPoll = func() {
		client := harness.client(t, testVersion)

		go func() {
			defer close(done)

			time.Sleep(300 * time.Millisecond)

			if err := client.Cancel(testItem); err != nil {
				t.Error(err)
			}

			harness.tick(t)
		}()
	}
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	<-done

	require.Error(t, err)
	assert.Less(t, len(harness.merge.lasts), queuedReads)
}

func TestAwaitQueued_ACancelDuringTheWaitTurnsOffTheQueuedMerge(t *testing.T) {
	t.Parallel()

	harness, _ := queuedHarness(t, merge.Result{Stop: merge.Refused, Reason: merge.Queued})
	harness.sleep = 2 * time.Second

	done := make(chan struct{})

	harness.merge.onPoll = func() {
		client := harness.client(t, testVersion)

		go func() {
			defer close(done)

			time.Sleep(300 * time.Millisecond)

			if err := client.Cancel(testItem); err != nil {
				t.Error(err)
			}

			harness.tick(t)
		}()
	}
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	<-done

	require.Error(t, err)
	assert.Equal(t, []string{testItem}, harness.merge.disarms, "the queued merge is turned off once, after the cancel")
}

func TestAwaitQueued_AStepErrorTurnsOffTheQueuedMerge(t *testing.T) {
	t.Parallel()

	harness, _ := queuedHarness(t)
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
	assert.Equal(t, []string{testItem}, harness.merge.disarms)
}

func TestAwaitQueued_ADisarmThatFailsSaysTheMergeMayStillBeArmed(t *testing.T) {
	t.Parallel()

	harness, _ := queuedHarness(t)
	harness.merge.disarmErr = errors.New("cannot turn off #5")
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake, "the error of the wait stays the cause")
	require.ErrorContains(t, err, "item "+testItem+" may still be armed")
	require.ErrorContains(t, err, "cannot turn off #5")
}

func TestAwaitQueued_AMergedResultCallsNoDisarm(t *testing.T) {
	t.Parallel()

	harness, _ := queuedHarness(t, merge.Result{Stop: merge.Done})
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Empty(t, harness.merge.disarms)
}

func TestAwaitQueued_AReadThatRefusesCallsNoSecondDisarm(t *testing.T) {
	t.Parallel()

	harness, _ := queuedHarness(t, merge.Result{Stop: merge.Refused, Lines: []string{"REFUSE: head moved"}})
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errMergeRefused)
	assert.Empty(t, harness.merge.disarms, "the read that refused has already turned the merge off")
}

func TestEndsTheMerge_OnlyAnEngineStopKeepsTheQueuedMerge(t *testing.T) {
	t.Parallel()

	stop := errors.New("the engine is shutting down")
	owner, cancel := context.WithCancelCause(t.Context())
	cancel(context.Canceled)
	stopped, halt := context.WithCancelCause(t.Context())
	halt(stop)
	expired, expire := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))

	t.Cleanup(expire)

	assert.True(t, endsTheMerge(t.Context()), "a live context means the error is a failure of the run")
	assert.True(t, endsTheMerge(owner), "an owner cancel ends the run")
	assert.True(t, endsTheMerge(expired), "a timeout of the run ends it")
	assert.False(t, endsTheMerge(stopped), "an engine stop resumes the run at the same round")
}

func TestAbandoned_AnEngineStopLeavesTheQueuedMergeArmed(t *testing.T) {
	t.Parallel()

	stopped, halt := context.WithCancelCause(t.Context())
	halt(errors.New("the engine is shutting down"))

	fake := &fakeMerge{}
	engine := &Engine{deps: Deps{Merge: fake}}

	err := engine.abandoned(stopped, queuedRead{item: testItem}, errFake)

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, fake.disarms, "the run resumes at the same round and the merge stays armed")
}

func TestAbandoned_TheDisarmOfAnOwnerCancelRunsOnAContextThatTheCancelDoesNotReach(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancelCause(t.Context())
	cancel(context.Canceled)

	fake := &fakeMerge{} // Its Disarm fails on a done context, as the forge call does.
	engine := &Engine{deps: Deps{Merge: fake}}

	err := engine.abandoned(canceled, queuedRead{item: testItem}, errFake)

	require.ErrorIs(t, err, errFake)
	assert.NotContains(t, err.Error(), "may still be armed", "the disarm got a live context")
	assert.Equal(t, []string{testItem}, fake.disarms)
}

func TestRun_AnIntentThatCannotBeReadEndsTheItemBeforeTheMerge(t *testing.T) {
	t.Parallel()

	harness, host := queuedHarness(t)
	host.intentErr = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, harness.merge.asked, "nothing was queued")
}

func TestRun_APullRequestThatIsNotOpenGetsNoIntentAndTheMergeRefusesIt(t *testing.T) {
	t.Parallel()

	harness, host := queuedHarness(t)
	host.notOpen = map[string]bool{testItem: true}
	harness.merge.result = merge.Result{Stop: merge.Refused, Lines: []string{"REFUSE: state is MERGED"}}
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errMergeRefused)
	assert.Nil(t, harness.merge.given[0].Intent)
}

func TestRun_ARefusedQueuedReadEndsTheItemWithItsLines(t *testing.T) {
	t.Parallel()

	harness, _ := queuedHarness(t, merge.Result{Stop: merge.Refused, Lines: []string{"REFUSE: head moved"}})
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errMergeRefused)
	require.ErrorContains(t, err, "REFUSE: head moved")
	assert.Len(t, harness.merge.lasts, 1)
}

func TestRun_AnyOtherRefusalEndsTheItemWithTheLines(t *testing.T) {
	t.Parallel()

	for _, stop := range []merge.Stop{merge.Refused, merge.NoWorkflowScope} {
		harness := mergeHarness(t, merge.Result{Stop: stop, Lines: []string{"REFUSE: checks are not green: ci=fail", "(above refers to #7)"}})
		harness.flowIs(t, shipFlow("merge = True, "))
		engine := harness.engine(t)

		_, err := engine.Run(testItem, testProject)

		require.ErrorIs(t, err, errMergeRefused)
		require.ErrorContains(t, err, "item 12: REFUSE: checks are not green: ci=fail; (above refers to #7)")
		assert.Empty(t, harness.watch.reads, "the watch never starts")
		assert.Equal(t, []string{"claim:12", "ready:12", "release:12"}, harness.life.seen(), "the claim is released")
	}
}
