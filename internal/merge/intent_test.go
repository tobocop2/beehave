package merge_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

var intentTime = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func intentFor() *merge.Intent {
	return &merge.Intent{PR: pr, Head: sha, Base: "main", BaseTipTree: "tree0", State: "OPEN", Time: intentTime}
}

// mergedWorld is a forge where the pull request merged at the given time, after the intent.
func mergedWorld(t *testing.T, mergedAt time.Time, over map[string]any) *world {
	t.Helper()

	row := map[string]any{"state": "MERGED", "mergeable": "UNKNOWN", "mergedAt": mergedAt.Format(time.RFC3339)}
	for key, value := range over {
		row[key] = value
	}

	return newWorld(t).set(t, row)
}

func replay() merge.Request {
	req := request()
	req.Intent = intentFor()

	return req
}

func TestRun_AnIntentCountsAMergedPullRequestAtTheGatedHeadAsDone(t *testing.T) {
	t.Parallel()

	w := mergedWorld(t, intentTime.Add(time.Minute), nil)
	result := w.run(replay())

	assert.Equal(t, merge.Done, result.Stop, result.Lines)
	assert.Empty(t, w.writes)
	assert.Contains(t, result.Lines, "checks: 1 of 1 passed")
	assert.Equal(t, "MERGED #5 at "+sha+"; main tip is now "+tipSHA, last(result))
	assert.True(t, w.called("PRView 5 state,isDraft,baseRefName,headRefName,headRefOid,mergeable,files,commits,body,author,labels,mergedAt"))
}

func TestRun_AnIntentStillRunsTheTreeProof(t *testing.T) {
	t.Parallel()

	req := replay()
	req.GatedTree = "tree-gated"
	w := mergedWorld(t, intentTime.Add(time.Minute), nil)
	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "STOP:")
}

func TestRun_WithoutAnIntentAMergedPullRequestStillRefuses(t *testing.T) {
	t.Parallel()

	w := mergedWorld(t, intentTime.Add(time.Minute), nil)
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: state is MERGED", last(result))
}

func TestRun_AnIntentMergedBeforeTheIntentIsRefused(t *testing.T) {
	t.Parallel()

	result := mergedWorld(t, intentTime.Add(-merge.DefaultSkew-time.Second), nil).run(replay())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "merged before the intent was written")
}

func TestRun_TheSkewMarginIsInclusive(t *testing.T) {
	t.Parallel()

	assert.Equal(t, merge.Done, mergedWorld(t, intentTime.Add(-merge.DefaultSkew), nil).run(replay()).Stop)

	req := replay()
	req.Intent.Skew = time.Hour
	assert.Equal(t, merge.Done, mergedWorld(t, intentTime.Add(-time.Hour), nil).run(req).Stop)
	assert.Equal(t, merge.Refused, mergedWorld(t, intentTime.Add(-time.Hour-time.Second), nil).run(req).Stop)
}

func TestRun_AnIntentMergedAtAnotherHeadIsRefused(t *testing.T) {
	t.Parallel()

	result := mergedWorld(t, intentTime.Add(time.Minute), map[string]any{"headRefOid": moved}).run(replay())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: head "+moved+" is not the gated "+sha+". Re-gate.", last(result))
}

func TestRun_AnIntentMergedIntoAnotherBaseIsRefused(t *testing.T) {
	t.Parallel()

	result := mergedWorld(t, intentTime.Add(time.Minute), map[string]any{"baseRefName": dev}).run(replay())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: base is dev, expected main", last(result))
}

func TestRun_AnIntentThatDidNotReadOpenIsRefused(t *testing.T) {
	t.Parallel()

	req := replay()
	req.Intent.State = "MERGED"
	result := mergedWorld(t, intentTime.Add(time.Minute), nil).run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "did not read the pull request as OPEN")
}

func TestRun_AnIntentForAnotherPullRequestHeadOrBaseIsRefused(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*merge.Intent){
		"pr":   func(i *merge.Intent) { i.PR = 6 },
		"head": func(i *merge.Intent) { i.Head = moved },
		"base": func(i *merge.Intent) { i.Base = dev },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := replay()
			mutate(req.Intent)
			result := mergedWorld(t, intentTime.Add(time.Minute), nil).run(req)

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Contains(t, last(result), "intent is for")
		})
	}
}

func TestRun_AMergedPullRequestWithNoReadableMergeTimeIsRefused(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]any{"null": nil, "garbage": "yesterday"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := mergedWorld(t, intentTime, map[string]any{"mergedAt": value}).run(replay())

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Contains(t, last(result), "merge time")
		})
	}
}

func TestRun_AnIntentDoesNotSkipTheOwnWorkGuard(t *testing.T) {
	t.Parallel()

	w := mergedWorld(t, intentTime.Add(time.Minute), map[string]any{"author": map[string]any{"login": me}})
	result := w.run(replay())

	assert.Equal(t, merge.OwnWork, result.Stop)
	assert.Empty(t, w.writes)
}

func TestRun_AnIntentDoesNotSkipTheChecksTheHoldOrTheForbiddenText(t *testing.T) {
	t.Parallel()

	failing := mergedWorld(t, intentTime.Add(time.Minute), nil)
	failing.checks = []model.Check{{Name: "ci", Bucket: model.CheckFail}}
	assert.Contains(t, failing.run(replay()).Lines, "REFUSE: checks are not green: ci=fail")

	req := replay()
	req.Hold = "hold"
	held := mergedWorld(t, intentTime.Add(time.Minute), map[string]any{"labels": []any{map[string]any{"name": "hold"}}})
	assert.Contains(t, last(held.run(req)), "so it is held")

	req = replay()
	req.ForbidText = compiled([]string{"secret"})
	forbidden := mergedWorld(t, intentTime.Add(time.Minute), map[string]any{"body": "a secret"})
	assert.Contains(t, last(forbidden.run(req)), "a secret")
}

func TestRun_AnIntentSkipsTheGuardsOfAnOpenPullRequest(t *testing.T) {
	t.Parallel()

	w := mergedWorld(t, intentTime.Add(time.Minute), map[string]any{"isDraft": true})
	w.children = []model.PullRequest{pullJSON(t, map[string]any{"number": 9, "baseRefName": "feat", "headRefName": "kid"})}
	w.stacks[pr] = "stack-1"

	result := w.run(replay())

	assert.Equal(t, merge.Done, result.Stop, result.Lines)
	assert.Empty(t, w.writes)
}

func TestNewIntent_RecordsThePullRequestRead(t *testing.T) {
	t.Parallel()

	view := pullJSON(t, nil)
	got, err := merge.NewIntent(view, "tree0", intentTime)

	require.NoError(t, err)
	assert.Equal(t, *intentFor(), got)
}

// replayRefuses runs a replay of a merged pull request that a guard must still refuse, and returns the result.
func replayRefuses(t *testing.T, over map[string]any, arm func(*world)) merge.Result {
	t.Helper()

	w := mergedWorld(t, intentTime.Add(time.Minute), over)
	arm(w)

	return w.run(replay())
}

func TestRun_AnIntentDoesNotSkipTheReviewsTheFileListOrTheAuthors(t *testing.T) {
	t.Parallel()

	line, path, text := 3, "a.go", "fix this"
	thread := model.ReviewThread{Path: &path, Line: &line, First: &model.ThreadComment{Author: model.Account{Login: "rev"}, Body: &text}}

	for name, one := range map[string]struct {
		over map[string]any
		arm  func(*world)
		want string
	}{
		"a standing change request": {nil, func(w *world) { w.requests = []model.ChangeRequest{{Login: "rev", Commit: sha}} }, "rev@"},
		"an unresolved thread":      {nil, func(w *world) { w.threads = []model.ReviewThread{thread} }, "unresolved review threads"},
		"a workflow file with no scope": {
			map[string]any{"files": []any{map[string]any{"path": ".github/workflows/ci.yml"}}},
			func(w *world) { w.scopes = []string{"repo"} }, "no workflow scope",
		},
		"a short file list": {nil, func(w *world) { w.counts = &counts{commits: 1, files: 101} }, "incomplete"},
		"an unlinked author": {
			map[string]any{"commits": []any{commitJSON("other@x", "", "fix", "")}},
			func(*world) {}, "no linked account",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := replayRefuses(t, one.over, one.arm)

			assert.NotEqual(t, merge.Done, result.Stop, result.Lines)
			assert.Contains(t, strings.Join(result.Lines, "\n"), one.want)
		})
	}
}

func TestRun_AReplayLeavesOneIntegrationRow(t *testing.T) {
	t.Parallel()

	req := replay()
	req.IntegrationPR, req.Dir = 9, t.TempDir()
	row := "- #5 merged at " + sha

	w := mergedWorld(t, intentTime.Add(time.Minute), nil)
	w.body = "rows\n" + row
	assert.Equal(t, merge.Done, w.run(req).Stop)
	assert.Equal(t, 1, strings.Count(w.body, row))

	crlf := mergedWorld(t, intentTime.Add(time.Minute), nil)
	crlf.body = "rows\r\n" + row + "\r\n"
	assert.Equal(t, merge.Done, crlf.run(req).Stop)
	assert.Equal(t, 1, strings.Count(crlf.body, row), "a body written with CRLF keeps one row")

	fresh := mergedWorld(t, intentTime.Add(time.Minute), nil)
	assert.Equal(t, merge.Done, fresh.run(req).Stop)
	assert.Equal(t, 1, strings.Count(fresh.body, row))
}

func TestNewIntent_RefusesAReadWithoutAFact(t *testing.T) {
	t.Parallel()

	for name, over := range map[string]map[string]any{
		"state": {"state": nil}, "head": {"headRefOid": nil}, "base": {"baseRefName": nil},
	} {
		_, err := merge.NewIntent(pullJSON(t, over), "tree0", intentTime)

		require.Error(t, err, name)
	}
}
