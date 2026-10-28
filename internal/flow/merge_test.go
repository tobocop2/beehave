package flow

import (
	"context"
	"sync"
	"testing"

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
}

func (f *fakeMerge) Merge(_ context.Context, project, item, head string) (merge.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, project+" "+item+" "+head)

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
	harness.flowIs(t, shipFlow("  merge = true\n"))
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "merge of item 12")
	assert.Empty(t, harness.watch.reads, "the watch never starts")
}

func TestRun_AnyOtherRefusalEndsTheItemWithTheLines(t *testing.T) {
	t.Parallel()

	for _, stop := range []merge.Stop{merge.Refused, merge.NoWorkflowScope} {
		harness := mergeHarness(t, merge.Result{Stop: stop, Lines: []string{"REFUSE: checks are not green: ci=fail", "(above refers to #7)"}})
		harness.flowIs(t, shipFlow("  merge = true\n"))
		engine := harness.engine(t)

		_, err := engine.Run(testItem, testProject)

		require.ErrorIs(t, err, errMergeRefused)
		require.ErrorContains(t, err, "item 12: REFUSE: checks are not green: ci=fail; (above refers to #7)")
		assert.Empty(t, harness.watch.reads, "the watch never starts")
		assert.Equal(t, []string{"claim:12", "ready:12", "release:12"}, harness.life.seen(), "the claim is released")
	}
}
