package merge_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

func kids(t *testing.T, base string, numbers ...int) []model.PullRequest {
	t.Helper()

	found := make([]model.PullRequest, len(numbers))
	for i, number := range numbers {
		found[i] = pullJSON(t, map[string]any{"number": number, "baseRefName": base})
	}

	return found
}

func stackCalls(w *world) int {
	count := 0

	for _, call := range w.calls {
		if strings.HasPrefix(call, "StackID") {
			count++
		}
	}

	return count
}

func TestStack_AnOpenChildBlocks(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.children = kids(t, "feat", 7)
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: #5 is the base of #7. A squash merge orphans them.", result.Lines[1])
	assert.Equal(t, []string{"--state", "open", "--base", "feat", "--json", "number,baseRefName"}, w.listed)
	assert.Empty(t, w.writes)
}

func TestStack_NoChildMeansNoChildStackRead(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	assert.Equal(t, merge.Done, w.run(request()).Stop)
	assert.Equal(t, 1, stackCalls(w), "only the native stack guard of the pull request itself")
}

func TestStack_ARowOnAnotherBaseIsNotAChild(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.children = kids(t, "other", 7)

	assert.Equal(t, merge.Done, w.run(request()).Stop)
}

func TestStack_AChildInTheSameStackIsNotedAndDoesNotBlock(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.children = kids(t, "feat", 7)
	w.stacks[5], w.stacks[7] = "S", "S"
	result := w.run(request())

	assert.Contains(t, result.Lines, "note: #7 is the next layer of stack S; GitHub retargets it when #5 merges")
	assert.NotContains(t, strings.Join(result.Lines, "\n"), "is the base of")
	assert.Contains(t, last(result), "REFUSE: #5 is in stack S", "the native stack guard speaks, not the child guard")
}

func TestStack_AChildInAnotherStackStillBlocks(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.children = kids(t, "feat", 7)
	w.stacks[5], w.stacks[7] = "S", "T"

	assert.Contains(t, w.run(request()).Lines, "REFUSE: #5 is the base of #7. A squash merge orphans them.")
}

func TestStack_AChildInNoStackBlocksAParentInAStack(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.children = kids(t, "feat", 7)
	w.stacks[5] = "S"

	assert.Contains(t, w.run(request()).Lines, "REFUSE: #5 is the base of #7. A squash merge orphans them.")
}

func TestStack_AParentInNoStackBlocksEveryChild(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.children = kids(t, "feat", 7, 8)

	assert.Contains(t, w.run(request()).Lines, "REFUSE: #5 is the base of #7 #8. A squash merge orphans them.")
	assert.Equal(t, 1, stackCalls(w), "a parent in no stack reads no child stack")
}

func TestStack_AnUnreadableAnswerRefuses(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		over  map[string]any
		child bool
		fail  string
		own   string
		want  string
	}{
		"no branch name":                {over: map[string]any{"headRefName": nil}, want: "REFUSE: cannot read #5's own branch name, so its children cannot be found"},
		"an empty branch name":          {over: map[string]any{"headRefName": ""}, want: "REFUSE: cannot read #5's own branch name, so its children cannot be found"},
		"the list":                      {fail: "PRList", want: "REFUSE: cannot read the open pull requests to check for stacked children"},
		"the stack of the pull request": {child: true, fail: "StackID 5", want: "REFUSE: cannot read which stack #5 is in"},
		"the stack of a child":          {child: true, own: "S", fail: "StackID 7", want: "REFUSE: cannot read which stack #7 is in"},
	}
	for name, one := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t).set(t, one.over)
			if one.child {
				w.children = kids(t, "feat", 7)
			}

			w.stacks[5] = one.own
			w.fails[one.fail] = true
			result := w.run(request())

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Equal(t, one.want, last(result))
			assert.Empty(t, w.writes)
		})
	}
}

func TestStack_AFullListRefusesAsCutOff(t *testing.T) {
	t.Parallel()

	numbers := make([]int, 1000)
	for i := range numbers {
		numbers[i] = i + 10
	}

	w := newWorld(t)
	w.children = kids(t, "feat", numbers...)
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: cannot read the open pull requests to check for stacked children", last(result))
	assert.Len(t, result.Lines, 3, "the before line, the forge error and the refusal")
}

func TestStack_NineHundredNinetyNineRowsAreNotCutOff(t *testing.T) {
	t.Parallel()

	numbers := make([]int, 999)
	for i := range numbers {
		numbers[i] = i + 10
	}

	w := newWorld(t)
	w.children = kids(t, "feat", numbers...)
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines[1], fmt.Sprintf("#%d", 10+998))
}
