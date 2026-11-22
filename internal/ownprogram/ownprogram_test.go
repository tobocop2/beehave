package ownprogram_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/ownprogram"
)

const (
	head = "0123456789012345678901234567890123456789"
	tip  = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
)

var errFake = errors.New("fake failure")

func ptr(s string) *string { return &s }

// rig is the fake side of the command: a pull request, the paths it changes, a verdict, and the statuses that the post received.
type rig struct {
	pull    model.PullRequest
	changed []string
	verdict ownprogram.Verdict
	note    string
	posted  []model.CommitStatus
	judged  int
	fail    string
	// refuse is what the owner answers: a confirmation that returns it. shown is the diff that the owner was shown.
	refuse error
	shown  []string
	noAsk  bool
	// open is the open item list of the tracker. pulls and tips are the answers of the later reads of the pull request and the tip: the last one repeats.
	open  []model.Item
	pulls []model.PullRequest
	tips  []string
}

// next returns the answer of the call: the first of the list, which it then drops when another follows.
func next[T any](list *[]T, fallback T) T {
	if len(*list) == 0 {
		return fallback
	}

	first := (*list)[0]
	if len(*list) > 1 {
		*list = (*list)[1:]
	}

	return first
}

func newRig() *rig {
	return &rig{
		pull:    model.PullRequest{HeadRefOid: ptr(head), HeadRefName: ptr("owner/gate-change"), BaseRefName: ptr("main")},
		changed: []string{"Makefile", "scripts/check.sh"},
		verdict: ownprogram.Verdict{Head: head, Key: "k1", Passed: true, Report: "ok"},
	}
}

func (r *rig) deps() ownprogram.Deps {
	deps := r.baseDeps()

	deps.Diff = func(_ context.Context, tipSHA, headSHA string) (string, error) {
		if r.fail == "diff" || tipSHA != tip || headSHA != head {
			return "", errFake
		}

		return "the diff of the head", nil
	}

	if !r.noAsk {
		deps.Confirm = func(_ context.Context, _ int, _, diff string) error {
			r.shown = append(r.shown, diff)

			return r.refuse
		}
	}

	return deps
}

func (r *rig) baseDeps() ownprogram.Deps {
	return ownprogram.Deps{
		Pull: func(context.Context, int) (model.PullRequest, error) {
			if r.fail == "pull" {
				return model.PullRequest{}, errFake
			}

			return next(&r.pulls, r.pull), nil
		},
		OpenItems: r.openItems,
		Tip:       r.tip,
		Changed: func(_ context.Context, tipSHA, headSHA string) ([]string, error) {
			if r.fail == "changed" {
				return nil, errFake
			}

			if tipSHA != tip || headSHA != head {
				return nil, errFake
			}

			return r.changed, nil
		},
		Judge: func(_ context.Context, headSHA string) (ownprogram.Verdict, error) {
			r.judged++

			if r.fail == "judge" || headSHA != head {
				return ownprogram.Verdict{}, errFake
			}

			return r.verdict, nil
		},
		Post: func(_ context.Context, statuses []model.CommitStatus) (string, error) {
			if r.fail == "post" {
				return "", errFake
			}

			r.posted = append(r.posted, statuses...)

			return r.note, nil
		},
	}
}

func (r *rig) openItems(context.Context) ([]model.Item, error) {
	if r.fail == "items" {
		return nil, errFake
	}

	return r.open, nil
}

func (r *rig) tip(_ context.Context, base string) (string, error) {
	if r.fail == "tip" || base != "main" {
		return "", errFake
	}

	return next(&r.tips, tip), nil
}

func request(items ...string) ownprogram.Request {
	return ownprogram.Request{Project: model.Project{BaseBranch: "main", GateProgram: []string{"Makefile", "scripts/"}}, PR: 7, ItemBranches: items}
}

func TestRun_PostsTheGateStatusOnTheHeadWithTheOwnerCommandText(t *testing.T) {
	t.Parallel()

	r := newRig()

	out, err := ownprogram.Run(t.Context(), r.deps(), request("12"))

	require.NoError(t, err)
	assert.True(t, out.Passed)
	assert.Equal(t, head, out.Head)
	assert.Equal(t, []model.CommitStatus{{SHA: head, Context: "beehave/gate", State: model.StatusSuccess, Description: "gate program changed, run by owner command, gate key k1"}}, r.posted)
}

func TestRun_AGateWithNoKeyPostsTheDescriptionAlone(t *testing.T) {
	t.Parallel()

	r := newRig()
	r.verdict.Key = ""

	_, err := ownprogram.Run(t.Context(), r.deps(), request())

	require.NoError(t, err)
	require.Len(t, r.posted, 1)
	assert.Equal(t, ownprogram.Description, r.posted[0].Description)
	assert.Equal(t, "gate program changed, run by owner command", ownprogram.Description)
}

func TestRun_AFailedGatePostsAFailureStatus(t *testing.T) {
	t.Parallel()

	r := newRig()
	r.verdict.Passed, r.verdict.Report = false, "lint failed"

	out, err := ownprogram.Run(t.Context(), r.deps(), request())

	require.NoError(t, err)
	assert.False(t, out.Passed)
	assert.Equal(t, "lint failed", out.Report)
	require.Len(t, r.posted, 1)
	assert.Equal(t, model.StatusFailure, r.posted[0].State)
}

func TestRun_RefusesAPullRequestOnTheBranchOfAnItemBeforeTheGateRuns(t *testing.T) {
	t.Parallel()

	r := newRig()
	r.pull.HeadRefName = ptr("12")

	_, err := ownprogram.Run(t.Context(), r.deps(), request("3", "12"))

	require.ErrorIs(t, err, ownprogram.ErrItemBranch)
	assert.Zero(t, r.judged, "the gate must not run")
	assert.Empty(t, r.posted)
}

func TestRun_RefusesAChangeOutsideTheListedPathsAndNamesIt(t *testing.T) {
	t.Parallel()

	r := newRig()
	r.changed = []string{"Makefile", "internal/gate/gate.go", "scriptsx/a.sh"}

	_, err := ownprogram.Run(t.Context(), r.deps(), request())

	require.ErrorIs(t, err, ownprogram.ErrOutsideProgram)
	assert.Contains(t, err.Error(), "internal/gate/gate.go scriptsx/a.sh")
	assert.NotContains(t, err.Error(), "Makefile")
	assert.Zero(t, r.judged, "the gate must not run")
	assert.Empty(t, r.posted)
}

func TestRun_TheListOfChangedPathsRefusesInAnyOrderOnARepeatAndInsideAName(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"the outside path comes first":    {"main.go", "Makefile", "scripts/a.sh"},
		"the outside path comes last":     {"Makefile", "scripts/a.sh", "main.go"},
		"the outside path repeats":        {"Makefile", "main.go", "Makefile", "main.go"},
		"a listed name inside a name":     {"Makefile.bak"},
		"a listed folder inside a name":   {"my-scripts/a.sh"},
		"the folder name without a slash": {"scripts"},
	}

	for name, changed := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := newRig()
			r.changed = changed

			_, err := ownprogram.Run(t.Context(), r.deps(), request())

			require.ErrorIs(t, err, ownprogram.ErrOutsideProgram)
			assert.Zero(t, r.judged)
			assert.Empty(t, r.posted)
		})
	}
}

func TestRun_AProjectWithNoGateProgramRefusesEveryChange(t *testing.T) {
	t.Parallel()

	r := newRig()

	_, err := ownprogram.Run(t.Context(), r.deps(), ownprogram.Request{Project: model.Project{BaseBranch: "main"}, PR: 7})

	require.ErrorIs(t, err, ownprogram.ErrOutsideProgram)
}

func TestRun_RefusesAPullRequestWhoseHeadBranchOrBaseTheForgeDidNotTell(t *testing.T) {
	t.Parallel()

	for name, edit := range map[string]func(*model.PullRequest){
		"head":   func(p *model.PullRequest) { p.HeadRefOid = nil },
		"branch": func(p *model.PullRequest) { p.HeadRefName = nil },
		"base":   func(p *model.PullRequest) { p.BaseRefName = nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := newRig()
			edit(&r.pull)

			_, err := ownprogram.Run(t.Context(), r.deps(), request())

			require.ErrorIs(t, err, ownprogram.ErrUnreadPull)
			assert.Zero(t, r.judged)
		})
	}
}

func TestRun_AFailedReadOrRunOrPostIsAnError(t *testing.T) {
	t.Parallel()

	for _, step := range []string{"pull", "items", "tip", "changed", "diff", "judge", "post"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()

			r := newRig()
			r.fail = step

			_, err := ownprogram.Run(t.Context(), r.deps(), request())

			require.ErrorIs(t, err, errFake)
		})
	}
}

func TestRun_RefusesTheBranchOfAnItemInEveryFormThatTheEngineRuleCouldProduce(t *testing.T) {
	t.Parallel()

	for _, branch := range []string{"bh-12", "BH-12", "refs/heads/bh-12", "heads/bh-12", " bh-12 ", "bh-12 ", "bh-12-gate", "bh-12/gate", "Refs/Heads/BH-12-x"} {
		t.Run(branch, func(t *testing.T) {
			t.Parallel()

			r := newRig()
			r.pull.HeadRefName = ptr(branch)

			_, err := ownprogram.Run(t.Context(), r.deps(), request("bh-3", "bh-12"))

			require.ErrorIs(t, err, ownprogram.ErrItemBranch)
			assert.Zero(t, r.judged)
			assert.Empty(t, r.posted)
		})
	}
}

func TestRun_LeavesABranchFreeThatOnlyLooksLikeTheBranchOfAnItem(t *testing.T) {
	t.Parallel()

	for _, branch := range []string{"bh-123", "bh-1x", "xbh-12", "owner/gate-change", "bh"} {
		t.Run(branch, func(t *testing.T) {
			t.Parallel()

			r := newRig()
			r.pull.HeadRefName = ptr(branch)

			_, err := ownprogram.Run(t.Context(), r.deps(), request("bh-1", "bh-12", ""))

			require.NoError(t, err)
		})
	}
}

func TestRun_RefusesAPullRequestIntoABranchThatIsNotTheBaseOfTheProject(t *testing.T) {
	t.Parallel()

	r := newRig()
	r.pull.BaseRefName = ptr("evil")
	r.changed = []string{"scripts/check.sh"}

	_, err := ownprogram.Run(t.Context(), r.deps(), request())

	require.ErrorIs(t, err, ownprogram.ErrWrongBase)
	assert.Contains(t, err.Error(), "targets evil and the base branch is main")
	assert.Zero(t, r.judged)
	assert.Empty(t, r.posted)
}

func TestRun_ComparesTheHeadWithTheBaseBranchOfTheProjectAndNotWithTheBaseOfThePullRequest(t *testing.T) {
	t.Parallel()

	var read []string

	r := newRig()
	r.pull.BaseRefName = ptr("main")
	deps := r.deps()
	deps.Tip = func(_ context.Context, base string) (string, error) {
		read = append(read, base)

		return tip, nil
	}

	_, err := ownprogram.Run(t.Context(), deps, request())

	require.NoError(t, err)
	assert.Equal(t, []string{"main", "main"}, read, "the tip is read from the base branch of the project, before the paths and again before the post")
}

func TestRun_RefusesTheBranchOfAnOpenItemOfTheTrackerThatNoRunNames(t *testing.T) {
	t.Parallel()

	for _, branch := range []string{"382", "382-gate", "refs/heads/382"} {
		t.Run(branch, func(t *testing.T) {
			t.Parallel()

			r := newRig()
			r.open = []model.Item{{ID: "17"}, {ID: "382"}}
			r.pull.HeadRefName = ptr(branch)

			_, err := ownprogram.Run(t.Context(), r.deps(), request())

			require.ErrorIs(t, err, ownprogram.ErrItemBranch)
			assert.Zero(t, r.judged)
			assert.Empty(t, r.posted)
		})
	}
}

func TestRun_AnItemListThatTheTrackerCannotGiveRefusesBeforeAnyPathIsRead(t *testing.T) {
	t.Parallel()

	r := newRig()
	r.fail = "items"

	_, err := ownprogram.Run(t.Context(), r.deps(), request())

	require.ErrorIs(t, err, errFake)
	assert.Contains(t, err.Error(), "read the open items of the project")
	assert.Empty(t, r.shown, "no diff is shown")
	assert.Zero(t, r.judged)
	assert.Empty(t, r.posted)
}

func TestRun_RefusesToPostWhenTheBaseOrTheHeadMovedWhileTheGateRan(t *testing.T) {
	t.Parallel()

	moved := model.PullRequest{HeadRefOid: ptr("9999999999999999999999999999999999999999"), HeadRefName: ptr("owner/gate-change"), BaseRefName: ptr("main")}
	retargeted := model.PullRequest{HeadRefOid: ptr(head), HeadRefName: ptr("owner/gate-change"), BaseRefName: ptr("evil")}

	for name, edit := range map[string]func(*rig){
		"the base tip moved":   func(r *rig) { r.tips = []string{tip, "1111111111111111111111111111111111111111"} },
		"the head moved":       func(r *rig) { r.pulls = []model.PullRequest{r.pull, moved} },
		"the base was changed": func(r *rig) { r.pulls = []model.PullRequest{r.pull, retargeted} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := newRig()
			edit(r)

			_, err := ownprogram.Run(t.Context(), r.deps(), request())

			require.Error(t, err)
			assert.Equal(t, 1, r.judged, "the gate ran")
			assert.Empty(t, r.posted, "nothing is posted for a head or a base that moved")
		})
	}
}

func TestRun_TheMoveOfTheBaseOrTheHeadIsNamedAndASecondFailedReadOfTheTipIsAnError(t *testing.T) {
	t.Parallel()

	r := newRig()
	r.tips = []string{tip, "1111111111111111111111111111111111111111"}

	_, err := ownprogram.Run(t.Context(), r.deps(), request())
	require.ErrorIs(t, err, ownprogram.ErrMoved)
	assert.Contains(t, err.Error(), "the base branch main moved from "+tip+" to 1111111111111111111111111111111111111111")

	r = newRig()
	r.pulls = []model.PullRequest{r.pull, {HeadRefOid: ptr("9999999999999999999999999999999999999999"), HeadRefName: ptr("x"), BaseRefName: ptr("main")}}

	_, err = ownprogram.Run(t.Context(), r.deps(), request())
	require.ErrorIs(t, err, ownprogram.ErrMoved)
	assert.Contains(t, err.Error(), "the head moved from "+head)

	r = newRig()
	deps := r.deps()
	reads := 0
	deps.Tip = func(context.Context, string) (string, error) {
		reads++
		if reads > 1 {
			return "", errFake
		}

		return tip, nil
	}

	_, err = ownprogram.Run(t.Context(), deps, request())
	require.ErrorIs(t, err, errFake)
	assert.Empty(t, r.posted)
}

func TestRun_ShowsTheDiffToTheOwnerBeforeTheGateRunsAndStopsWhenTheOwnerRefuses(t *testing.T) {
	t.Parallel()

	r := newRig()
	r.refuse = ownprogram.ErrNotConfirmed

	_, err := ownprogram.Run(t.Context(), r.deps(), request())

	require.ErrorIs(t, err, ownprogram.ErrNotConfirmed)
	assert.Equal(t, []string{"the diff of the head"}, r.shown)
	assert.Zero(t, r.judged, "the gate must not run before the owner confirms")
	assert.Empty(t, r.posted)
}

func TestRun_ShowsTheDiffBeforeItPosts(t *testing.T) {
	t.Parallel()

	r := newRig()

	_, err := ownprogram.Run(t.Context(), r.deps(), request())

	require.NoError(t, err)
	assert.Equal(t, []string{"the diff of the head"}, r.shown)
}

func TestRun_AnEffectWithNoConfirmationRefusesToRun(t *testing.T) {
	t.Parallel()

	r := newRig()
	r.noAsk = true

	_, err := ownprogram.Run(t.Context(), r.deps(), request())

	require.ErrorIs(t, err, ownprogram.ErrNotConfirmed)
	assert.Zero(t, r.judged)
	assert.Empty(t, r.posted)
}

func TestRun_AStatusThatTheWriterSkippedIsAnErrorWithItsNote(t *testing.T) {
	t.Parallel()

	r := newRig()
	r.note = "no writer"

	_, err := ownprogram.Run(t.Context(), r.deps(), request())

	require.ErrorIs(t, err, ownprogram.ErrNotPosted)
	assert.Contains(t, err.Error(), "no writer")
}
