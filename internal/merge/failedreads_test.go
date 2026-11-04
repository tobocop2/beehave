package merge_test

import (
	"strings"
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
		says string
	}{
		"the state and head of the pull request": {fail: "PRView", req: request(), says: "for its state"},
		"the forge count of commits and files":   {fail: "PullCounts", req: request(), says: "cannot be counted"},
		"the committers of the commits":          {fail: "Committers", req: request(), says: "committers of"},
		"the checks":                             {fail: "PRChecks", req: request(), says: "cannot read the checks"},
		"the token scopes for a workflow change": {fail: "TokenScopes", req: request(), says: "cannot read the token scopes", set: func(t *testing.T, w *world) *world { t.Helper(); return w.set(t, workflow) }},
		"the standing change requests":           {fail: "OpenChangeRequests", req: request(), says: "standing change request cannot be ruled out"},
		"the unresolved review threads":          {fail: "UnresolvedThreads", req: request(), says: "review threads"},
		"the reviews and the commits they saw":   {fail: "Reviews", req: request(), says: "reviews of"},
		"the stack of the pull request":          {fail: "StackID 5", req: request(), says: "which stack"},
		"the open children of the branch":        {fail: "PRList", req: request(), says: "open pull requests"},
		"the head again before the merge call":   {fail: "PRView later", req: request(), says: "again"},
		"the tip of the base":                    {fail: "BranchRef", req: request(), says: "cannot read the tip of"},
		"the closing references":                 {fail: "", req: audited(12), says: "issues that"},
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
			assert.Contains(t, strings.Join(result.Lines, "\n"), one.says, "the refusal names the read that failed")
		})
	}
}
