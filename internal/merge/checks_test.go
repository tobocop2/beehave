package merge_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

func rows(buckets ...model.CheckBucket) []model.Check {
	found := make([]model.Check, len(buckets))
	for i, bucket := range buckets {
		found[i] = model.Check{Name: fmt.Sprintf("c%d", i), Bucket: bucket}
	}

	return found
}

func TestChecks_Rules(t *testing.T) {
	t.Parallel()

	const (
		pass, fail, pend, skip = model.CheckPass, model.CheckBucket("fail"), model.CheckPending, model.CheckSkipping
	)

	cases := map[string]struct {
		checks []model.Check
		allow  bool
		stop   merge.Stop
		line   string
	}{
		"a failed check":        {rows(pass, fail), false, merge.Refused, "REFUSE: checks are not green: c1=fail"},
		"a canceled check":      {rows(pass, "cancel"), false, merge.Refused, "REFUSE: checks are not green: c1=cancel"},
		"a pending check":       {rows(pass, pend), false, merge.Refused, "REFUSE: checks still running: c1. Wait for them."},
		"only a skipped check":  {rows(skip), false, merge.Refused, "REFUSE: no check passed (1 reported, 0 passed). CI did not run on this head."},
		"no checks":             {nil, false, merge.Refused, "REFUSE: no check passed (0 reported, 0 passed). CI did not run on this head."},
		"skipped and allowed":   {rows(skip), true, merge.Done, "checks: 0 of 1 passed"},
		"no checks and allowed": {nil, true, merge.Done, "checks: 0 of 0 passed"},
		"a skip beside a pass":  {rows(pass, skip), false, merge.Done, "checks: skipped and not counted as a pass: c1"},
		"a check with no name":  {[]model.Check{{Bucket: fail}}, false, merge.Refused, "REFUSE: checks are not green: =fail"},
	}
	for name, one := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			w.checks = one.checks
			req := request()
			req.AllowNoChecks = one.allow
			result := w.run(req)

			assert.Equal(t, one.stop, result.Stop)
			assert.Contains(t, result.Lines, one.line)

			if one.stop == merge.Refused {
				assert.Equal(t, "       (above refers to #5)", last(result))
				assert.Empty(t, w.writes)
			}
		})
	}
}

func TestChecks_UnreadableChecksRefuse(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["PRChecks"] = true
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: cannot read the checks for #5", last(result))
}

func TestScope_AWorkflowChangeNeedsTheWorkflowScope(t *testing.T) {
	t.Parallel()

	workflow := map[string]any{"files": []any{map[string]any{"path": ".github/workflows/ci.yml"}}}

	cases := map[string]struct {
		scopes []string
		want   merge.Stop
	}{
		"both":      {[]string{"repo", "workflow"}, merge.Done},
		"repo only": {[]string{"repo"}, merge.NoWorkflowScope},
		"none":      {nil, merge.NoWorkflowScope},
	}
	for name, one := range cases {
		w := newWorld(t).set(t, workflow)
		w.scopes = one.scopes
		result := w.run(request())

		assert.Equal(t, one.want, result.Stop, name)

		if one.want == merge.NoWorkflowScope {
			assert.Equal(t, 3, result.Stop.Code())
			assert.Equal(t, "REFUSE: #5 touches .github/workflows and the token has no workflow scope.", result.Lines[len(result.Lines)-2])
			assert.Contains(t, last(result), "gh auth refresh")
			assert.Empty(t, w.writes)
		}
	}
}

func TestScope_TheWorkflowPrefixNeedsItsTrailingSlash(t *testing.T) {
	t.Parallel()

	other := map[string]any{"files": []any{map[string]any{"path": ".github/workflows"}, map[string]any{"path": "x/.github/workflows/a"}}}
	w := newWorld(t).set(t, other)

	assert.Equal(t, merge.Done, w.run(request()).Stop)
	assert.False(t, w.called("TokenScopes"))
}

func TestScope_UnreadableScopesRefuse(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"files": []any{map[string]any{"path": ".github/workflows/ci.yml"}}})
	w.fails["TokenScopes"] = true
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: cannot read the token scopes, so a workflow change cannot be merged", last(result))
}

func TestScope_AnUnreadableFileListRefusesInsteadOfSkippingTheScopeCheck(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"files": nil})
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "the changed files cannot be read")
	assert.NotContains(t, last(result), "incomplete")
	assert.False(t, w.called("TokenScopes"))
	assert.Empty(t, w.writes)
}

func TestScope_AFileListThatTheForgeCutRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.counts = &counts{commits: 1, files: 101}
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "the list of changed files is incomplete")
	assert.Empty(t, w.writes)
}

func TestReviews_AStandingChangeRequestRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.requests = []model.ChangeRequest{{Login: "rev", Commit: "eeeeeeeeeeeeeeee"}, {Login: "rev2", Commit: "ffff"}}
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: changes requested and still standing (an approval or a dismissal lifts one, a comment does not): rev@eeeeeeeee,rev2@ffff. Route the review to a fix lane.", last(result))
	assert.Empty(t, w.writes)
}

func TestReviews_NoStandingRequestLetsTheMergeGo(t *testing.T) {
	t.Parallel()

	assert.Equal(t, merge.Done, newWorld(t).run(request()).Stop)
}

func TestReviews_UnreadableReviewsRefuse(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["OpenChangeRequests"] = true

	assert.Equal(t, "REFUSE: the reviews of #5 could not be read, so a standing change request cannot be ruled out.", last(w.run(request())))
}

func TestReviews_AnUnresolvedThreadRefusesWithItsLine(t *testing.T) {
	t.Parallel()

	path, line, text := "a.go", 3, "fix it"
	w := newWorld(t)
	w.threads = []model.ReviewThread{{Path: &path, Line: &line, First: &model.ThreadComment{Author: model.Account{Login: "rev"}, Body: &text}}, {}}
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, []string{
		"REFUSE: unresolved review threads on #5 (address each, then resolve the thread):",
		"a.go:3 rev: fix it",
		"null:null null: null",
	}, result.Lines[len(result.Lines)-3:])
	assert.Empty(t, w.writes)
}

func TestReviews_UnreadableThreadsRefuse(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["UnresolvedThreads"] = true

	assert.Equal(t, "REFUSE: the review threads of #5 could not all be read, so an unresolved one cannot be ruled out.", last(w.run(request())))
}

func TestAttribution_RefusesWhereverItHides(t *testing.T) {
	t.Parallel()

	texts := []string{
		"Co-Authored-By: Claude <noreply@anthropic.com>",
		"x laude-Session y",
		"Authored-By: Claude",
		"Generated with [Claude Code]",
		"NOREPLY@ANTHROPIC.com",
	}
	for _, text := range texts {
		for _, where := range []string{"body", "messageBody", "messageHeadline"} {
			t.Run(where+" "+text, func(t *testing.T) {
				t.Parallel()

				over := map[string]any{}
				switch where {
				case "body":
					over["body"] = "first\n" + text
				case "messageBody":
					over["commits"] = []any{commitJSON("dev@x", dev, "fix", text)}
				default:
					over["commits"] = []any{commitJSON("dev@x", dev, text, "")}
				}

				w := newWorld(t).set(t, over)
				result := w.run(request())

				assert.Equal(t, merge.Refused, result.Stop)
				assert.Equal(t, "REFUSE: AI attribution on #5. Offending lines:", result.Lines[len(result.Lines)-2])
				assert.Regexp(t, ":"+regexp.QuoteMeta(text)+"$", last(result))
				assert.Empty(t, w.writes)
			})
		}
	}
}

func TestAttribution_LineNumbersCountFromTheBody(t *testing.T) {
	t.Parallel()

	result := newWorld(t).set(t, map[string]any{"body": "a\nb\nGenerated with [Claude Code]", "commits": []any{commitJSON("dev@x", dev, "fix", "")}}).run(request())

	assert.Equal(t, "  3:Generated with [Claude Code]", last(result))
}

func TestAttribution_AnEmptyBodyAndEmptyMessagesPass(t *testing.T) {
	t.Parallel()

	assert.Equal(t, merge.Done, newWorld(t).set(t, map[string]any{"body": nil, "commits": []any{commitJSON("dev@x", dev, "", "")}}).run(request()).Stop)
}

func TestAuthors_ACommitWithNoLinkedAccountRefuses(t *testing.T) {
	t.Parallel()

	for name, over := range map[string][]any{
		"an unlinked address": {commitJSON("other@x", "", "fix", "")},
		"no author at all":    {map[string]any{"oid": "bbbbbbbbbbbbbbbb", "authors": []any{}}},
	} {
		w := newWorld(t).set(t, map[string]any{"commits": over})
		result := w.run(request())

		assert.Equal(t, merge.Refused, result.Stop, name)
		assert.Contains(t, result.Lines[len(result.Lines)-4], "REFUSE: these commits have no linked account and carry an address other than me@x:", name)
		assert.Regexp(t, `^  bbbbbbbbb (other@x|\?) \?$`, result.Lines[len(result.Lines)-3], name)
		assert.Empty(t, w.writes, name)
	}
}

func TestAuthors_NoCommitsIsNotClean(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"commits": []any{}})

	assert.Equal(t, "REFUSE: no commit authors to check; refusing to call that clean", last(w.run(request())))
}
