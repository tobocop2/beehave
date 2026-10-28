package preflight_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/preflight"
)

var (
	head  = strings.Repeat("1", 40)
	other = strings.Repeat("2", 40)

	errDown = errors.New("down")
)

// fake serves one pull request. The view is the JSON that gh prints; the branch is gone or not.
type fake struct {
	view       string
	viewErr    error
	branchGone bool
	branchErr  error
	fields     []string
	branches   []string
}

func (f *fake) PRView(_ context.Context, _ int, fields ...string) (model.PullRequest, error) {
	f.fields = fields
	if f.viewErr != nil {
		return model.PullRequest{}, f.viewErr
	}

	pull, err := model.Decode[model.PullRequest]("gh pr view", f.view)

	return pull, err
}

func (f *fake) BranchRef(_ context.Context, branch string) (string, bool, error) {
	f.branches = append(f.branches, branch)

	return head, !f.branchGone, f.branchErr
}

type pull struct {
	state  string
	draft  bool
	labels []string
	base   string
	gone   bool
}

func open() pull { return pull{state: "OPEN", base: "main"} }

func (p pull) fake(t *testing.T) *fake {
	t.Helper()

	labels := []map[string]string{}
	for _, name := range p.labels {
		labels = append(labels, map[string]string{"name": name})
	}

	data, err := json.Marshal(map[string]any{
		"number": 1, "state": p.state, "isDraft": p.draft, "mergedAt": nil,
		"headRefOid": head, "headRefName": "feature", "baseRefName": p.base, "labels": labels,
	})
	require.NoError(t, err)

	return &fake{view: string(data), branchGone: p.gone}
}

func summary(state, draft, base, labels string) string {
	return "pr 1  state " + state + "  draft " + draft + "  head " + head + "  ref feature  base " + base + "  labels [" + labels + "]"
}

func check(t *testing.T, f *fake, req preflight.Request) preflight.Result {
	t.Helper()

	result, err := preflight.Check(context.Background(), f, req, "hold")
	require.NoError(t, err)

	return result
}

func TestCheck_HoldsOnTheLabelItIsGiven(t *testing.T) {
	t.Parallel()

	p := open()
	p.labels = []string{"blocked", "P1"}

	held, err := preflight.Check(context.Background(), p.fake(t), preflight.Request{PR: 1}, "blocked")
	require.NoError(t, err)
	assert.Equal(t, preflight.Held, held.Stop)
	assert.Equal(t, "STOP: #1 now carries the 'blocked' label. A product decision is open; nothing merges.", held.Lines[0])

	other, err := preflight.Check(context.Background(), p.fake(t), preflight.Request{PR: 1}, "hold")
	require.NoError(t, err)
	assert.True(t, other.OK(), "the label hold is not the label blocked")
}

func TestCheck_AnEmptyHoldLabelHoldsNothing(t *testing.T) {
	t.Parallel()

	for _, labels := range [][]string{nil, {"hold"}} {
		p := open()
		p.labels = labels

		result, err := preflight.Check(context.Background(), p.fake(t), preflight.Request{PR: 1}, "")
		require.NoError(t, err)
		assert.True(t, result.OK(), labels)
	}
}

func TestCheck_ReadsThePullRequestOnceWithTheFieldsTheChecksNeed(t *testing.T) {
	t.Parallel()

	f := open().fake(t)
	check(t, f, preflight.Request{PR: 1})

	assert.Equal(t, []string{"number", "state", "isDraft", "mergedAt", "headRefOid", "headRefName", "baseRefName", "labels", "headRepository"}, f.fields)
	assert.Equal(t, []string{"feature"}, f.branches)
}

func TestCheck_AnUnchangedPullRequestReportsOK(t *testing.T) {
	t.Parallel()

	result := check(t, open().fake(t), preflight.Request{PR: 1})

	assert.Equal(t, preflight.Unchanged, result.Stop)
	assert.Equal(t, summary("OPEN", "false", "main", ""), result.Summary)
	assert.Equal(t, []string{"OK: #1 is unchanged in every respect this checks."}, result.Lines)
	assert.True(t, result.OK())
}

func TestCheck_TheSummaryListsEveryLabel(t *testing.T) {
	t.Parallel()

	p := open()
	p.labels = []string{"bug", "P1"}

	assert.Equal(t, summary("OPEN", "false", "main", "bug,P1"), check(t, p.fake(t), preflight.Request{PR: 1}).Summary)
}

func TestCheck_EachStopHasItsCodeAndRecovery(t *testing.T) {
	t.Parallel()

	merged, closed, held, draft, gone, retarget := open(), open(), open(), open(), open(), open()
	merged.state, merged.gone = "MERGED", true
	closed.state = "CLOSED"
	held.labels = []string{"hold"}
	draft.draft = true
	gone.gone = true
	retarget.base = "other-base"

	tests := []struct {
		name  string
		p     pull
		req   preflight.Request
		want  preflight.Stop
		code  int
		lines []string
	}{
		{"merged", merged, preflight.Request{PR: 1}, preflight.Merged, 3, []string{
			"STOP: #1 is already MERGED. Anything you push to 'feature' now lands on a branch nobody ships.",
			"  Recovery: branch from the CURRENT tip of 'main', re-apply the work, open a NEW pull request,",
			"  and say in its body that it continues #1. Do not reopen a merged pull request.",
		}},
		{"closed_unmerged", closed, preflight.Request{PR: 1}, preflight.Closed, 4, []string{
			"STOP: #1 is CLOSED and not merged. Somebody decided against it while you were working.",
			"  Recovery: read the closing comment before anything else. A close is a decision, and",
			"  reopening it is the maintainer's call, not the caller's.",
		}},
		{"branch_gone", gone, preflight.Request{PR: 1}, preflight.BranchGone, 7, []string{
			"STOP: the head branch 'feature' no longer exists on the remote.",
			"  Recovery: do NOT push, which would recreate it silently. Branch from the current tip of",
			"  'main', re-apply the work, and open a new pull request.",
		}},
		{"held", held, preflight.Request{PR: 1}, preflight.Held, 9, []string{
			"STOP: #1 now carries the 'hold' label. A product decision is open; nothing merges.",
			"  Recovery: write the judgement on the issue and move to the next item.",
		}},
		{"draft", draft, preflight.Request{PR: 1}, preflight.Draft, 8, []string{
			"STOP: #1 is a DRAFT. It is not mergeable and may be mid-rework by its author.",
			"  Recovery: review it if you were sent to review it, but do not merge and do not push",
			"  without saying so on the pull request first.",
		}},
		{"head_moved", open(), preflight.Request{PR: 1, ExpectHead: other}, preflight.HeadMoved, 5, []string{
			"STOP: the head moved. You expected " + other + "; the head is now " + head + ".",
			"  Recovery: a review of the old SHA does not describe this one. Re-read the diff between",
			"  them and review the delta, and re-run any gate whose result named the old SHA.",
		}},
		{"base_changed", retarget, preflight.Request{PR: 1, ExpectBase: "main"}, preflight.BaseChanged, 6, []string{
			"STOP: the base changed. You gated against 'main'; it now targets 'other-base'.",
			"  Recovery: a union gate is a statement about one base. Re-cut it against 'other-base'.",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := check(t, tt.p.fake(t), tt.req)
			assert.Equal(t, tt.want, result.Stop)
			assert.Equal(t, tt.code, result.Stop.Code())
			assert.Equal(t, tt.lines, result.Lines)
			assert.False(t, result.OK())
			assert.NotEmpty(t, result.Summary)
		})
	}
}

func TestCheck_TheStopOrderReportsTheConditionThatMakesTheRestMoot(t *testing.T) {
	t.Parallel()

	all := pull{state: "MERGED", draft: true, labels: []string{"hold"}, base: "other", gone: true}
	heldDraft := pull{state: "OPEN", draft: true, labels: []string{"hold"}, base: "main"}
	goneHeld := pull{state: "OPEN", draft: true, labels: []string{"hold"}, base: "main", gone: true}
	closedGone := pull{state: "CLOSED", base: "main", gone: true}
	moved := pull{state: "OPEN", base: "other"}
	draftMoved := pull{state: "OPEN", draft: true, base: "main"}

	tests := []struct {
		name string
		p    pull
		req  preflight.Request
		want preflight.Stop
	}{
		{"merged_over_everything", all, preflight.Request{PR: 1, ExpectHead: other, ExpectBase: "main"}, preflight.Merged},
		{"closed_over_branch", closedGone, preflight.Request{PR: 1}, preflight.Closed},
		{"branch_over_hold_and_draft", goneHeld, preflight.Request{PR: 1}, preflight.BranchGone},
		{"hold_over_draft", heldDraft, preflight.Request{PR: 1}, preflight.Held},
		{"draft_over_head", draftMoved, preflight.Request{PR: 1, ExpectHead: other}, preflight.Draft},
		{"head_over_base", moved, preflight.Request{PR: 1, ExpectHead: other, ExpectBase: "main"}, preflight.HeadMoved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, check(t, tt.p.fake(t), tt.req).Stop)
		})
	}
}

func TestCheck_AWordInsideALabelDoesNotHold(t *testing.T) {
	t.Parallel()

	for _, label := range []string{"on-hold-ish", "holdout", "Hold", "wait-hold"} {
		p := open()
		p.labels = []string{label}
		assert.True(t, check(t, p.fake(t), preflight.Request{PR: 1}).OK(), label)
	}
}

func TestCheck_AMatchingHeadAndBasePass(t *testing.T) {
	t.Parallel()

	assert.True(t, check(t, open().fake(t), preflight.Request{PR: 1, ExpectHead: head, ExpectBase: "main"}).OK())
}

func TestCheck_AShortOrLongSHAIsRefusedRatherThanCompared(t *testing.T) {
	t.Parallel()

	for _, sha := range []string{"abc1234", strings.Repeat("1", 39), strings.Repeat("1", 41)} {
		result := check(t, open().fake(t), preflight.Request{PR: 1, ExpectHead: sha})

		assert.Equal(t, preflight.Refused, result.Stop, sha)
		assert.Equal(t, 2, result.Stop.Code())
		assert.Equal(t, []string{"REFUSE: the expected head needs the full 40-character SHA, got '" + sha + "'."}, result.Lines)
		assert.Equal(t, summary("OPEN", "false", "main", ""), result.Summary)
	}
}

func TestCheck_AnAnswerThatCannotBeUsedIsARefusal(t *testing.T) {
	t.Parallel()

	const tail = `"headRefName":"feature","labels":[]}`

	tests := map[string]string{
		"empty":         "",
		"null":          "null",
		"an_array":      "[]",
		"not_json":      "not json",
		"no_labels":     `{"state":"OPEN","isDraft":false}`,
		"labels_object": `{"state":"OPEN","isDraft":false,"labels":{}}`,
		"labels_null":   `{"state":"OPEN","isDraft":false,"labels":null}`,
		"no_state":      `{"isDraft":false,` + tail,
		"null_state":    `{"state":null,"isDraft":false,` + tail,
		"int_state":     `{"state":3,"isDraft":false,` + tail,
		"no_draft":      `{"state":"OPEN",` + tail,
		"null_draft":    `{"state":"OPEN","isDraft":null,` + tail,
		"string_draft":  `{"state":"OPEN","isDraft":"false",` + tail,
		"no_head_ref":   `{"state":"OPEN","isDraft":false,"labels":[]}`,
	}
	for name, view := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := preflight.Check(context.Background(), &fake{view: view}, preflight.Request{PR: 1}, "hold")

			var unread *preflight.ReadError
			require.ErrorAs(t, err, &unread)
			assert.Equal(t, "cannot read #1", err.Error())
		})
	}
}

func TestCheck_AFailedReadIsARefusalThatKeepsItsCause(t *testing.T) {
	t.Parallel()

	_, err := preflight.Check(context.Background(), &fake{viewErr: errDown}, preflight.Request{PR: 1}, "hold")
	require.ErrorIs(t, err, errDown)
	assert.Equal(t, "cannot read #1", err.Error())

	_, err = preflight.Check(context.Background(), &fake{view: `{"state":"OPEN","isDraft":false,"headRefName":"f","labels":[]}`, branchErr: errDown}, preflight.Request{PR: 1}, "hold")
	require.ErrorIs(t, err, errDown)
	assert.Equal(t, "cannot read the branch f of #1", err.Error())
}

func TestCheck_AMissingOtherFieldReadsAsNullInTheSummary(t *testing.T) {
	t.Parallel()

	f := &fake{view: `{"state":"OPEN","isDraft":false,"headRefName":"feature","labels":[{}]}`}

	result := check(t, f, preflight.Request{PR: 1})

	assert.Equal(t, "pr 1  state OPEN  draft false  head null  ref feature  base null  labels [null]", result.Summary)
	assert.True(t, result.OK())
}

func TestStop_IsAClosedSetWithTheCodesACallerBranchesOn(t *testing.T) {
	t.Parallel()

	codes := map[preflight.Stop]int{
		preflight.Unchanged: 0, preflight.Refused: 2, preflight.Merged: 3, preflight.Closed: 4, preflight.HeadMoved: 5,
		preflight.BaseChanged: 6, preflight.BranchGone: 7, preflight.Draft: 8, preflight.Held: 9,
	}
	for stop, code := range codes {
		assert.Equal(t, code, stop.Code(), stop.String())
	}

	assert.Len(t, preflight.Stops(), len(codes))
	assert.Equal(t, "HEAD MOVED", preflight.HeadMoved.String())
	assert.Equal(t, "UNKNOWN", preflight.Stop(200).String())
	assert.Equal(t, 2, preflight.Stop(200).Code())
}
