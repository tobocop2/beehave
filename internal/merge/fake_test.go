package merge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

const (
	sha    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	moved  = "ffffffffffffffffffffffffffffffffffffffff"
	tipSHA = "cccccccccccccccccccccccccccccccccccccccc"
	pr     = 5
	me     = "me"
	meMail = "me@x"
	dev    = "dev"
)

var errDown = errors.New("down")

// world is the fake forge. A name in fails makes the method of that name fail. writes lists every call that changes the forge.
type world struct {
	first, later *model.PullRequest
	children     []model.PullRequest
	checks       []model.Check
	scopes       []string
	requests     []model.ChangeRequest
	threads      []model.ReviewThread
	stacks       map[int]string
	stackQueue   []string
	tip          string
	tipFound     bool
	body         string
	fails        map[string]bool
	views        int
	calls        []string
	writes       []string
	listed       []string
	// counts is what the forge counts for the pull request. Nil means the count of the first read.
	counts *counts
	// tipReads are the answers of the next reads of the base tip, in order. When they are used up, the read gives tip.
	tipReads []tipRead
	// failBase names the pull requests whose move onto a new base fails.
	failBase map[int]bool
	// tipsOf are the branches whose tip was read.
	tipsOf []string
	// committers are the committers the forge reports. Nil means one unnamed committer for each commit.
	committers []model.Committer
	// reviews are the reviews of the pull request, oldest first.
	reviews []model.Review
	// behind makes the head not an ancestor of the base after the merge. ancestryErr makes that read fail.
	behind      bool
	ancestryErr bool
	// authorOf is the author login of each pull request other than the merged one. A number not in the map has no author.
	authorOf map[int]string
	// failAt makes the read of that number fail, counted from 1 over all reads of the pull request. Zero fails none.
	failAt int
	// closingRefs is what the forge lists as closing references. Nil means the forge cannot read them, and the read fails.
	closingRefs *model.ClosingRefs
}

func (w *world) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	w.calls = append(w.calls, "ClosingRefs")

	if w.closingRefs == nil {
		return model.ClosingRefs{}, errors.New("this forge cannot read closing references")
	}

	return *w.closingRefs, nil
}

func (w *world) Reviews(context.Context, int) ([]model.Review, error) {
	w.calls = append(w.calls, "Reviews")

	return w.reviews, w.fail("Reviews")
}

func (w *world) Compare(_ context.Context, commit, base string) (model.Comparison, error) {
	w.calls = append(w.calls, "Compare "+commit+" "+base)

	if w.ancestryErr {
		return model.Comparison{}, fmt.Errorf("Compare: %w", errDown)
	}

	if w.behind {
		return model.Comparison{Status: "diverged", AheadBy: 4, BehindBy: 2}, nil
	}

	return model.Comparison{Status: "ahead", AheadBy: 1}, nil
}

// tipRead is one answer of a read of a branch tip.
type tipRead struct {
	sha   string
	found bool
	err   error
}

// counts are the forge's own numbers of commits and changed files.
type counts struct{ commits, files int }

// pullJSON decodes a pull request of the default shape with the keys of over replaced.
func pullJSON(t *testing.T, over map[string]any) model.PullRequest {
	t.Helper()

	row := map[string]any{
		"number": pr, "state": "OPEN", "isDraft": false, "baseRefName": "main", "headRefName": "feat",
		"headRefOid": sha, "mergeable": "MERGEABLE", "body": "text", "author": map[string]any{"login": dev},
		"files":   []any{map[string]any{"path": "a.go"}},
		"commits": []any{commitJSON(dev+"@x", dev, "fix", "")},
	}
	for key, value := range over {
		row[key] = value
	}

	text, err := json.Marshal(row)
	require.NoError(t, err)

	view, err := model.Decode[model.PullRequest]("test", string(text))
	require.NoError(t, err)

	return view
}

func commitJSON(email, login, headline, body string) map[string]any {
	author := map[string]any{"email": email}
	if login != "" {
		author["login"] = login
	}

	return map[string]any{"oid": strings.Repeat("b", 40), "messageHeadline": headline, "messageBody": body, "authors": []any{author}}
}

func newWorld(t *testing.T) *world {
	t.Helper()

	first := pullJSON(t, nil)

	return &world{
		first: &first, checks: []model.Check{{Name: "ci", Bucket: model.CheckPass}}, scopes: []string{"repo", "workflow"},
		stacks: map[int]string{}, tip: tipSHA, tipFound: true, body: "rows", fails: map[string]bool{},
	}
}

// set replaces keys of the pull request the first read returns.
func (w *world) set(t *testing.T, over map[string]any) *world {
	t.Helper()

	view := pullJSON(t, over)
	w.first = &view

	return w
}

func (w *world) fail(name string) error {
	if w.fails[name] {
		return fmt.Errorf("%s: %w", name, errDown)
	}

	return nil
}

func (w *world) PRView(_ context.Context, number int, fields ...string) (model.PullRequest, error) {
	w.calls = append(w.calls, fmt.Sprintf("PRView %d %s", number, strings.Join(fields, ",")))
	if number != pr {
		if err := w.fail("PRView other"); err != nil {
			return model.PullRequest{}, err
		}

		return model.PullRequest{Author: model.Account{Login: w.authorOf[number]}}, nil
	}

	w.views++

	if w.views == w.failAt {
		return model.PullRequest{}, fmt.Errorf("PRView %d: %w", w.views, errDown)
	}

	if err := w.fail("PRView"); err != nil {
		return model.PullRequest{}, err
	}

	if w.views > 1 && w.later != nil {
		return *w.later, w.fail("PRView later")
	}

	return *w.first, nil
}

func (w *world) PullCounts(context.Context, int) (commits, files int, err error) {
	w.calls = append(w.calls, "PullCounts")

	if err := w.fail("PullCounts"); err != nil {
		return 0, 0, err
	}

	if w.counts != nil {
		return w.counts.commits, w.counts.files, nil
	}

	return len(w.first.Commits), len(w.first.Files), nil
}

func (w *world) PRList(_ context.Context, limit int, args ...string) ([]model.PullRequest, error) {
	w.calls = append(w.calls, "PRList")
	w.listed = args

	if err := w.fail("PRList"); err != nil {
		return nil, err
	}

	if len(w.children) >= limit {
		return nil, errDown
	}

	return w.children, nil
}

func (w *world) PRChecks(context.Context, int) ([]model.Check, error) {
	w.calls = append(w.calls, "PRChecks")

	return w.checks, w.fail("PRChecks")
}

func (w *world) OpenChangeRequests(context.Context, int) ([]model.ChangeRequest, error) {
	w.calls = append(w.calls, "OpenChangeRequests")

	return w.requests, w.fail("OpenChangeRequests")
}

func (w *world) UnresolvedThreads(context.Context, int) ([]model.ReviewThread, error) {
	w.calls = append(w.calls, "UnresolvedThreads")

	return w.threads, w.fail("UnresolvedThreads")
}

func (w *world) StackID(_ context.Context, number int) (string, error) {
	w.calls = append(w.calls, fmt.Sprintf("StackID %d", number))

	if err := w.fail(fmt.Sprintf("StackID %d", number)); err != nil {
		return "", err
	}

	if len(w.stackQueue) > 0 {
		next := w.stackQueue[0]
		w.stackQueue = w.stackQueue[1:]

		return next, nil
	}

	return w.stacks[number], nil
}

func (w *world) TokenScopes(context.Context) ([]string, error) {
	w.calls = append(w.calls, "TokenScopes")

	return w.scopes, w.fail("TokenScopes")
}

func (w *world) BranchRef(_ context.Context, branch string) (string, bool, error) {
	w.calls = append(w.calls, "BranchRef")
	w.tipsOf = append(w.tipsOf, branch)

	if len(w.tipReads) > 0 {
		next := w.tipReads[0]
		w.tipReads = w.tipReads[1:]

		return next.sha, next.found, next.err
	}

	return w.tip, w.tipFound, w.fail("BranchRef")
}

// Committers returns the committers of the world, or one unnamed committer for each commit of the first read.
func (w *world) Committers(context.Context, int) ([]model.Committer, error) {
	w.calls = append(w.calls, "Committers")

	if err := w.fail("Committers"); err != nil {
		return nil, err
	}

	if w.committers != nil {
		return w.committers, nil
	}

	return make([]model.Committer, len(w.first.Commits)), nil
}

func (w *world) SetDraft(_ context.Context, number int, draft bool) error {
	w.writes = append(w.writes, fmt.Sprintf("SetDraft %d %t", number, draft))

	return w.fail("SetDraft")
}

func (w *world) SetBase(_ context.Context, number int, base string) error {
	w.writes = append(w.writes, fmt.Sprintf("SetBase %d %s", number, base))

	if w.failBase[number] {
		return fmt.Errorf("SetBase %d: %w", number, errDown)
	}

	return w.fail("SetBase")
}

func (w *world) MergePR(_ context.Context, number int, pin string) error {
	w.writes = append(w.writes, fmt.Sprintf("MergePR %d %s", number, pin))

	return w.fail("MergePR")
}

func (w *world) PRBody(context.Context, int) (string, error) {
	return w.body, w.fail("PRBody")
}

// SetBody is the write of the forge: like the GitHub forge, it refuses a pull request that the owner did not write and an author that cannot be read.
func (w *world) SetBody(_ context.Context, number int, body string) error {
	if w.fail("PRView other") != nil {
		return &model.NotAuthorError{Reason: fmt.Sprintf("cannot read its author: %v", errDown)}
	}

	if author := w.authorOf[number]; !strings.EqualFold(author, me) {
		return &model.NotAuthorError{Reason: fmt.Sprintf("its author is %q, not the owner", author)}
	}

	if err := w.fail("SetBody"); err != nil {
		return err
	}

	w.writes = append(w.writes, fmt.Sprintf("SetBody %d", number))
	w.body = body

	return nil
}

// request is a merge request of the default shape. Its forbidden text is the `forbid_text` of the remediation example, so the engine holds no pattern of its own.
func request() merge.Request {
	return merge.Request{
		PR: pr, Gated: sha, Runners: []string{"bee-1"},
		Repo: "owner/name", Base: "main", OwnLogins: []string{me}, OwnEmails: []string{meMail},
		ForbidText: compiled(examplePatterns()),
	}
}

func (w *world) run(req merge.Request) merge.Result {
	return merge.Run(context.Background(), w, req)
}

func merges(w *world) []string {
	var found []string

	for _, write := range w.writes {
		if strings.HasPrefix(write, "MergePR") {
			found = append(found, write)
		}
	}

	return found
}

func (w *world) called(prefix string) bool {
	for _, call := range w.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}

	return false
}
