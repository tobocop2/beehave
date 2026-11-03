package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/review"
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
