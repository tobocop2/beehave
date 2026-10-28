package merge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
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
	w.views++

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

func (w *world) BranchRef(context.Context, string) (string, bool, error) {
	w.calls = append(w.calls, "BranchRef")

	return w.tip, w.tipFound, w.fail("BranchRef")
}

func (w *world) SetDraft(_ context.Context, number int, draft bool) error {
	w.writes = append(w.writes, fmt.Sprintf("SetDraft %d %t", number, draft))

	return w.fail("SetDraft")
}

func (w *world) MergePR(_ context.Context, number int, pin string) error {
	w.writes = append(w.writes, fmt.Sprintf("MergePR %d %s", number, pin))

	return w.fail("MergePR")
}

func (w *world) PRBody(context.Context, int) (string, error) {
	return w.body, w.fail("PRBody")
}

func (w *world) SetBody(_ context.Context, number int, body string) error {
	if err := w.fail("SetBody"); err != nil {
		return err
	}

	w.writes = append(w.writes, fmt.Sprintf("SetBody %d", number))
	w.body = body

	return nil
}

func settings(over map[string]string) config.Settings {
	env := map[string]string{"REPO": "owner/name", "BASE": "main", "ROUND": "r1", "ME_LOGIN": me, "ME_EMAIL": meMail}
	for key, value := range over {
		env[key] = value
	}

	return config.NewSettings(env)
}

func request() merge.Request {
	return merge.Request{PR: pr, Gated: sha, Settings: settings(nil), Runners: []string{"bee-1"}}
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
