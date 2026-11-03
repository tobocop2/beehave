package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/github"
	"github.com/tobocop2/obee/internal/store/storetest"
)

var _ store.Writer = (*github.Store)(nil)

// Flags are checked against `gh issue create|list|view|edit|close|reopen|comment --help` of gh 2.101.0.
// Manual: https://cli.github.com/manual/gh_issue_create, https://cli.github.com/manual/gh_issue_list, https://cli.github.com/manual/gh_issue_view,
// https://cli.github.com/manual/gh_issue_edit, https://cli.github.com/manual/gh_issue_close, https://cli.github.com/manual/gh_issue_reopen,
// https://cli.github.com/manual/gh_issue_comment.

type fakeIssue struct {
	title, body string
	closed      bool
	labels      []string
	comments    []string
}

// fakeGh keeps issues in memory and answers the gh issue commands of the writer. It also plays the search of gh issue list: an issue matches when its body holds the quoted text.
type fakeGh struct {
	issues map[int]*fakeIssue
	calls  [][]string
	broken bool
	// lagging makes the search find nothing, as an index that is behind does. A list without --search still reads the issues.
	lagging bool
	// pageSize splits the comments into pages of this many, as `gh api --paginate --slurp` returns them. Zero is one page.
	pageSize int
}

func newFakeGh() *fakeGh { return &fakeGh{issues: map[int]*fakeIssue{}} }

func (f *fakeGh) Run(_ context.Context, args []string) model.CommandResult {
	f.calls = append(f.calls, slices.Clone(args))

	switch {
	case f.broken || len(args) < 2:
		return model.CommandResult{ExitCode: 1, Stderr: "boom"}
	case args[0] == "api":
		return f.api(args)
	case args[0] == "issue":
		return f.issue(args)
	}

	return model.CommandResult{ExitCode: 1, Stderr: "boom"}
}

// issue answers a gh issue command.
func (f *fakeGh) issue(args []string) model.CommandResult {
	flags := map[string]string{}
	for i := 2; i < len(args)-1; i++ {
		if strings.HasPrefix(args[i], "--") {
			flags[args[i]] = args[i+1]
		}
	}

	number := numberOf(args[2])

	switch args[1] {
	case "list":
		return f.list(flags)
	case "create":
		return f.create(flags)
	case "view":
		return f.view(number)
	case "close", "reopen", "edit", "comment":
		return f.change(args[1], number, flags)
	}

	return model.CommandResult{ExitCode: 1, Stderr: "no such command"}
}

func (f *fakeGh) list(flags map[string]string) model.CommandResult {
	rows := []map[string]any{}
	search, searching := flags["--search"]

	if searching && f.lagging {
		return model.CommandResult{Stdout: "[]"}
	}

	for number := 1; number <= len(f.issues); number++ {
		if !searching || strings.Contains(f.issues[number].body, strings.Split(search, `"`)[1]) {
			rows = append(rows, map[string]any{"number": number, "body": f.issues[number].body})
		}
	}

	return model.CommandResult{Stdout: mustMarshal(rows)}
}

// api answers `gh api --paginate --slurp repos/<repo>/issues/<n>/comments` with the comments in pages of pageSize, or in one page.
func (f *fakeGh) api(args []string) model.CommandResult {
	parts := strings.Split(args[len(args)-1], "/")
	issue, found := f.issues[numberOf(parts[len(parts)-2])]

	if !found {
		return model.CommandResult{ExitCode: 1, Stderr: "not found"}
	}

	comments := []map[string]string{}
	for _, comment := range issue.comments {
		comments = append(comments, map[string]string{"body": comment})
	}

	if f.pageSize == 0 {
		return model.CommandResult{Stdout: mustMarshal([]any{comments})}
	}

	pages := []any{}
	for page := range slices.Chunk(comments, f.pageSize) {
		pages = append(pages, page)
	}

	return model.CommandResult{Stdout: mustMarshal(pages)}
}

func (f *fakeGh) create(flags map[string]string) model.CommandResult {
	number := len(f.issues) + 1
	f.issues[number] = &fakeIssue{title: flags["--title"], body: flags["--body"]}

	return model.CommandResult{Stdout: fmt.Sprintf("https://github.com/owner/name/issues/%d\n", number)}
}

func (f *fakeGh) view(number int) model.CommandResult {
	issue, found := f.issues[number]
	if !found {
		return model.CommandResult{ExitCode: 1, Stderr: "not found"}
	}

	state := "OPEN"
	if issue.closed {
		state = "CLOSED"
	}

	labels := []map[string]string{}
	for _, label := range issue.labels {
		labels = append(labels, map[string]string{"name": label})
	}

	comments := []map[string]string{}
	for _, comment := range issue.comments {
		comments = append(comments, map[string]string{"body": comment})
	}

	return model.CommandResult{Stdout: mustMarshal(map[string]any{"state": state, "labels": labels, "comments": comments, "number": number})}
}

func (f *fakeGh) change(command string, number int, flags map[string]string) model.CommandResult {
	issue, found := f.issues[number]
	if !found {
		return model.CommandResult{ExitCode: 1, Stderr: "not found"}
	}

	switch command {
	case "close":
		issue.closed = true
	case "reopen":
		issue.closed = false
	case "comment":
		issue.comments = append(issue.comments, flags["--body"])
	case "edit":
		if label := flags["--add-label"]; label != "" {
			issue.labels = append(issue.labels, label)
		}

		if label := flags["--remove-label"]; label != "" {
			issue.labels = slices.DeleteFunc(issue.labels, func(l string) bool { return l == label })
		}
	}

	return model.CommandResult{}
}

// ghProbe reads the fake back.
type ghProbe struct{ gh *fakeGh }

func (p ghProbe) Items() int { return len(p.gh.issues) }

func (p ghProbe) Status(id string) store.Status {
	issue := p.gh.issues[numberOf(id)]

	switch {
	case issue.closed:
		return store.StatusClosed
	case slices.Contains(issue.labels, "hold"):
		return store.StatusHold
	}

	return store.StatusOpen
}

func (p ghProbe) Comments(id string) int {
	return len(p.gh.issues[numberOf(id)].comments)
}

// numberOf reads an issue number; a text that is not a number gives 0, which no fake issue has.
func numberOf(text string) int {
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}

	return n
}

func mustMarshal(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}

	return string(data)
}

func writerOver(t *testing.T, run *fakeGh) *github.Store {
	t.Helper()

	gh, err := github.New(ghRepo, run)
	require.NoError(t, err)

	return gh
}

func TestWriter_FollowsTheContract(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	storetest.CheckWriter(t, writerOver(t, fake), ghProbe{gh: fake})
}

func TestWriter_AFailedCommandIsAnErrorEverywhere(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	fake.broken = true
	storetest.CheckWriterFails(t, writerOver(t, fake))
}

func TestCreate_RunsTheExactCommands(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	id, created, err := writerOver(t, fake).Create(t.Context(), "run-1:plan", store.NewItem{Title: "the title", Body: "the body", Parent: "7"})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "1", id)

	assert.Equal(t, [][]string{
		{"issue", "list", "--repo", ghRepo, "--state", "all", "--search", `"obee-key: run-1:plan" in:body`, "--json", "number,body", "--limit", "1000"},
		{"issue", "list", "--repo", ghRepo, "--state", "all", "--json", "number,body", "--limit", "100"},
		{"issue", "create", "--repo", ghRepo, "--title", "the title", "--body", "the body\n\nobee-key: run-1:plan", "--parent", "7"},
	}, fake.calls)
}

func TestCreate_PassesTheAssigneeOnlyWhenOneIsSet(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	_, _, err := writerOver(t, fake).Create(t.Context(), "run-1:a", store.NewItem{Title: "t", Assignee: "alice"})
	require.NoError(t, err)
	assert.Equal(t, []string{"issue", "create", "--repo", ghRepo, "--title", "t", "--body", "obee-key: run-1:a", "--assignee", "alice"}, fake.calls[len(fake.calls)-1])

	_, _, err = writerOver(t, fake).Create(t.Context(), "run-1:b", store.NewItem{Title: "t"})
	require.NoError(t, err)
	assert.NotContains(t, fake.calls[len(fake.calls)-1], "--assignee", "the default is no assignee")
}

func TestAssign_AddsTheAssigneeWithOneEdit(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan("", "issue", "edit")

	s, err := github.New(ghRepo, run)
	require.NoError(t, err)
	require.NoError(t, s.Assign(t.Context(), "7", "alice"))
	assert.Equal(t, [][]string{{"issue", "edit", "7", "--repo", ghRepo, "--add-assignee", "alice"}}, run.Calls)

	down, err := github.New(ghRepo, proctest.Always(model.CommandResult{ExitCode: 1}))
	require.NoError(t, err)
	require.Error(t, down.Assign(t.Context(), "7", "alice"))
}

func TestCreate_FindsTheKeyInTheListWhenTheSearchIndexLags(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	fake.issues[1] = &fakeIssue{title: "t", body: "b\n\nobee-key: run-1:plan"}
	fake.lagging = true

	id, created, err := writerOver(t, fake).Create(t.Context(), "run-1:plan", store.NewItem{Title: "t"})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "1", id)
	assert.Len(t, fake.issues, 1)

	for _, call := range fake.calls {
		assert.NotEqual(t, "create", call[1])
	}
}

func TestCreate_RefusesAFailedOrUnreadableSecondLookup(t *testing.T) {
	t.Parallel()

	for name, plan := range map[string]func(*proctest.Scripted){
		"failed":     func(run *proctest.Scripted) { run.PlanFail("boom", "--limit", "100") },
		"unreadable": func(run *proctest.Scripted) { run.Plan("not json", "--limit", "100") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			run := &proctest.Scripted{}
			run.Plan("[]", "issue", "list")
			plan(run)

			gh, err := github.New(ghRepo, run)
			require.NoError(t, err)

			_, _, err = gh.Create(t.Context(), "k", store.NewItem{Title: "t"})
			require.Error(t, err)
			assert.False(t, run.Called("issue", "create"))
		})
	}
}

func TestCreate_AKeyThatPrefixesAnotherKeyIsNotAMatch(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	gh := writerOver(t, fake)
	_, _, err := gh.Create(t.Context(), "run-1:10", store.NewItem{Title: "ten"})
	require.NoError(t, err)

	_, created, err := gh.Create(t.Context(), "run-1:1", store.NewItem{Title: "one"})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Len(t, fake.issues, 2)
}

func TestCreate_RefusesAnAnswerWithNoIssueNumber(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan("[]", "issue", "list")
	run.Plan("created it\n", "issue", "create")

	gh, err := github.New(ghRepo, run)
	require.NoError(t, err)

	_, _, err = gh.Create(t.Context(), "k", store.NewItem{Title: "t"})
	require.Error(t, err)
	assert.Equal(t, store.CodeRefused, codeOf(t, err))
}

func TestCreate_AFailedCreateIsARefusal(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan("[]", "issue", "list")
	run.PlanFail("boom", "issue", "create")

	gh, err := github.New(ghRepo, run)
	require.NoError(t, err)

	_, _, err = gh.Create(t.Context(), "k", store.NewItem{Title: "t"})
	require.Error(t, err)
	assert.Equal(t, store.CodeRefused, codeOf(t, err))
}

func TestCreate_RefusesAnUnreadableList(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan("not json", "issue", "list")

	gh, err := github.New(ghRepo, run)
	require.NoError(t, err)

	_, _, err = gh.Create(t.Context(), "k", store.NewItem{Title: "t"})
	require.Error(t, err)
	assert.False(t, run.Called("issue", "create"))
}

func TestCreate_RefusesAFullPageOfMatches(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(toJSON(t, fill(1000, map[string]any{"body": "x"})), "issue", "list")

	gh, err := github.New(ghRepo, run)
	require.NoError(t, err)

	_, _, err = gh.Create(t.Context(), "k", store.NewItem{Title: "t"})
	require.Error(t, err)
	assert.False(t, run.Called("issue", "create"))
}

func TestSetStatus_RunsTheExactCommands(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		start  func(*fakeGh)
		status store.Status
		want   [][]string
	}{
		"close an open issue": {func(*fakeGh) {}, store.StatusClosed, [][]string{
			{"issue", "close", "1", "--repo", ghRepo},
		}},
		"hold an open issue": {func(*fakeGh) {}, store.StatusHold, [][]string{
			{"issue", "edit", "1", "--repo", ghRepo, "--add-label", "hold"},
		}},
		"hold a closed issue": {func(f *fakeGh) { f.issues[1].closed = true }, store.StatusHold, [][]string{
			{"issue", "reopen", "1", "--repo", ghRepo},
			{"issue", "edit", "1", "--repo", ghRepo, "--add-label", "hold"},
		}},
		"open a held issue": {func(f *fakeGh) { f.issues[1].labels = []string{"hold"} }, store.StatusOpen, [][]string{
			{"issue", "edit", "1", "--repo", ghRepo, "--remove-label", "hold"},
		}},
		"open a closed issue": {func(f *fakeGh) { f.issues[1].closed = true }, store.StatusOpen, [][]string{
			{"issue", "reopen", "1", "--repo", ghRepo},
		}},
		"open an open issue":   {func(*fakeGh) {}, store.StatusOpen, nil},
		"close a closed issue": {func(f *fakeGh) { f.issues[1].closed = true }, store.StatusClosed, nil},
	} {
		fake := newFakeGh()
		fake.issues[1] = &fakeIssue{title: "t"}
		tc.start(fake)

		require.NoError(t, writerOver(t, fake).SetStatus(t.Context(), "1", tc.status), name)

		view := []string{"issue", "view", "1", "--repo", ghRepo, "--json", "state,labels"}
		assert.Equal(t, append([][]string{view}, tc.want...), fake.calls, name)
	}
}

func TestSetStatus_RefusesAnIdThatIsNotANumber(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	err := writerOver(t, fake).SetStatus(t.Context(), "abc", store.StatusClosed)
	require.Error(t, err)
	assert.Equal(t, store.CodeRefused, codeOf(t, err))
	assert.Empty(t, fake.calls)
}

func TestSetStatus_RefusesAnUnreadableView(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan("not json", "issue", "view")

	gh, err := github.New(ghRepo, run)
	require.NoError(t, err)
	require.Error(t, gh.SetStatus(t.Context(), "1", store.StatusClosed))
}

func TestSetStatus_ReportsEachFailedStep(t *testing.T) {
	t.Parallel()

	for _, failing := range []string{"reopen", "edit", "close"} {
		run := &proctest.Scripted{}
		run.Plan(`{"state":"CLOSED","labels":[]}`, "issue", "view")

		status := store.StatusHold
		if failing == "close" {
			run.Plan(`{"state":"OPEN","labels":[]}`, "issue", "view")

			status = store.StatusClosed
		}

		for _, command := range []string{"reopen", "edit", "close"} {
			run.Plan("", "issue", command)
		}

		run.PlanFail("boom", "issue", failing)

		gh, err := github.New(ghRepo, run)
		require.NoError(t, err)
		require.Error(t, gh.SetStatus(t.Context(), "1", status), failing)
	}
}

func TestSetStatus_AHoldWithNoLabelInTheRepositoryNamesTheLabel(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(`{"state":"OPEN","labels":[]}`, "issue", "view")
	run.PlanFail("label not found", "issue", "edit")

	gh, err := github.New(ghRepo, run)
	require.NoError(t, err)

	err = gh.SetStatus(t.Context(), "1", store.StatusHold)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `label named "hold"`)
}

func TestComment_RunsTheExactCommands(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	fake.issues[3] = &fakeIssue{title: "t"}

	require.NoError(t, writerOver(t, fake).Comment(t.Context(), "3", "note", "the text"))
	assert.Equal(t, [][]string{
		{"api", "--paginate", "--slurp", "repos/" + ghRepo + "/issues/3/comments"},
		{"issue", "comment", "3", "--repo", ghRepo, "--body", "the text\n\nobee-key: note"},
	}, fake.calls)
}

func TestComment_FindsTheMarkerOnALaterPage(t *testing.T) {
	t.Parallel()

	fake := newFakeGh()
	fake.pageSize = 1
	fake.issues[3] = &fakeIssue{title: "t", comments: []string{"first", "the text\n\nobee-key: note"}}

	require.NoError(t, writerOver(t, fake).Comment(t.Context(), "3", "note", "the text"))
	assert.Len(t, fake.issues[3].comments, 2)
	assert.Len(t, fake.calls, 1)
}

func TestComment_RefusesAnUnreadableViewAndAnIdThatIsNotANumber(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan("not json", "api")

	gh, err := github.New(ghRepo, run)
	require.NoError(t, err)
	require.Error(t, gh.Comment(t.Context(), "1", "k", "t"))
	require.Error(t, gh.Comment(t.Context(), "x", "k", "t"))
}
