package remedy_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/recount"
	"github.com/tobocop2/beehave/examples/remediation/tools/remedy"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	roundBranch = "integration/s1"
	trunk       = "integration/r1"
)

// roundWorld is a forge for the integration subcommand. found is the answer to the lookup of the pull request from the round branch,
// merged is the list of pull requests that merged into the round branch, and view is the pull request that a given number reads.
// Every write is recorded: created holds the pull requests opened, and bodies holds each body written.
type roundWorld struct {
	forgeWorld
	found   []model.PullRequest
	merged  []model.PullRequest
	view    model.PullRequest
	nextPR  int
	listed  [][]string
	created []model.NewPull
	bodies  []string
	// failFind, failMerged and failCreate are the errors of the lookup, of the list of merged pull requests and of the open call. Nil means the call works.
	failFind, failMerged, failCreate error
}

func (w *roundWorld) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return w.view, nil
}

func (w *roundWorld) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	w.listed = append(w.listed, args)

	if slices.Contains(args, "merged") {
		return w.merged, w.failMerged
	}

	return w.found, w.failFind
}

func (w *roundWorld) CreatePR(_ context.Context, req model.NewPull) (int, error) {
	w.created = append(w.created, req)

	return w.nextPR, w.failCreate
}

func (w *roundWorld) SetBody(_ context.Context, _ int, body string) error {
	w.bodies = append(w.bodies, body)

	return nil
}

func roundDeps(world *roundWorld) remedy.Deps {
	return remedy.Deps{
		Git:   origin("git@github.com:owner/name.git", nothing),
		Dir:   ".",
		Forge: func(string) (remedy.Forge, error) { return world, nil },
	}
}

func state(s string) *string { return &s }

func mergedPull(number int, title string) model.PullRequest {
	return model.PullRequest{Number: number, Title: title, State: state("MERGED")}
}

func integrationArgs(pr string) []string {
	return []string{"integration", "--base", roundBranch, "--into", trunk, "--pr", pr}
}

func TestIntegration_FindsThePullRequestFromTheRoundBranchIntoTheTrunkAndPrintsItsLine(t *testing.T) {
	t.Parallel()

	world := &roundWorld{found: []model.PullRequest{{Number: 7, State: state("OPEN"), Body: "## Pull requests merged into this branch (0 pull requests)\n\n| PR | Change |\n|---|---|\n"}}}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=7 state=OPEN\n", out)
	assert.Empty(t, world.created)
	assert.Contains(t, strings.Join(world.listed[0], " "), "--head "+roundBranch)
	assert.Contains(t, strings.Join(world.listed[0], " "), "--base "+trunk)
}

func TestIntegration_UsesTheGivenNumberAndLooksForNoOther(t *testing.T) {
	t.Parallel()

	world := &roundWorld{view: model.PullRequest{Number: 9, State: state("MERGED"), Body: "x"}}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("9")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=9 state=MERGED\n", out)
	assert.Empty(t, world.created)
	assert.Empty(t, world.listed)
}

func TestIntegration_CreatesThePullRequestWhenNoneExistsWithTheMergedRowsInItsBody(t *testing.T) {
	t.Parallel()

	world := &roundWorld{nextPR: 21, merged: []model.PullRequest{mergedPull(12, "fix the queue"), mergedPull(13, "add the wait")}}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=21 state=OPEN\n", out)
	require.Len(t, world.created, 1)
	assert.Equal(t, model.NewPull{Head: roundBranch, Base: trunk, Title: world.created[0].Title, Body: world.created[0].Body, Label: config.DefaultPRLabel}, world.created[0])
	assert.Equal(t, recount.Tally{Rows: 2}, recount.Count(world.created[0].Body))
	assert.Empty(t, recount.Check(world.created[0].Title, world.created[0].Body))
	assert.Contains(t, world.created[0].Body, "| #12 | fix the queue |")
	assert.Equal(t, "Integration round s1", world.created[0].Title, "the title names the last part of the round branch")
}

func TestIntegration_TheOpenPullRequestWinsOverAMergedOneThatTheLookupListsFirst(t *testing.T) {
	t.Parallel()

	world := &roundWorld{found: []model.PullRequest{{Number: 6, State: state("MERGED"), Body: "old"}, {Number: 7, State: state("OPEN"), Body: "old"}}}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=7 state=OPEN\n", out)
}

func branchPull(number int, title, branch string) model.PullRequest {
	pull := mergedPull(number, title)
	pull.HeadRefName = &branch

	return pull
}

func syncPull(number int, trunkName string) model.PullRequest {
	return branchPull(number, "Merge "+trunkName+" into "+roundBranch, "sync/"+roundBranch+"/abcd1234")
}

func writtenBody(t *testing.T, world *roundWorld) string {
	t.Helper()

	require.Len(t, world.bodies, 1)

	return world.bodies[0]
}

func TestIntegration_ARoundWithSyncsOnlyNamesTheRoundAndListsTheSyncsApartFromTheWork(t *testing.T) {
	t.Parallel()

	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("OPEN"), Body: "\n\n"}},
		merged: []model.PullRequest{syncPull(734, trunk), syncPull(748, trunk)},
	}

	code, _, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)

	want := "Round s1 collects the item pull requests merged into integration/s1. The engine merges this pull request into integration/r1 when the round lands.\n\n" +
		"No item pull request has merged into this branch yet.\n\n" +
		"## Syncs with the trunk (2 pull requests)\n\n" +
		"#734, #748\n"
	assert.Equal(t, want, writtenBody(t, world))
	assert.Equal(t, recount.Tally{}, recount.Count(want), "a sync is no row of work")
}

func TestIntegration_ARoundWithItemsListsTheItemsAndTheirIssuesAndTheSyncsApart(t *testing.T) {
	t.Parallel()

	world := &roundWorld{
		found: []model.PullRequest{{Number: 7, State: state("OPEN"), Body: "old"}},
		merged: []model.PullRequest{
			branchPull(13, "add the | wait", "102"),
			syncPull(734, trunk),
			branchPull(12, "fix the queue", "101"),
			branchPull(14, "fix the queue again", "101"),
			branchPull(15, "a loose change", "fix-queue"),
		},
	}

	code, _, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)

	want := "Round s1 collects the item pull requests merged into integration/s1. The engine merges this pull request into integration/r1 when the round lands.\n\n" +
		"## Pull requests merged into this branch (4 pull requests)\n\n" +
		"| PR | Change |\n|---|---|\n" +
		"| #12 | fix the queue |\n| #13 | add the \\| wait |\n| #14 | fix the queue again |\n| #15 | a loose change |\n\n" +
		"## Syncs with the trunk (1 pull request)\n\n" +
		"#734\n\n" +
		"## Issues this batch closes\n\n" +
		"- Closes #101\n- Closes #102\n"
	body := writtenBody(t, world)
	assert.Equal(t, want, body)
	assert.Equal(t, recount.Tally{Rows: 4, Issues: 2}, recount.Count(body))
	assert.Empty(t, recount.Check("a person's title", body))
}

func TestIntegration_ASyncAfterABaseChangeIsListedAndTheStaleBodyIsRewrittenFromTheMergedPullRequests(t *testing.T) {
	t.Parallel()

	stale := "\n\n## Pull requests merged into this branch (5 pull requests)\n\n| PR | Change |\n|---|---|\n| #734 | Merge integration/r1 into integration/s1 |\n\nA note by hand.\n"
	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("OPEN"), Body: stale}},
		merged: []model.PullRequest{syncPull(734, "integration/r1"), syncPull(779, trunk)},
	}

	code, _, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)

	body := writtenBody(t, world)
	assert.Contains(t, body, "into integration/r1 when the round lands")
	assert.Contains(t, body, "#734, #779")
	assert.NotContains(t, body, "Merge integration", "a sync is a number, never a row titled with a trunk")
	assert.NotContains(t, body, "A note by hand", "the body is made again from the merged pull requests")
	assert.Equal(t, recount.Tally{}, recount.Count(body))
}

func TestIntegration_WritesNothingWhenTheBodyIsAlreadyTheOneTheMergedPullRequestsGive(t *testing.T) {
	t.Parallel()

	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("OPEN"), Body: "old"}},
		merged: []model.PullRequest{branchPull(12, "fix the queue", "101"), syncPull(734, trunk)},
	}

	code, _, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)

	world.found[0].Body = writtenBody(t, world)

	code, _, errOut = run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Len(t, world.bodies, 1, "a second run with no new merge writes nothing")
}

func TestIntegration_AFullListOfMergedPullRequestsIsARefusalAndWritesNothing(t *testing.T) {
	t.Parallel()

	world := &roundWorld{found: []model.PullRequest{{Number: 7, State: state("OPEN"), Body: "old"}}}
	for number := range 100 {
		world.merged = append(world.merged, mergedPull(number+1, "a change"))
	}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "remedy integration: the list of merged pull requests is full: "+roundBranch+" has 100 or more merged pull requests")
	assert.Empty(t, world.bodies, "a body from a part of the list is not written")
	assert.Empty(t, world.created)
}

func TestIntegration_ALandedPullRequestIsReportedAndItsBodyIsLeftAlone(t *testing.T) {
	t.Parallel()

	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("MERGED"), Body: "old"}},
		merged: []model.PullRequest{mergedPull(12, "fix the queue")},
	}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=7 state=MERGED\n", out)
	assert.Empty(t, world.bodies)
}

func TestIntegration_ABadArgumentIsARefusal(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"integration", "--into", trunk, "--pr", "0"},
		{"integration", "--base", roundBranch, "--pr", "0"},
		{"integration", "--base", roundBranch, "--into", trunk, "--pr", "-1"},
	} {
		code, out, errOut := run(t, roundDeps(&roundWorld{}), args...)

		assert.Equal(t, remedy.ExitRefused, code, args)
		assert.Empty(t, out)
		assert.Contains(t, errOut, "usage: remedy integration")
	}
}

func TestIntegration_AReadOrAWriteThatFailsIsARefusalAndPrintsNoLine(t *testing.T) {
	t.Parallel()

	for name, world := range map[string]*roundWorld{
		"the lookup fails":      {failFind: errDown},
		"the merged list fails": {failMerged: errDown},
		"the open call fails":   {failCreate: errDown},
	} {
		code, out, errOut := run(t, roundDeps(world), integrationArgs("0")...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Empty(t, out, name)
		assert.Contains(t, errOut, "remedy integration: down", name)
	}
}

func TestIntegration_AnOriginOrAForgeThatCannotBeOpenedIsARefusal(t *testing.T) {
	t.Parallel()

	noForge := roundDeps(&roundWorld{})
	noForge.Forge = func(string) (remedy.Forge, error) { return nil, errDown }

	for name, deps := range map[string]remedy.Deps{
		"no origin": {Git: origin("owner/name", nothing)},
		"no forge":  noForge,
	} {
		code, out, errOut := run(t, deps, integrationArgs("0")...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Empty(t, out, name)
		assert.Contains(t, errOut, "remedy integration:", name)
	}
}

func TestIntegration_AGivenPullRequestWithNoStateIsReportedAsUnknownAndItsBodyIsLeftAlone(t *testing.T) {
	t.Parallel()

	world := &roundWorld{view: model.PullRequest{Body: "x"}, merged: []model.PullRequest{mergedPull(12, "fix the queue")}}

	code, out, errOut := run(t, roundDeps(world), integrationArgs("9")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, "pr=9 state=UNKNOWN\n", out)
	assert.Empty(t, world.bodies)
}

func TestIntegration_AForgeThatCannotWriteIsARefusal(t *testing.T) {
	t.Parallel()

	code, out, errOut := run(t, cutDeps(&forgeWorld{}), integrationArgs("0")...)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "remedy integration:")
}

func TestIntegration_TheLabelFlagIsTheLabelOfTheOpenedPullRequest(t *testing.T) {
	t.Parallel()

	world := &roundWorld{nextPR: 21}

	code, _, errOut := run(t, roundDeps(world), append(integrationArgs("0"), "--label", "made-by-beehave")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	require.Len(t, world.created, 1)
	assert.Equal(t, "made-by-beehave", world.created[0].Label)
}

func TestIntegration_NoLabelFlagGivesTheDefaultLabel(t *testing.T) {
	t.Parallel()

	world := &roundWorld{nextPR: 21}

	code, _, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)
	require.Len(t, world.created, 1)
	assert.Equal(t, config.DefaultPRLabel, world.created[0].Label)
}

func TestIntegration_ABranchOfAnotherRoundWhoseNameStartsLikeThisOneIsARowAndNotASync(t *testing.T) {
	t.Parallel()

	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("OPEN"), Body: "old"}},
		merged: []model.PullRequest{branchPull(20, "a sync of round s10", "sync/integration/s10/abcd1234"), syncPull(734, trunk)},
	}

	code, _, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)

	body := writtenBody(t, world)
	assert.Contains(t, body, "| #20 | a sync of round s10 |", "the prefix of a round ends with a slash, so s10 is no sync of s1")
	assert.Contains(t, body, "## Syncs with the trunk (1 pull request)\n\n#734\n")
}

func TestIntegration_TheListOfMergedPullRequestsIsAskedForTheRoundBranch(t *testing.T) {
	t.Parallel()

	world := &roundWorld{found: []model.PullRequest{{Number: 7, State: state("OPEN"), Body: "old"}}}

	code, _, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)

	var asked []string

	for _, args := range world.listed {
		if slices.Contains(args, "merged") {
			asked = args
		}
	}

	require.NotEmpty(t, asked, "the merged list was read")
	at := slices.Index(asked, "--base")
	require.GreaterOrEqual(t, at, 0, "the merged list is filtered by a base")
	assert.Equal(t, roundBranch, asked[at+1], "the base is the round branch, so no other pull request of the repository is listed")
}

func TestIntegration_AHeadBranchThatOnlyEndsInDigitsIsARowAndNamesNoIssue(t *testing.T) {
	t.Parallel()

	world := &roundWorld{
		found:  []model.PullRequest{{Number: 7, State: state("OPEN"), Body: "old"}},
		merged: []model.PullRequest{branchPull(12, "fix the queue", "fix-101"), branchPull(13, "add the wait", "101-fix")},
	}

	code, _, errOut := run(t, roundDeps(world), integrationArgs("0")...)

	require.Equal(t, remedy.ExitPass, code, errOut)

	body := writtenBody(t, world)
	assert.Contains(t, body, "| #12 | fix the queue |")
	assert.Contains(t, body, "| #13 | add the wait |")
	assert.NotContains(t, body, "Closes", "only a branch of digits is the id of an issue")
}
