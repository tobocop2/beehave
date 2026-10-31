package merge_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

func all(result merge.Result) string {
	return strings.Join(result.Lines, "\n")
}

func last(result merge.Result) string {
	return result.Lines[len(result.Lines)-1]
}

func TestRun_ACleanPullRequestMergesPinnedToItsHead(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	result := w.run(request())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, 0, result.Stop.Code())
	assert.Equal(t, []string{"MergePR 5 " + sha}, w.writes)
	assert.Contains(t, result.Lines, "checks: 1 of 1 passed")
	assert.Equal(t, "MERGED #5 at "+sha+"; main tip is now "+tipSHA, last(result))
}

func TestRun_TheGatedShaMustBeExactlyFortyCharacters(t *testing.T) {
	t.Parallel()

	for length, want := range map[int]merge.Stop{39: merge.Refused, 40: merge.Done, 41: merge.Refused} {
		pin := strings.Repeat("a", length)
		w := newWorld(t).set(t, map[string]any{"headRefOid": pin})
		req := request()
		req.Gated = pin
		result := w.run(req)

		assert.Equal(t, want, result.Stop, length)

		if want == merge.Refused {
			assert.Equal(t, []string{"REFUSE: gated SHA must be the full 40 characters, got '" + pin + "'"}, result.Lines)
			assert.Empty(t, w.writes)
		}
	}
}

func TestRun_TheFirstReadRefusesWhatItCannotMerge(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		over map[string]any
		want string
	}{
		"merged":      {map[string]any{"state": "MERGED"}, "REFUSE: state is MERGED"},
		"other base":  {map[string]any{"baseRefName": "dev"}, "REFUSE: base is dev, expected main"},
		"head moved":  {map[string]any{"headRefOid": moved}, "REFUSE: head " + moved + " is not the gated " + sha + ". Re-gate."},
		"conflicting": {map[string]any{"mergeable": "CONFLICTING"}, "REFUSE: mergeable is CONFLICTING"},
		"unknown":     {map[string]any{"mergeable": nil}, "REFUSE: mergeable is "},
		"no draft":    {map[string]any{"isDraft": nil}, "REFUSE: cannot read #5 for its state"},
	}
	for name, one := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t).set(t, one.over)
			result := w.run(request())

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Equal(t, one.want, last(result))
			assert.Empty(t, w.writes)
		})
	}
}

func TestRun_AFailedFirstReadRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["PRView"] = true

	assert.Equal(t, []string{"REFUSE: cannot read #5 for its state"}, w.run(request()).Lines)
	assert.Empty(t, w.writes)
}

func TestRun_TheFirstReadAsksOnceForEveryField(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.run(request())

	assert.Equal(t, "PRView 5 state,isDraft,baseRefName,headRefName,headRefOid,mergeable,files,commits,body,author,labels", w.calls[0])
}

func TestRun_TheBeforeLineShowsTheStateWithTabs(t *testing.T) {
	t.Parallel()

	result := newWorld(t).set(t, map[string]any{"state": "CLOSED"}).run(request())

	assert.Equal(t, "before: CLOSED\tfalse\tmain\t"+sha+"\tMERGEABLE", result.Lines[0])
}

func TestRun_TheBeforeLineEscapesATabAndPrintsANull(t *testing.T) {
	t.Parallel()

	result := newWorld(t).set(t, map[string]any{"state": "A\tB\n\r\\", "mergeable": nil}).run(request())

	assert.Equal(t, "before: A\\tB\\n\\r\\\\\tfalse\tmain\t"+sha+"\t", result.Lines[0])
}

func TestRun_ReadsTheRepositoryBaseAndOwnerFromTheRequest(t *testing.T) {
	t.Parallel()

	req := merge.Request{
		PR: pr, Gated: sha, Runners: []string{"bee-1"},
		Repo: "owner/name", Base: "main", OwnLogins: []string{me}, OwnEmails: []string{meMail},
	}
	w := newWorld(t)
	result := w.run(req)

	assert.Equal(t, merge.Done, result.Stop, strings.Join(result.Lines, "\n"))
	assert.Len(t, merges(w), 1)
}

func TestRun_TheBaseOfTheRequestIsTheBaseTheGuardExpects(t *testing.T) {
	t.Parallel()

	req := request()
	req.Base = "develop"
	result := newWorld(t).run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: base is main, expected develop", last(result))
}

func TestRun_AMissingSettingRefuses(t *testing.T) {
	t.Parallel()

	blanks := map[string]func(*merge.Request){
		"base branch": func(r *merge.Request) { r.Base = "" },
		"repository":  func(r *merge.Request) { r.Repo = "" },
	}
	for name, blank := range blanks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := request()
			blank(&req)
			w := newWorld(t)
			result := w.run(req)

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Equal(t, "REFUSE: the "+name+" of the merge is not set.", result.Lines[0])
			assert.Empty(t, w.writes)
		})
	}
}

func TestRun_TheRefusalsComeInTheOrderOfTheList(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"headRefOid": moved, "mergeable": "CONFLICTING"})
	assert.Contains(t, last(w.run(request())), "is not the gated", "head before mergeable")

	w = newWorld(t).set(t, map[string]any{"files": []any{map[string]any{"path": ".github/workflows/ci.yml"}}})
	w.checks, w.scopes = []model.Check{{Name: "ci", Bucket: "fail"}}, nil
	result := w.run(request())

	assert.Contains(t, all(result), "checks are not green", "checks before scope")
	assert.False(t, w.called("TokenScopes"))

	w = newWorld(t)
	w.requests = []model.ChangeRequest{{Login: "rev", Commit: sha}}
	w.fails["UnresolvedThreads"] = true
	assert.Contains(t, last(w.run(request())), "changes requested", "requests before threads")

	w = newWorld(t).set(t, map[string]any{"body": "Generated with [Claude Code]", "isDraft": true})
	w.threads = []model.ReviewThread{{}}
	assert.Contains(t, all(w.run(request())), "unresolved review threads", "threads before attribution")

	w = newWorld(t).set(t, map[string]any{"commits": []any{commitJSON("x@x", "", "fix", "")}, "isDraft": true})
	w.stacks[5] = "S"
	assert.Contains(t, all(w.run(request())), "no linked account", "authors before the draft rule")
}

func TestRun_AChildOfThePullRequestRefusesTheMergeUnlessOrphansAreAllowed(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.children = []model.PullRequest{pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"})}
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: #5 is the base of #7. A squash merge orphans them.", result.Lines[1])
	assert.Contains(t, strings.Join(result.Lines, "\n"), "--allow-orphans")
	assert.Empty(t, w.writes)

	req := request()
	req.AllowOrphans = true
	result = w.run(req)

	assert.Equal(t, merge.Done, result.Stop)
	assert.Contains(t, result.Lines, "        --allow-orphans is set; merging and orphaning #7 is now a stated decision.")
}

func TestRun_ADraftIsRefusedUnlessReadyingIsAllowed(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"isDraft": true})
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, all(result), "--allow-ready")
	assert.Contains(t, all(result), "#5 is a draft")
	assert.Empty(t, w.writes)
}

func TestRun_ADraftIsReadiedBeforeTheMergeWhenAllowed(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"isDraft": true})
	later := pullJSON(t, nil)
	w.later = &later
	req := request()
	req.AllowReady = true

	assert.Equal(t, merge.Done, w.run(req).Stop)
	assert.Equal(t, []string{"SetDraft 5 false", "MergePR 5 " + sha}, w.writes)
}

func TestRun_AFailedReadyRefuses(t *testing.T) {
	t.Parallel()

	req := request()
	req.AllowReady = true

	w := newWorld(t).set(t, map[string]any{"isDraft": true})
	w.fails["SetDraft"] = true
	assert.Equal(t, "REFUSE: cannot mark #5 ready: SetDraft: down", last(w.run(req)))
	assert.Empty(t, merges(w))

	w = newWorld(t).set(t, map[string]any{"isDraft": true})
	still := pullJSON(t, map[string]any{"isDraft": true})
	w.later = &still
	assert.Equal(t, "REFUSE: #5 is still a draft after it was marked ready", last(w.run(req)))

	w = newWorld(t).set(t, map[string]any{"isDraft": true})
	open := "OPEN"
	w.later = &model.PullRequest{State: &open}
	assert.Equal(t, "REFUSE: #5 is still a draft after it was marked ready", last(w.run(req)))
	assert.Empty(t, merges(w))

	w = newWorld(t).set(t, map[string]any{"isDraft": true})
	w.later = &still
	w.failAt = 3
	assert.Equal(t, "REFUSE: cannot read #5 after it was marked ready", last(w.run(req)))
	assert.Empty(t, merges(w))

	w = newWorld(t).set(t, map[string]any{"isDraft": true})
	w.failAt = 2
	assert.Equal(t, "REFUSE: cannot read the state of #5 before it is marked ready", last(w.run(req)))
	assert.Empty(t, w.writes)
}

func TestRun_AHeadThatMovesBeforeTheMergeRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	later := pullJSON(t, map[string]any{"headRefOid": moved})
	w.later = &later
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: head moved to "+moved+" while preparing. Re-gate.", last(result))
	assert.Empty(t, merges(w))
}

func TestRun_AHeadThatCannotBeReadAgainRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.later = &model.PullRequest{}
	assert.Equal(t, "REFUSE: head moved to  while preparing. Re-gate.", last(w.run(request())))

	w = newWorld(t)
	later := pullJSON(t, nil)
	w.later = &later
	w.fails["PRView later"] = true
	w.fails["PRView"] = false

	assert.Equal(t, "REFUSE: cannot read the head of #5 again", last(w.run(request())))
	assert.Empty(t, merges(w))
}

func TestRun_APullRequestInANativeStackIsRefused(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.stacks[5] = "S1"
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "REFUSE: #5 is in stack S1.")
	assert.Empty(t, merges(w))
}

func TestRun_AnUnreadableStackRefusesBeforeTheMerge(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["StackID 5"] = true

	assert.Equal(t, "REFUSE: cannot read which stack #5 is in", last(w.run(request())))
	assert.Empty(t, merges(w))
}

func TestRun_AFailedMergeCallRefusesWithTheForgeMessage(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["MergePR"] = true
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: MergePR: down", last(result))
}

func TestRun_AnUnreadableTipAfterTheMergeStillSaysMerged(t *testing.T) {
	t.Parallel()

	for name, after := range map[string]tipRead{
		"error":   {err: errDown},
		"missing": {},
	} {
		w := newWorld(t)
		w.tipReads = []tipRead{{sha: tipSHA, found: true}, {sha: tipSHA, found: true}, after}
		result := w.run(request())

		assert.Equal(t, merge.Done, result.Stop, name)
		assert.Equal(t, "MERGED #5 at "+sha+"; main tip is now unreadable", last(result), name)
	}
}

func TestRun_AMergeThatIsRecordedOnTheIntegrationPullRequestKeepsItsRow(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.authorOf = map[int]string{9: me}
	req := request()
	req.IntegrationPR, req.Dir = 9, t.TempDir()
	result := w.run(req)

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, "integration-row: recorded on #9", last(result))
	assert.Equal(t, "rows\n- #5 merged at "+sha, w.body)
	assert.Equal(t, []string{"MergePR 5 " + sha, "SetBody 9"}, w.writes)
}

func TestRun_ARowWithNoStateFolderRefusesBeforeTheMerge(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	req := request()
	req.IntegrationPR = 9

	assert.Equal(t, merge.Refused, w.run(req).Stop)
	assert.Empty(t, w.writes)
}

func TestRun_AFailedRowWriteLeavesTheMergeDone(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["SetBody"] = true
	w.authorOf = map[int]string{9: me}
	req := request()
	req.IntegrationPR, req.Dir = 9, t.TempDir()
	result := w.run(req)

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, "integration-row: not recorded on #9: write the body of #9: SetBody: down. Add the row by hand.", last(result))
}

func TestRun_ARowIsNeverWrittenOnAPullRequestOfAnotherAuthor(t *testing.T) {
	t.Parallel()

	for name, authors := range map[string]map[int]string{
		"another author": {9: dev},
		"no author":      {},
	} {
		w := newWorld(t)
		w.authorOf = authors
		req := request()
		req.IntegrationPR, req.Dir = 9, t.TempDir()
		result := w.run(req)

		assert.Equal(t, merge.Done, result.Stop, name)
		assert.Equal(t, []string{"MergePR 5 " + sha}, w.writes, name)
		assert.Equal(t, "rows", w.body, name)
		assert.Contains(t, last(result), "REFUSE: integration-row not recorded on #9: its author is", name)
		assert.Contains(t, last(result), "Add the row by hand.", name)
	}

	w := newWorld(t)
	w.authorOf = map[int]string{9: dev}
	req := request()
	req.IntegrationPR, req.Dir = 9, t.TempDir()
	assert.Contains(t, last(w.run(req)), `"dev"`)
}

func TestRun_ARowIsNotWrittenWhenTheAuthorCannotBeRead(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["PRView other"] = true
	req := request()
	req.IntegrationPR, req.Dir = 9, t.TempDir()
	result := w.run(req)

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, []string{"MergePR 5 " + sha}, w.writes)
	assert.Contains(t, last(result), "REFUSE: integration-row not recorded on #9: cannot read its author")
}

func TestRun_TheAuthorOfTheIntegrationPullRequestMatchesWithoutCase(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.authorOf = map[int]string{9: "ME"}
	req := request()
	req.IntegrationPR, req.Dir = 9, t.TempDir()

	assert.Equal(t, "integration-row: recorded on #9", last(w.run(req)))
}

func TestStop_IsAClosedSetWithCodes(t *testing.T) {
	t.Parallel()

	want := map[merge.Stop]int{merge.Done: 0, merge.Refused: 2, merge.NoWorkflowScope: 3, merge.OwnWork: 4}
	require.Len(t, merge.Stops(), len(want))

	for stop, code := range want {
		assert.Equal(t, code, stop.Code())
	}

	assert.Equal(t, 2, merge.Stop(99).Code())
	assert.Equal(t, "UNKNOWN", merge.Stop(99).String())
	assert.Equal(t, "OWN WORK", merge.OwnWork.String())
	assert.Equal(t, "DONE", merge.Done.String())
	assert.Equal(t, "REFUSED", merge.Refused.String())
	assert.Equal(t, "NO WORKFLOW SCOPE", merge.NoWorkflowScope.String())
}
