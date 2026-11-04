package cli_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/flow"
)

// waiting returns the status lines of runs that wait: item, project and question.
func waiting(item, project, question string) flow.RunInfo {
	return flow.RunInfo{ID: "item-" + item + "-r1", Item: item, Project: project, State: "running", Question: question}
}

func manyRig(t *testing.T) *flowRig {
	t.Helper()

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{
		waiting("12", "p", "Ship the release?"),
		waiting("13", "p", "Ship the hotfix?"),
		waiting("14", "q", "Ship the release?"),
		{ID: "item-15-r1", Item: "15", Project: "p", State: "running"},
		{ID: "item-16-r1", Item: "16", Project: "p", State: "done", Question: "Ship the release?"},
	}

	return rig
}

func TestAnswerAll_AnswersEveryWaitingRunAndPrintsOneLineForEach(t *testing.T) {
	t.Parallel()

	rig := manyRig(t)

	code, stdout, stderr := rig.execute(t.Context(), "answer", "--all", "yes", "go")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, []string{"answer 12 yes go", "answer 13 yes go", "answer 14 yes go"}, rig.steering.calls)
	assert.Equal(t, "12\tanswered\n13\tanswered\n14\tanswered\n", stdout)
}

func TestAnswerAll_TheSelectorsNarrowTheRuns(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		args  []string
		calls []string
	}{
		"project":          {[]string{"--project", "p"}, []string{"answer 12 ok", "answer 13 ok"}},
		"question":         {[]string{"--question", "release"}, []string{"answer 12 ok", "answer 14 ok"}},
		"items":            {[]string{"--items", "13,14"}, []string{"answer 13 ok", "answer 14 ok"}},
		"all three":        {[]string{"--project", "p", "--question", "release", "--items", "12,13"}, []string{"answer 12 ok"}},
		"as a named voice": {[]string{"--as", "ana", "--project", "q"}, []string{"answer-as ana 14 ok"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := manyRig(t)

			code, _, stderr := rig.execute(t.Context(), append([]string{"answer", "--all"}, append(tc.args, "ok")...)...)
			require.Equal(t, 0, code, stderr)

			assert.Equal(t, tc.calls, rig.steering.calls)
		})
	}
}

func TestAnswerAll_ARefusedRunDoesNotStopTheOthersAndTheExitCodeSaysSo(t *testing.T) {
	t.Parallel()

	rig := manyRig(t)
	rig.steering.answerErrs = map[string]error{"13": errors.New("fake\n  failure")}

	code, stdout, stderr := rig.execute(t.Context(), "answer", "--all", "yes")

	assert.Equal(t, 1, code)
	assert.Equal(t, []string{"answer 12 yes", "answer 13 yes", "answer 14 yes"}, rig.steering.calls)
	assert.Equal(t, "12\tanswered\n13\trefused: fake failure\n14\tanswered\n", stdout)
	assert.Contains(t, stderr, "1 of 3 runs did not take the answer")
}

func TestAnswerAll_RefusesWhenNoWaitingRunMatches(t *testing.T) {
	t.Parallel()

	rig := manyRig(t)

	code, _, stderr := rig.execute(t.Context(), "answer", "--all", "--project", "none", "yes")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "no waiting run matches")
	assert.Empty(t, rig.steering.calls)
}

func TestAnswerAll_RefusesAMisplacedSelectorOrMissingText(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"selector without all": {"answer", "--project", "p", "yes"},
		"all with no text":     {"answer", "--all"},
		"item before text":     {"answer", "--all", "12", "yes"},
		"empty items":          {"answer", "--all", "--items", "", "yes"},
		"empty project":        {"answer", "--all", "--project", "", "yes"},
		"empty question":       {"answer", "--all", "--question", "", "yes"},
		"empty without all":    {"answer", "--project", "", "yes"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := manyRig(t)

			code, _, stderr := rig.execute(t.Context(), args...)

			assert.Equal(t, 1, code, stderr)
			assert.Empty(t, rig.steering.calls)
		})
	}
}

func TestAnswerAll_AFailedStatusReadAnswersNothing(t *testing.T) {
	t.Parallel()

	rig := manyRig(t)
	rig.steering.err = errFake

	code, _, stderr := rig.execute(t.Context(), "answer", "--all", "yes")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")
	assert.Empty(t, rig.steering.calls)
}

func TestAnswerAll_RefusesABadItemInTheList(t *testing.T) {
	t.Parallel()

	rig := manyRig(t)

	code, _, stderr := rig.execute(t.Context(), "answer", "--all", "--items", "12,not an item", "yes")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "invalid item")
	assert.Empty(t, rig.steering.calls)
}
