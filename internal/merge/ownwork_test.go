package merge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

// ownWorkCases are the pull requests that obee must never merge. Each one refuses before the first write.
func ownWorkCases() map[string]map[string]any {
	return map[string]map[string]any{
		"the author of the pull request is me":      {"author": map[string]any{"login": me}},
		"the author is me in other case":            {"author": map[string]any{"login": "ME"}},
		"the author is a runner login":              {"author": map[string]any{"login": "Bee-1"}},
		"a commit of ours in a pull request of dev": {"commits": []any{commitJSON("dev@x", dev, "fix", ""), commitJSON("x@x", me, "mine", "")}},
		"a commit with our login in other case":     {"commits": []any{commitJSON("x@x", "ME", "mine", "")}},
		"a commit with our address in other case":   {"commits": []any{commitJSON("ME@X", "other", "mine", "")}},
		"a commit with our address and no login":    {"commits": []any{commitJSON(meMail, "", "mine", "")}},
		"a commit by a runner login":                {"commits": []any{commitJSON("x@x", "bee-1", "mine", "")}},
	}
}

func TestOwnWork_RefusesBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	for name, over := range ownWorkCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t).set(t, over)
			result := w.run(request())

			assert.Equal(t, merge.OwnWork, result.Stop)
			assert.Contains(t, result.Lines[len(result.Lines)-1], "owner merges it")
			assert.Empty(t, w.writes)
			assert.False(t, w.called("PRChecks"))
			assert.False(t, w.called("TokenScopes"))
		})
	}
}

func TestOwnWork_TheTextNamesTheLogin(t *testing.T) {
	t.Parallel()

	result := newWorld(t).set(t, map[string]any{"author": map[string]any{"login": "bee-1"}}).run(request())

	assert.Contains(t, result.Lines[0], "bee-1")
	assert.Contains(t, result.Lines[0], "#5")
}

func TestOwnWork_NoFlagLiftsTheRefusal(t *testing.T) {
	t.Parallel()

	for name, over := range ownWorkCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t).set(t, over)
			req := request()
			req.AllowOrphans, req.AllowNoChecks, req.AllowReady = true, true, true

			assert.Equal(t, merge.OwnWork, w.run(req).Stop)
			assert.Empty(t, w.writes)
		})
	}
}

func TestOwnWork_ASecondAuthorOfACommitCounts(t *testing.T) {
	t.Parallel()

	two := commitJSON("dev@x", dev, "pair", "")
	two["authors"] = []any{map[string]any{"email": "dev@x", "login": dev}, map[string]any{"email": meMail, "login": "x"}}

	w := newWorld(t).set(t, map[string]any{"commits": []any{two}})

	assert.Equal(t, merge.OwnWork, w.run(request()).Stop)
	assert.Empty(t, w.writes)
}

func TestOwnWork_AMissingLoginOrAddressRefuses(t *testing.T) {
	t.Parallel()

	blanks := map[string]func(*merge.Request){
		"no owner login":   func(r *merge.Request) { r.OwnLogins = nil },
		"no owner address": func(r *merge.Request) { r.OwnEmails = nil },
		"an empty login":   func(r *merge.Request) { r.OwnLogins = []string{me, ""} },
		"an empty address": func(r *merge.Request) { r.OwnEmails = []string{""} },
	}
	for missing, blank := range blanks {
		t.Run(missing, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			req := request()
			blank(&req)
			result := w.run(req)

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Contains(t, result.Lines[0], "the owner logins and the owner addresses must both be set")
			assert.Empty(t, w.calls, "no read before the guard")
			assert.Empty(t, w.writes)
		})
	}
}

func TestOwnWork_AReadThatFailsRefusesBeforeAnyOtherRead(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["PRView"] = true
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, []string{"REFUSE: cannot read #5 for its state"}, result.Lines)
	assert.Len(t, w.calls, 1)
}

func TestOwnWork_AnEmptyNameIsNotOurs(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"author": map[string]any{"login": ""}, "commits": []any{commitJSON("dev@x", dev, "fix", "")}})
	req := request()
	req.Runners = []string{"", "bee-1"}

	assert.Equal(t, merge.Done, w.run(req).Stop)
}

func TestOwnWork_ADraftOfOursStaysADraftEvenWithAllowReady(t *testing.T) {
	t.Parallel()

	w := newWorld(t).set(t, map[string]any{"isDraft": true, "author": map[string]any{"login": me}})
	req := request()
	req.AllowReady = true

	assert.Equal(t, merge.OwnWork, w.run(req).Stop)
	assert.Empty(t, w.writes)
}

func TestOwnWork_ACommitListThatTheForgeCutRefuses(t *testing.T) {
	t.Parallel()

	commits := make([]any, 0, 100)
	for range 100 {
		commits = append(commits, commitJSON("dev@x", dev, "fix", ""))
	}

	w := newWorld(t).set(t, map[string]any{"commits": commits})
	w.counts = &counts{commits: 101, files: 1}
	result := w.run(request())

	assert.Equal(t, merge.OwnWork, result.Stop)
	assert.Contains(t, result.Lines[0], "list of commits is incomplete")
	assert.Empty(t, w.writes)
	assert.False(t, w.called("PRChecks"))
}

func TestOwnWork_ACountThatCannotBeReadRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["PullCounts"] = true
	result := w.run(request())

	assert.Equal(t, merge.OwnWork, result.Stop)
	assert.Contains(t, result.Lines[0], "cannot be counted")
	assert.Empty(t, w.writes)
	assert.False(t, w.called("PRChecks"))
}

func TestOwnWork_EqualCountsPass(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.counts = &counts{commits: 1, files: 1}

	assert.Equal(t, merge.Done, w.run(request()).Stop)
}

func TestOwnWork_ACommitterThatIsOursCountsLikeAnAuthor(t *testing.T) {
	t.Parallel()

	for name, committer := range map[string]model.Committer{
		"login": {Login: me}, "address": {Email: meMail}, "runner": {Login: "bee-1"}, "case": {Login: "ME"},
	} {
		w := newWorld(t)
		w.committers = []model.Committer{committer}
		result := w.run(request())

		assert.Equal(t, merge.OwnWork, result.Stop, name)
		assert.Contains(t, result.Lines[0], "the committer of a commit", name)
		assert.Empty(t, w.writes, name)

		approved := request()
		approved.Approved = true
		w = newWorld(t)
		w.committers = []model.Committer{committer}

		assert.Equal(t, merge.Done, w.run(approved).Stop, name)
	}
}

func TestOwnWork_ACommitterThatIsNotOursOrNotNamedMerges(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.committers = []model.Committer{{Login: "web-flow", Email: "noreply@github.com"}}

	assert.Equal(t, merge.Done, w.run(request()).Stop)
}

func TestOwnWork_CommittersThatCannotBeReadOrAreFewerThanTheCommitsRefuse(t *testing.T) {
	t.Parallel()

	failing := newWorld(t)
	failing.fails["Committers"] = true
	result := failing.run(request())

	assert.Equal(t, merge.OwnWork, result.Stop)
	assert.Contains(t, result.Lines[0], "committers of #5 cannot be read")
	assert.Empty(t, failing.writes)

	short := newWorld(t)
	short.committers = []model.Committer{}
	result = short.run(request())

	assert.Equal(t, merge.OwnWork, result.Stop)
	assert.Contains(t, result.Lines[0], "list of committers is incomplete for #5 (0 read, 1 in the forge)")
	assert.Empty(t, short.writes)
}

func TestOwnWork_AThirdPartyPullRequestStillMerges(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	result := w.run(request())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, []string{"MergePR 5 " + sha}, w.writes)
}
