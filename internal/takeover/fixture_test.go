package takeover_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/takeover"
)

var (
	now     = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	errDown = errors.New("down")
)

type obj = map[string]any

func ago(hours float64) string {
	return now.Add(-time.Duration(hours * float64(time.Hour))).Format("2006-01-02T15:04:05Z")
}

func login(name string) obj { return obj{"login": name} }

// spec holds the few knobs of a pull request: the age of the last human comment, a notice of ours (0 for none) and a reply (empty for none).
type spec struct {
	assignees []string
	age       float64
	notice    float64
	reply     string
	replyAge  float64
	noReview  bool
	author    string
	commits   []obj
}

// setup is the fake forge and the calls of the round. Calls lists the free, post and take calls in order.
type setup struct {
	pr      obj
	rows    []model.Comment
	viewErr error
	rowsErr error
	postErr error
	freeErr error
	calls   []string
	posted  []string
	asked   []string
	idle    time.Duration
	grace   time.Duration
}

func newSetup(s spec) *setup {
	author := s.author
	if author == "" {
		author = "them"
	}

	comments := []obj{{"author": login("them"), "createdAt": ago(s.age), "body": "work in progress"}}
	if s.notice != 0 {
		comments = append(comments, obj{"author": login("me"), "createdAt": ago(s.notice), "body": "Taking over this pull request: it is assigned to me"})
	}

	if s.reply != "" {
		comments = append(comments, obj{"author": login("them"), "createdAt": ago(s.replyAge), "body": s.reply})
	}

	state := "CHANGES_REQUESTED"
	if s.noReview {
		state = "COMMENTED"
	}

	assignees := []obj{}
	for _, name := range s.assignees {
		assignees = append(assignees, login(name))
	}

	return &setup{
		idle: time.Hour, grace: time.Hour,
		pr: obj{
			"number": 7, "state": "OPEN", "author": login(author), "assignees": assignees, "headRefOid": "aaa",
			"commits": s.commits, "comments": comments,
			"reviews": []obj{{"author": login("me"), "state": state, "submittedAt": "2020-01-01T00:00:00Z", "body": "x"}},
		},
	}
}

func (s *setup) PRView(_ context.Context, _ int, _ ...string) (model.PullRequest, error) {
	s.asked = append(s.asked, "view")

	if s.viewErr != nil {
		return model.PullRequest{}, s.viewErr
	}

	data, err := json.Marshal(s.pr)
	if err != nil {
		return model.PullRequest{}, err
	}

	return model.Decode[model.PullRequest]("pr view", string(data))
}

func (s *setup) ReviewComments(context.Context, int) ([]model.Comment, error) {
	s.asked = append(s.asked, "rows")

	return s.rows, s.rowsErr
}

func (s *setup) PostComment(_ context.Context, _ int, body string) error {
	s.calls = append(s.calls, "post")
	s.posted = append(s.posted, body)

	return s.postErr
}

func (s *setup) context() takeover.Context {
	return takeover.Context{
		Me: "me", Idle: s.idle, Grace: s.grace, Now: now,
		Take: func(context.Context) error { s.calls = append(s.calls, "take"); return nil },
		Free: func(context.Context) error { s.calls = append(s.calls, "free"); return s.freeErr },
	}
}

func (s *setup) run(t *testing.T, mode takeover.Mode, mutate ...func(*takeover.Request)) takeover.Result {
	t.Helper()

	got, err := s.try(mode, mutate...)
	require.NoError(t, err)

	return got
}

func (s *setup) try(mode takeover.Mode, mutate ...func(*takeover.Request)) (takeover.Result, error) {
	request := takeover.Request{PR: 7, Mode: mode}
	for _, change := range mutate {
		change(&request)
	}

	return takeover.Run(context.Background(), s, request, s.context())
}

func (s *setup) count(call string) int {
	n := 0

	for _, got := range s.calls {
		if got == call {
			n++
		}
	}

	return n
}

func (s *setup) comments() []obj {
	list, ok := s.pr["comments"].([]obj)
	if !ok {
		return nil
	}

	return list
}

func all(r takeover.Result) string { return strings.Join(r.Lines, "\n") }

func last(r takeover.Result) string { return r.Lines[len(r.Lines)-1] }
