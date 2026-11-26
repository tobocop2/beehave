package github_test

import (
	"context"
	"reflect"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/ghcontract"
	"github.com/tobocop2/beehave/internal/model"
)

const shapeSHA = "0123456789abcdef0123456789abcdef01234567"

const shapeJob = "https://github.com/owner/name/job/12"

// shapeGh answers every call with an empty answer of the kind the command gives, and records each call. It refuses nothing itself: the test reads the record.
type shapeGh struct {
	mu    sync.Mutex
	calls [][]string
}

func (g *shapeGh) Run(_ context.Context, args []string) model.CommandResult {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.calls = append(g.calls, slices.Clone(args))

	switch {
	case slices.Contains(args, "--slurp"):
		return model.CommandResult{Stdout: "[[]]"}
	case slices.Contains(args, "list") || slices.Contains(args, "checks"):
		return model.CommandResult{Stdout: "[]"}
	}

	return model.CommandResult{Stdout: "{}"}
}

type shapeDriver func(context.Context, *github.Forge)

// shapeDrivers calls every public method of the forge with arguments that pass its own checks. A method that needs no gh call is listed in shapeNoCall.
func shapeDrivers() map[string]shapeDriver {
	card := model.Card{Board: 3, Number: 7, Pull: true, Status: "Todo"}
	search := model.SearchRequest{Kind: model.SearchPulls, Involves: "me", Since: "2026-01-01", Limit: 5}
	pull := model.NewPull{Head: "topic", Base: "main", Title: "title", Body: "body", Label: "label"}
	overlap := model.OverlapRequest{PR: "7", Files: "a.go", Since: "2026-01-01", Base: "main", Integration: "integration", Now: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
	status := model.CommitStatus{SHA: shapeSHA, Context: "ci", State: model.StatusSuccess, Description: "done"}

	d := map[string]shapeDriver{}

	d["PlaceCard"] = func(c context.Context, f *github.Forge) { _ = f.PlaceCard(c, card) }
	d["CardStatus"] = func(c context.Context, f *github.Forge) { _, _ = f.CardStatus(c, card) }
	d["CommitTree"] = func(c context.Context, f *github.Forge) { _, _ = f.CommitTree(c, shapeSHA) }
	d["Compare"] = func(c context.Context, f *github.Forge) { _, _ = f.Compare(c, shapeSHA, "main") }
	d["TreeBlobs"] = func(c context.Context, f *github.Forge) { _, _ = f.TreeBlobs(c, shapeSHA) }
	d["FailedLog"] = func(c context.Context, f *github.Forge) { _, _ = f.FailedLog(c, shapeJob) }
	d["ExistingReview"] = func(c context.Context, f *github.Forge) { _, _ = f.ExistingReview(c, 7) }
	d["Reviews"] = func(c context.Context, f *github.Forge) { _, _ = f.Reviews(c, 7) }
	d["PRBody"] = func(c context.Context, f *github.Forge) { _, _ = f.PRBody(c, 7) }
	d["PRView"] = func(c context.Context, f *github.Forge) { _, _ = f.PRView(c, 7, "title", "body") }
	d["PullRead"] = func(c context.Context, f *github.Forge) { _, _ = f.PullRead(c, 7) }
	d["OpenPulls"] = func(c context.Context, f *github.Forge) { _, _ = f.OpenPulls(c, "main") }
	d["Item"] = func(c context.Context, f *github.Forge) { _, _ = f.Item(c, 7) }
	d["PullCounts"] = func(c context.Context, f *github.Forge) { _, _, _ = f.PullCounts(c, 7) }
	d["Committers"] = func(c context.Context, f *github.Forge) { _, _ = f.Committers(c, 7) }
	d["ReadyMarks"] = func(c context.Context, f *github.Forge) { _, _ = f.ReadyMarks(c, 7) }
	d["PRList"] = func(c context.Context, f *github.Forge) { _, _ = f.PRList(c, 5, "--state", "open", "--json", "number,title") }
	d["PRChecks"] = func(c context.Context, f *github.Forge) { _, _ = f.PRChecks(c, 7) }
	d["OpenChangeRequests"] = func(c context.Context, f *github.Forge) { _, _ = f.OpenChangeRequests(c, 7) }
	d["UnresolvedThreads"] = func(c context.Context, f *github.Forge) { _, _ = f.UnresolvedThreads(c, 7) }
	d["ClosingRefs"] = func(c context.Context, f *github.Forge) { _, _ = f.ClosingRefs(c, 7) }
	d["StackID"] = func(c context.Context, f *github.Forge) { _, _ = f.StackID(c, 7) }
	d["Association"] = func(c context.Context, f *github.Forge) { _, _ = f.Association(c, 7) }
	d["Comments"] = func(c context.Context, f *github.Forge) { _, _ = f.Comments(c, 7) }
	d["ReviewComments"] = func(c context.Context, f *github.Forge) { _, _ = f.ReviewComments(c, 7) }
	d["Search"] = func(c context.Context, f *github.Forge) { _, _ = f.Search(c, search) }
	d["DefaultBranch"] = func(c context.Context, f *github.Forge) { _, _ = f.DefaultBranch(c) }
	d["BranchRef"] = func(c context.Context, f *github.Forge) { _, _, _ = f.BranchRef(c, "main") }
	d["PostComment"] = func(c context.Context, f *github.Forge) { _ = f.PostComment(c, 7, "text") }
	d["ClosePR"] = func(c context.Context, f *github.Forge) { _ = f.ClosePR(c, 7, "text") }
	d["SetDraft"] = func(c context.Context, f *github.Forge) { _ = f.SetDraft(c, 7, false) }
	d["CreatePR"] = func(c context.Context, f *github.Forge) { _, _ = f.CreatePR(c, pull) }
	d["LinkStack"] = func(c context.Context, f *github.Forge) { _ = f.LinkStack(c, 7, 8, "main") }
	d["Unstack"] = func(c context.Context, f *github.Forge) { _ = f.Unstack(c, 7) }
	d["Overlap"] = func(c context.Context, f *github.Forge) { _, _ = f.Overlap(c, overlap) }
	d["OwnPulls"] = func(c context.Context, f *github.Forge) { _, _ = f.OwnPulls(c, "me") }
	d["Title"] = func(c context.Context, f *github.Forge) { _, _ = f.Title(c, 7) }
	d["IssueState"] = func(c context.Context, f *github.Forge) { _, _ = f.IssueState(c, 7) }
	d["IssueAssignees"] = func(c context.Context, f *github.Forge) { _, _ = f.IssueAssignees(c, repo, 7) }
	d["CheckNotStarted"] = func(c context.Context, f *github.Forge) { _, _ = f.CheckNotStarted(c, shapeJob) }
	d["PostStatus"] = func(c context.Context, f *github.Forge) { _ = f.PostStatus(c, status) }
	d["Protection"] = func(c context.Context, f *github.Forge) { _, _ = f.Protection(c, "main") }
	d["CommitsAhead"] = func(c context.Context, f *github.Forge) { _, _ = f.CommitsAhead(c, "main", "topic") }
	d["MergeBranch"] = func(c context.Context, f *github.Forge) { _, _ = f.MergeBranch(c, "main", "topic") }
	d["MergePR"] = func(c context.Context, f *github.Forge) { _ = f.MergePR(c, 7, shapeSHA) }
	d["AutoMergePR"] = func(c context.Context, f *github.Forge) { _ = f.AutoMergePR(c, 7, shapeSHA) }
	d["DisableAutoMergePR"] = func(c context.Context, f *github.Forge) { _ = f.DisableAutoMergePR(c, 7) }
	d["CanWriteCI"] = func(c context.Context, f *github.Forge) { _, _ = f.CanWriteCI(c) }
	d["Login"] = func(c context.Context, f *github.Forge) { _, _ = f.Login(c) }
	d["CreateBranch"] = func(c context.Context, f *github.Forge) { _ = f.CreateBranch(c, "topic", shapeSHA) }
	d["SetBase"] = func(c context.Context, f *github.Forge) { _ = f.SetBase(c, 7, "main") }
	d["SetBody"] = func(c context.Context, f *github.Forge) { _ = f.SetBody(c, 7, "text") }
	d["CloneURL"] = func(_ context.Context, f *github.Forge) { _ = f.CloneURL() }
	d["ItemFields"] = func(_ context.Context, f *github.Forge) { _ = f.ItemFields() }

	return d
}

// shapeNoCall are the methods that answer without a gh call.
var shapeNoCall = []string{"CloneURL", "ItemFields"}

func TestForge_EveryCallShapeIsOneGhAccepts(t *testing.T) {
	t.Parallel()

	drivers := shapeDrivers()

	var methods, driven []string

	kind := reflect.TypeFor[*github.Forge]()
	for i := range kind.NumMethod() {
		methods = append(methods, kind.Method(i).Name)
	}

	for name := range drivers {
		driven = append(driven, name)
	}

	sort.Strings(driven)
	require.Equal(t, methods, driven, "every public method of the forge needs a driver here, so a new method cannot skip the contract")

	for name, drive := range drivers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			run := &shapeGh{}
			f, err := github.New(repo, run)
			require.NoError(t, err)

			drive(t.Context(), f)

			if slices.Contains(shapeNoCall, name) {
				assert.Empty(t, run.calls)

				return
			}

			require.NotEmpty(t, run.calls, "%s made no gh call: its arguments do not pass its own checks", name)

			for _, call := range run.calls {
				stderr, ok := ghcontract.Check(call)

				assert.True(t, ok, "%s sent gh %v: %s", name, call, stderr)
			}
		})
	}
}
