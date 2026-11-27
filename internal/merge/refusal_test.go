package merge_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/merge"
	"github.com/tobocop2/beehest/internal/model"
)

// attributionLine is a line that the patterns of the example forbid.
const attributionLine = "Co-Authored-By: Claude <noreply@anthropic.com>"

func TestRun_AForbiddenLineIsNamedByItsNumberAndPatternAndNotQuoted(t *testing.T) {
	t.Parallel()

	req := request()
	result := newWorld(t).set(t, map[string]any{"body": "a\nb\nGenerated with [Claude Code]", "commits": []any{commitJSON("dev@x", dev, "fix", "")}}).run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, []string{"REFUSE: forbidden text on #5. Offending lines:", "  line 3 matches pattern 4 of forbid_text"}, result.Refusal)

	_, found := merge.ForbiddenBy(req.ForbidText, all(result))
	assert.False(t, found, "no line of the result repeats the forbidden text")
}

func TestRun_NoLineOfTheResultMatchesTheForbidTextOfTheRequest(t *testing.T) {
	t.Parallel()

	path, line := "a.go", 3
	thread := func(body string) []model.ReviewThread {
		return []model.ReviewThread{{Path: &path, Line: &line, First: &model.ThreadComment{Author: model.Account{Login: "rev"}, Body: new(body)}}}
	}
	cases := map[string]struct {
		over    map[string]any
		threads []model.ReviewThread
		// forbid is the forbid_text of the request. Nil means the patterns of the example.
		forbid []string
		says   string
	}{
		"a line of the body":       {over: map[string]any{"body": "first\n" + attributionLine}, says: "  line 2 matches pattern 1 of forbid_text"},
		"a line of a commit body":  {over: map[string]any{"commits": []any{commitJSON("dev@x", dev, "fix", attributionLine)}}, says: "  line 2 matches pattern 1 of forbid_text"},
		"a commit headline":        {over: map[string]any{"commits": []any{commitJSON("dev@x", dev, attributionLine, "")}}, says: "  line 3 matches pattern 1 of forbid_text"},
		"the first thread comment": {threads: thread(attributionLine), says: "a.go:3 rev: (the comment is left out: it matches pattern 1 of forbid_text)"},
		"the second line of a thread comment under an anchored pattern": {
			threads: thread("looks fine\nCo-Authored-By: Claude"), forbid: []string{"zzz", "^Co-Authored-By"},
			says: "a.go:3 rev: (the comment is left out: it matches pattern 2 of forbid_text)",
		},
	}
	for name, one := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			if one.over != nil {
				w.set(t, one.over)
			}

			w.threads = one.threads
			req := request()
			if one.forbid != nil {
				req.ForbidText = compiled(one.forbid)
			}

			result := w.run(req)

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Contains(t, result.Refusal, one.says)
			assertPostable(t, req, result)
		})
	}
}

// assertPostable asserts that the posting guard passes the result: no element of Lines, and not the Refusal joined by newlines as the batch posts it.
func assertPostable(t *testing.T, req merge.Request, result merge.Result) {
	t.Helper()

	for _, said := range result.Lines {
		_, found := merge.ForbiddenBy(req.ForbidText, said)
		assert.False(t, found, "the result line %q matches a pattern of the request", said)
	}

	_, found := merge.ForbiddenBy(req.ForbidText, strings.Join(result.Refusal, "\n"))
	assert.False(t, found, "the refusal %q matches a pattern of the request", result.Refusal)
}

func TestRun_AForgeErrorOfSeveralLinesIsLeftOutWhenAPatternMatchesOneOfThem(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["UnresolvedThreads"] = true
	w.down = errors.New("HTTP 502\nCo-Authored-By: Claude")
	req := request()
	req.ForbidText = compiled([]string{"zzz", "^Co-Authored-By"})
	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	require.NotEmpty(t, result.Refusal)
	assert.Equal(t, "(a line is left out: it matches pattern 2 of forbid_text)", result.Refusal[0])
	assertPostable(t, req, result)
}

func TestPollAndDisarm_AForgeErrorOfSeveralLinesIsLeftOutWhenAPatternMatchesOneOfThem(t *testing.T) {
	t.Parallel()

	cases := map[string]func(q *queuedWorld, req merge.Request) merge.Result{
		"poll": func(q *queuedWorld, req merge.Request) merge.Result { return q.poll(req, false) },
		"disarm": func(q *queuedWorld, req merge.Request) merge.Result {
			return merge.Disarm(context.Background(), q, req)
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			q := queued(t, "CLOSED")
			q.fails["DisableAutoMergePR"] = true
			q.down = errors.New("HTTP 502\nCo-Authored-By: Claude")
			req := queuing()
			req.ForbidText = compiled([]string{"zzz", "^Co-Authored-By"})
			result := call(q, req)

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Contains(t, result.Refusal, "(a line is left out: it matches pattern 2 of forbid_text)")
			assertPostable(t, req, result)
		})
	}
}

func TestRun_ALineThatAGuardThatPassedSaidIsLeftOutOfTheResult(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.checks = rows(model.CheckPass, model.CheckBucket("fail"))
	req := request()
	req.ForbidText = compiled([]string{"zzz", "^before: "})
	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines, "(a line is left out: it matches pattern 2 of forbid_text)")
	assert.Equal(t, []string{"REFUSE: checks are not green: c1=fail", "       (above refers to #5)"}, result.Refusal)
	assertPostable(t, req, result)
}

func TestRun_ALineOfForgeTextThatAPatternMatchesIsLeftOutOfTheResult(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["UnresolvedThreads"] = true
	req := request()
	req.ForbidText = compiled([]string{"down", "zzz"})
	result := w.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	require.NotEmpty(t, result.Refusal)
	assert.Equal(t, "(a line is left out: it matches pattern 1 of forbid_text)", result.Refusal[0])
	assert.Equal(t, result.Refusal, result.Lines[len(result.Lines)-len(result.Refusal):])
}

func TestRun_ARefusalKeepsItsLinesApartFromTheLinesBefore(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.checks = rows(model.CheckPass, model.CheckBucket("fail"))
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, []string{"REFUSE: checks are not green: c1=fail", "       (above refers to #5)"}, result.Refusal)
	require.Greater(t, len(result.Lines), len(result.Refusal), "the guards that passed said lines before the refusal")
	assert.Equal(t, result.Refusal, result.Lines[len(result.Lines)-len(result.Refusal):])
	assert.NotContains(t, strings.Join(result.Refusal, "\n"), "OK")
}

func TestRun_AMergeThatLandsHasNoRefusal(t *testing.T) {
	t.Parallel()

	result := newWorld(t).run(request())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Empty(t, result.Refusal)
}
