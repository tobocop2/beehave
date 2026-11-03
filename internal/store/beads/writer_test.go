package beads_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/beads"
	"github.com/tobocop2/obee/internal/store/storetest"
)

var _ store.Writer = (*beads.Store)(nil)

// Flags are checked against `bd create|update|close|reopen|comments|list --help` of bd 0.56.1. No bd write ran.

type fakeIssue struct {
	title, description string
	status             string
	labels             []string
	comments           []string
}

// writeBd keeps issues in memory and answers the bd commands of the writer.
type writeBd struct {
	order  []string
	issues map[string]*fakeIssue
	calls  [][]string
	broken bool
}

func newWriteBd() *writeBd { return &writeBd{issues: map[string]*fakeIssue{}} }

func (f *writeBd) Run(_ context.Context, args []string) model.CommandResult {
	f.calls = append(f.calls, slices.Clone(args))

	if f.broken || len(args) == 0 {
		return model.CommandResult{ExitCode: 1, Stderr: "boom"}
	}

	switch args[0] {
	case "list":
		return f.list(flagsOf(args))
	case "create":
		return f.create(flagsOf(args))
	case "show":
		return f.show(args[1])
	case "comments":
		return f.comments(args)
	case "close":
		f.issues[args[1]].status = "closed"
	case "reopen":
		f.issues[args[1]].status = "open"
	case "update":
		f.update(args[1], flagsOf(args))
	}

	return model.CommandResult{}
}

func flagsOf(args []string) map[string]string {
	flags := map[string]string{}

	for i := 1; i < len(args)-1; i++ {
		if strings.HasPrefix(args[i], "--") {
			flags[args[i]] = args[i+1]
		}
	}

	return flags
}

func (f *writeBd) list(flags map[string]string) model.CommandResult {
	rows := []map[string]any{}

	for _, id := range f.order {
		if strings.Contains(f.issues[id].description, flags["--desc-contains"]) {
			rows = append(rows, map[string]any{"id": id, "title": f.issues[id].title, "description": f.issues[id].description})
		}
	}

	return model.CommandResult{Stdout: mustMarshal(rows)}
}

func mustMarshal(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}

	return string(data)
}

func (f *writeBd) create(flags map[string]string) model.CommandResult {
	id := fmt.Sprintf("p-%d", len(f.order)+1)
	f.order = append(f.order, id)
	f.issues[id] = &fakeIssue{title: flags["--title"], description: flags["--description"], status: "open"}

	return model.CommandResult{Stdout: id + "\n"}
}

func (f *writeBd) show(id string) model.CommandResult {
	issue, found := f.issues[id]
	if !found {
		return model.CommandResult{ExitCode: 1, Stderr: "not found"}
	}

	return model.CommandResult{Stdout: mustMarshal([]map[string]any{{"id": id, "title": issue.title, "status": issue.status, "labels": issue.labels}})}
}

func (f *writeBd) comments(args []string) model.CommandResult {
	if args[1] == "add" {
		f.issues[args[2]].comments = append(f.issues[args[2]].comments, args[len(args)-1])

		return model.CommandResult{}
	}

	return f.listComments(args[1])
}

func (f *writeBd) listComments(id string) model.CommandResult {
	rows := []map[string]string{}
	for _, text := range f.issues[id].comments {
		rows = append(rows, map[string]string{"text": text})
	}

	return model.CommandResult{Stdout: mustMarshal(rows)}
}

func (f *writeBd) update(id string, flags map[string]string) {
	if label := flags["--add-label"]; label != "" {
		f.issues[id].labels = append(f.issues[id].labels, label)
	}

	if label := flags["--remove-label"]; label != "" {
		f.issues[id].labels = slices.DeleteFunc(f.issues[id].labels, func(l string) bool { return l == label })
	}
}

type bdProbe struct{ bd *writeBd }

func (p bdProbe) Items() int { return len(p.bd.order) }

func (p bdProbe) Status(id string) store.Status {
	issue := p.bd.issues[id]

	switch {
	case issue.status == "closed":
		return store.StatusClosed
	case slices.Contains(issue.labels, "hold"):
		return store.StatusHold
	}

	return store.StatusOpen
}

func (p bdProbe) Comments(id string) int { return len(p.bd.issues[id].comments) }

func writerOver(t *testing.T, run *writeBd) *beads.Store {
	t.Helper()

	bd, err := beads.New(run, "p")
	require.NoError(t, err)

	return bd
}

func TestWriter_FollowsTheContract(t *testing.T) {
	t.Parallel()

	fake := newWriteBd()
	storetest.CheckWriter(t, writerOver(t, fake), bdProbe{bd: fake})
}

func TestWriter_AFailedCommandIsAnErrorEverywhere(t *testing.T) {
	t.Parallel()

	fake := newWriteBd()
	fake.broken = true
	storetest.CheckWriterFails(t, writerOver(t, fake))
}

func TestCreate_RunsTheExactCommands(t *testing.T) {
	t.Parallel()

	fake := newWriteBd()
	id, created, err := writerOver(t, fake).Create(t.Context(), "run-1:plan", store.NewItem{Title: "the title", Body: "the body", Parent: "p-9"})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "p-1", id)

	assert.Equal(t, [][]string{
		{"list", "--all", "--desc-contains", "obee-key: run-1:plan", "--limit", "5000", "--json"},
		{"create", "--title", "the title", "--description", "the body\n\nobee-key: run-1:plan", "--parent", "p-9", "--silent"},
	}, fake.calls)
}

func TestCreate_PassesTheAssigneeOnlyWhenOneIsSet(t *testing.T) {
	t.Parallel()

	fake := newWriteBd()
	_, _, err := writerOver(t, fake).Create(t.Context(), "run-1:a", store.NewItem{Title: "t", Assignee: "alice"})
	require.NoError(t, err)
	assert.Equal(t, []string{"create", "--title", "t", "--description", "obee-key: run-1:a", "--assignee", "alice", "--silent"}, fake.calls[1])

	_, _, err = writerOver(t, fake).Create(t.Context(), "run-1:b", store.NewItem{Title: "t"})
	require.NoError(t, err)
	assert.NotContains(t, fake.calls[3], "--assignee", "the default is no assignee")
}

func TestAssign_SetsTheAssigneeOfAnItemThatHasNoneOrTheSameOne(t *testing.T) {
	t.Parallel()

	for name, shown := range map[string]string{
		"nobody": `[{"id":"p-1","title":"t","status":"open"}]`,
		"same":   `[{"id":"p-1","title":"t","status":"open","assignee":"alice"}]`,
	} {
		run := &proctest.Scripted{}
		run.Plan(shown, "show")
		run.Plan("", "update")

		bd, err := beads.New(run, "p")
		require.NoError(t, err)
		require.NoError(t, bd.Assign(t.Context(), "p-1", "alice"), name)
		assert.Equal(t, [][]string{{"show", "p-1", "--json"}, {"update", "p-1", "--assignee", "alice"}}, run.Calls, name)
	}
}

func TestAssign_RefusesAnItemThatAnotherLoginHoldsAndWritesNothing(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(`[{"id":"p-1","title":"t","status":"open","assignee":"bob"}]`, "show")

	bd, err := beads.New(run, "p")
	require.NoError(t, err)

	err = bd.Assign(t.Context(), "p-1", "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bob")
	assert.Contains(t, err.Error(), "keeps one assignee")
	assert.Equal(t, [][]string{{"show", "p-1", "--json"}}, run.Calls)
}

func TestAssign_RefusesWhenTheItemCannotBeReadOrUpdated(t *testing.T) {
	t.Parallel()

	down, err := beads.New(proctest.Always(model.CommandResult{ExitCode: 1}), "p")
	require.NoError(t, err)
	require.Error(t, down.Assign(t.Context(), "p-1", "alice"))

	run := &proctest.Scripted{}
	run.Plan(`[{"id":"p-1","title":"t","status":"open"}]`, "show")
	run.PlanFail("bd failed", "update")

	failing, err := beads.New(run, "p")
	require.NoError(t, err)
	require.Error(t, failing.Assign(t.Context(), "p-1", "alice"))
}

func TestCreate_AKeyThatPrefixesAnotherKeyIsNotAMatch(t *testing.T) {
	t.Parallel()

	fake := newWriteBd()
	bd := writerOver(t, fake)
	_, _, err := bd.Create(t.Context(), "run-1:10", store.NewItem{Title: "ten"})
	require.NoError(t, err)

	_, created, err := bd.Create(t.Context(), "run-1:1", store.NewItem{Title: "one"})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Len(t, fake.order, 2)
}

func TestCreate_RefusesAnAnswerWithNoId(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan("[]", "list")
	run.Plan("\n", "create")

	bd, err := beads.New(run, "p")
	require.NoError(t, err)

	_, _, err = bd.Create(t.Context(), "k", store.NewItem{Title: "t"})
	require.Error(t, err)
	assert.Equal(t, 2, codeOf(t, err))
}

func TestCreate_StopsWhenTheLookupFailsOrCreateFails(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan("[]", "list")
	run.PlanFail("boom", "create")

	bd, err := beads.New(run, "p")
	require.NoError(t, err)

	_, _, err = bd.Create(t.Context(), "k", store.NewItem{Title: "t"})
	require.Error(t, err)

	run = &proctest.Scripted{}
	run.PlanFail("boom", "list")

	bd, err = beads.New(run, "p")
	require.NoError(t, err)

	_, _, err = bd.Create(t.Context(), "k", store.NewItem{Title: "t"})
	require.Error(t, err)
	assert.False(t, run.Called("create"))
}

func TestSetStatus_RunsTheExactCommands(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		start  func(*fakeIssue)
		status store.Status
		want   [][]string
	}{
		"close an open issue": {func(*fakeIssue) {}, store.StatusClosed, [][]string{{"close", "p-1"}}},
		"hold an open issue":  {func(*fakeIssue) {}, store.StatusHold, [][]string{{"update", "p-1", "--add-label", "hold"}}},
		"hold a closed issue": {func(i *fakeIssue) { i.status = "closed" }, store.StatusHold, [][]string{
			{"reopen", "p-1"}, {"update", "p-1", "--add-label", "hold"},
		}},
		"open a held issue":    {func(i *fakeIssue) { i.labels = []string{"hold"} }, store.StatusOpen, [][]string{{"update", "p-1", "--remove-label", "hold"}}},
		"open a closed issue":  {func(i *fakeIssue) { i.status = "closed" }, store.StatusOpen, [][]string{{"reopen", "p-1"}}},
		"open an open issue":   {func(*fakeIssue) {}, store.StatusOpen, nil},
		"close a closed issue": {func(i *fakeIssue) { i.status = "closed" }, store.StatusClosed, nil},
	} {
		fake := newWriteBd()
		fake.order = []string{"p-1"}
		fake.issues["p-1"] = &fakeIssue{title: "t", status: "open"}
		tc.start(fake.issues["p-1"])

		require.NoError(t, writerOver(t, fake).SetStatus(t.Context(), "p-1", tc.status), name)
		assert.Equal(t, append([][]string{{"show", "p-1", "--json"}}, tc.want...), fake.calls, name)
	}
}

func TestSetStatus_ReportsEachFailedStep(t *testing.T) {
	t.Parallel()

	for _, failing := range []string{"show", "reopen", "update", "close"} {
		run := &proctest.Scripted{}
		run.Plan(`[{"id":"p-1","title":"t","status":"closed"}]`, "show")

		status := store.StatusHold
		if failing == "close" {
			run.Plan(`[{"id":"p-1","title":"t","status":"open"}]`, "show")

			status = store.StatusClosed
		}

		for _, command := range []string{"reopen", "update", "close"} {
			run.Plan("", command)
		}

		run.PlanFail("boom", failing)

		bd, err := beads.New(run, "p")
		require.NoError(t, err)
		require.Error(t, bd.SetStatus(t.Context(), "p-1", status), failing)
	}
}

func TestComment_RunsTheExactCommands(t *testing.T) {
	t.Parallel()

	fake := newWriteBd()
	fake.order = []string{"p-1"}
	fake.issues["p-1"] = &fakeIssue{title: "t", status: "open"}

	require.NoError(t, writerOver(t, fake).Comment(t.Context(), "p-1", "note", "the text"))
	assert.Equal(t, [][]string{
		{"comments", "p-1", "--json"},
		{"comments", "add", "p-1", "--", "the text\n\nobee-key: note"},
	}, fake.calls)
}

func TestComment_AnArgumentThatStartsWithADashIsTextNotAFlag(t *testing.T) {
	t.Parallel()

	fake := newWriteBd()
	fake.order = []string{"p-1"}
	fake.issues["p-1"] = &fakeIssue{title: "t", status: "open"}

	require.NoError(t, writerOver(t, fake).Comment(t.Context(), "p-1", "note", "- a bullet"))
	assert.Equal(t, [][]string{
		{"comments", "p-1", "--json"},
		{"comments", "add", "p-1", "--", "- a bullet\n\nobee-key: note"},
	}, fake.calls)
}

func TestComment_RefusesAnUnreadableListAndStopsOnAFailedAdd(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan("not json", "comments")

	bd, err := beads.New(run, "p")
	require.NoError(t, err)
	require.Error(t, bd.Comment(t.Context(), "p-1", "k", "t"))

	run = &proctest.Scripted{}
	run.Plan("[]", "comments", "p-1")
	run.PlanFail("boom", "comments", "add")

	bd, err = beads.New(run, "p")
	require.NoError(t, err)
	require.Error(t, bd.Comment(t.Context(), "p-1", "k", "t"))
}
