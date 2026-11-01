package merge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/merge"
)

// TestRun_AReadThatFailsRefusesTheMerge fails one read of the forge at a time. A failed read is never an empty answer: the merge does not go on.
func TestRun_AReadThatFailsRefusesTheMerge(t *testing.T) {
	t.Parallel()

	workflow := map[string]any{"files": []any{map[string]any{"path": ".github/workflows/ci.yml"}}}
	cases := map[string]struct {
		set  func(*testing.T, *world) *world
		req  merge.Request
		fail string
	}{
		"the state and head of the pull request": {fail: "PRView", req: request()},
		"the forge count of commits and files":   {fail: "PullCounts", req: request()},
		"the committers of the commits":          {fail: "Committers", req: request()},
		"the checks":                             {fail: "PRChecks", req: request()},
		"the token scopes for a workflow change": {fail: "TokenScopes", req: request(), set: func(t *testing.T, w *world) *world { t.Helper(); return w.set(t, workflow) }},
		"the standing change requests":           {fail: "OpenChangeRequests", req: request()},
		"the unresolved review threads":          {fail: "UnresolvedThreads", req: request()},
		"the reviews and the commits they saw":   {fail: "Reviews", req: request()},
		"the stack of the pull request":          {fail: "StackID 5", req: request()},
		"the open children of the branch":        {fail: "PRList", req: request()},
		"the head again before the merge call":   {fail: "PRView later", req: request()},
		"the tip of the base":                    {fail: "BranchRef", req: request()},
		"the closing references":                 {fail: "", req: audited(12)},
	}

	for label, one := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			if one.set != nil {
				w = one.set(t, w)
			}

			again := pullJSON(t, nil)
			w.later = &again

			if one.fail != "" {
				w.fails[one.fail] = true
			}

			result := w.run(one.req)

			assert.NotEqual(t, merge.Done, result.Stop)
			assert.Empty(t, merges(w))
		})
	}
}
