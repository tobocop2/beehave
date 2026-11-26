package main

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
	"github.com/tobocop2/beehave/internal/store/beads"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
	"github.com/tobocop2/beehave/internal/store/local"
)

// authorForge is a forge that reads who wrote each issue and pull request, and fails the read of the number failing.
type authorForge struct {
	readForge

	authors map[int]model.Association
	failing int
	asked   []int
}

func (f *authorForge) Association(_ context.Context, number int) (model.Association, error) {
	f.asked = append(f.asked, number)
	if number == f.failing {
		return "", errFake
	}

	return f.authors[number], nil
}

// on is the pulls of project p with the tracker, whose forge and the GitHub access of whose tracker are both f.
func (f *authorForge) on(tracker model.TrackerName) pulls {
	project := config.Project{Project: model.Project{Name: "p", Tracker: tracker}}
	reads := func(model.Project) (forge.Forge, error) { return f, nil }

	return pulls{projects: map[string]config.Project{"p": project}, forge: reads, issues: reads}
}

func TestIssueAuthor_ReadsWhoWroteTheIssueOfAGitHubItem(t *testing.T) {
	t.Parallel()

	f := &authorForge{authors: map[int]model.Association{7: model.AssociationContributor}}

	issue, err := f.on(storegithub.Tracker).IssueAuthor(t.Context(), "p", "7")

	require.NoError(t, err)
	assert.Equal(t, model.Authored{Number: 7, Association: model.AssociationContributor}, issue)
	assert.Equal(t, []int{7}, f.asked, "the issue is read once, and no pull request is looked up")
}

func TestIssueAuthor_AGitHubTrackerWithNoForgeKeyReadsTheIssueThroughItsOwnAccess(t *testing.T) {
	t.Parallel()

	f := &authorForge{authors: map[int]model.Association{7: model.AssociationNone}}
	over := pulls{
		projects: map[string]config.Project{"p": {Project: model.Project{Name: "p", Tracker: storegithub.Tracker}}},
		forge:    func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge },
		issues:   func(model.Project) (forge.Forge, error) { return f, nil },
	}

	issue, err := over.IssueAuthor(t.Context(), "p", "7")

	require.NoError(t, err)
	assert.Equal(t, model.Authored{Number: 7, Association: model.AssociationNone}, issue, "a project with no forge key still reads who wrote its issue")
}

func TestIssueAuthor_AnItemOfAnotherTrackerReadsNoIssue(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		tracker model.TrackerName
		item    string
	}{
		"an item of beads":                   {beads.Tracker, "7"},
		"a numbered item of the local store": {local.Tracker, "7"},
		"a GitHub item that is not a number": {storegithub.Tracker, "bb-7"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := &authorForge{authors: map[int]model.Association{7: model.AssociationNone}}

			issue, err := f.on(test.tracker).IssueAuthor(t.Context(), "p", test.item)

			require.NoError(t, err)
			assert.Zero(t, issue)
			assert.Empty(t, f.asked)
		})
	}
}

func TestSameItem_TwoIdsNameOneItemOnlyAsTheTrackerKeysThem(t *testing.T) {
	t.Parallel()

	tooBig := "99999999999999999999"

	for name, test := range map[string]struct {
		tracker model.TrackerName
		a, b    string
		same    bool
	}{
		"the same text in the local store":               {local.Tracker, "fix-login", "fix-login", true},
		"two numbers of one value in the local store":    {local.Tracker, "0412", "412", false},
		"two numbers of one value in beads":              {beads.Tracker, "0412", "412", false},
		"two numbers of one issue on GitHub":             {storegithub.Tracker, "0412", "412", true},
		"two numbers of two issues on GitHub":            {storegithub.Tracker, "412", "413", false},
		"a number and an id that is no number on GitHub": {storegithub.Tracker, "412", "bb-412", false},
		"two ids that are no numbers on GitHub":          {storegithub.Tracker, "bb-7", "BB-7", false},
		"a number past the range of an issue on GitHub":  {storegithub.Tracker, "0" + tooBig, tooBig, false},
		"the same number past the range of an issue":     {storegithub.Tracker, tooBig, tooBig, true},
		"a number with a mark on GitHub":                 {storegithub.Tracker, "#412", "412", false},
		"a signed number on GitHub":                      {storegithub.Tracker, "+412", "412", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			over := (&authorForge{}).on(test.tracker)

			for _, pair := range [][2]string{{test.a, test.b}, {test.b, test.a}} {
				same, err := over.SameItem("p", pair[0], pair[1])

				require.NoError(t, err)
				assert.Equal(t, test.same, same, "%s and %s", pair[0], pair[1])
			}
		})
	}
}

func TestSameItem_AnUnknownProjectIsAnError(t *testing.T) {
	t.Parallel()

	_, err := (&authorForge{}).on(storegithub.Tracker).SameItem("zzz", "0412", "412")

	require.ErrorIs(t, err, errUnknownProject)
}

func TestIssueAuthor_AFailedReadIsAnError(t *testing.T) {
	t.Parallel()

	github := map[string]config.Project{"p": {Project: model.Project{Name: "p", Tracker: storegithub.Tracker}}}

	for name, test := range map[string]struct {
		over    pulls
		project string
		want    error
	}{
		"an unknown project":                  {over: (&authorForge{}).on(storegithub.Tracker), project: "zzz", want: errUnknownProject},
		"the read of the issue":               {over: (&authorForge{failing: 7}).on(storegithub.Tracker), project: "p", want: errFake},
		"a forge that cannot read it":         {over: pulls{projects: github, issues: func(model.Project) (forge.Forge, error) { return &pullForge{}, nil }}, project: "p", want: forge.ErrNoCapability},
		"a build with no access to the issue": {over: pulls{projects: github}, project: "p", want: errNoIssueReads},
		"an access that cannot be built":      {over: pulls{projects: github, issues: func(model.Project) (forge.Forge, error) { return nil, errFake }}, project: "p", want: errFake},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := test.over.IssueAuthor(t.Context(), test.project, "7")

			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestIssueAuthor_AGitHubItemTooLongForAnIssueNumberIsAnError(t *testing.T) {
	t.Parallel()

	f := &authorForge{}

	_, err := f.on(storegithub.Tracker).IssueAuthor(t.Context(), "p", "99999999999999999999999")

	require.ErrorContains(t, err, "item 99999999999999999999999 is not the number of an issue")
	assert.Empty(t, f.asked)
}

// turningForge is a forge whose open list finds another pull request on each call: the number at the index of the call, and the last one repeats.
// It reads who wrote each one and its facts, so a test can see that the author and the facts name the same pull request.
type turningForge struct {
	preflightForge

	opens   []int
	authors map[int]model.Association
	calls   int
	asked   []int
}

func (f *turningForge) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	if slices.Contains(args, "--search") || !slices.Contains(args, "open") {
		return nil, nil
	}

	f.calls++

	return []model.PullRequest{{Number: f.opens[min(f.calls, len(f.opens))-1]}}, nil
}

func (f *turningForge) Association(_ context.Context, number int) (model.Association, error) {
	f.asked = append(f.asked, number)

	return f.authors[number], nil
}

func (f *turningForge) PullRead(_ context.Context, pr int) (model.PullFacts, error) {
	f.reads = append(f.reads, pr)

	return model.PullFacts{Number: pr, State: "OPEN"}, nil
}

func (f *turningForge) over() pulls {
	return pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return f, nil }}
}

func TestReadPull_TheAuthorIsOfThePullRequestThatTheSameLookupFound(t *testing.T) {
	t.Parallel()

	f := &turningForge{opens: []int{31, 40}, authors: map[int]model.Association{31: model.AssociationOwner, 40: model.AssociationContributor}}

	first, err := f.over().ReadPull(t.Context(), "p", "7", true)
	require.NoError(t, err)

	second, err := f.over().ReadPull(t.Context(), "p", "7", true)
	require.NoError(t, err)

	assert.Equal(t, model.Authored{Number: 31, Association: model.AssociationOwner}, first.Authored())
	assert.Equal(t, model.Authored{Number: 40, Association: model.AssociationContributor}, second.Authored(), "a lookup that finds another pull request carries the author of that one")
	assert.Equal(t, []int{31, 40}, f.asked)
	assert.Equal(t, f.reads, f.asked, "the author and the facts are read on the same number")
	assert.Equal(t, 2, f.calls, "each read looks the pull request up once")
}

func TestPreflight_TheResultNamesThePullRequestThatItJudgedWithItsAuthor(t *testing.T) {
	t.Parallel()

	f := &turningForge{opens: []int{31, 40}, authors: map[int]model.Association{31: model.AssociationOwner, 40: model.AssociationContributor}}
	f.state = "OPEN"

	first, err := f.over().Preflight(t.Context(), "p", "bb-1", preflight.Request{}, true)
	require.NoError(t, err)

	second, err := f.over().Preflight(t.Context(), "p", "bb-1", preflight.Request{}, true)
	require.NoError(t, err)

	assert.Equal(t, model.Authored{Number: 31, Association: model.AssociationOwner}, first.Pull)
	assert.Equal(t, model.Authored{Number: 40, Association: model.AssociationContributor}, second.Pull)
	assert.Equal(t, f.reads, f.asked, "the author and the judged pull request are read on the same number")
	assert.Equal(t, 2, f.calls, "each preflight looks the pull request up once")
}

func TestReadPullAndPreflight_AFailedReadOfTheAuthorIsAnError(t *testing.T) {
	t.Parallel()

	f := &authorForge{readForge: readForge{pullForge: pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 31}}}, state: "OPEN"}}, failing: 31}

	_, err := f.on(beads.Tracker).ReadPull(t.Context(), "p", "7", true)
	require.ErrorIs(t, err, errFake)
	assert.Empty(t, f.reads, "no facts are read for a pull request whose author is unknown")

	g := &preflightForge{pullForge: pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 31}}}, state: "OPEN"}, failing: 31}

	_, err = g.over().Preflight(t.Context(), "p", "bb-1", preflight.Request{}, true)
	require.ErrorIs(t, err, errFake)
	assert.Empty(t, g.reads, "no pull request is judged whose author is unknown")
}

func TestReadPullAndPreflight_ACallerThatAsksForNoAuthorMakesNoAuthorReadAndCannotFailOnOne(t *testing.T) {
	t.Parallel()

	f := &authorForge{readForge: readForge{pullForge: pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 88}}}, state: "OPEN"}}, failing: 88}

	facts, err := f.on(beads.Tracker).ReadPull(t.Context(), "p", "other", false)
	require.NoError(t, err, "the read of the author of #88 would fail, and it is not made")
	assert.Equal(t, model.Authored{Number: 88}, facts.Authored(), "the facts name the pull request and no author")
	assert.Equal(t, []int{88}, f.reads, "the facts are read on the number of the one lookup")
	assert.Empty(t, f.asked, "no author is read")

	g := &turningForge{opens: []int{88}, authors: map[int]model.Association{88: model.AssociationContributor}}
	g.state = "OPEN"

	result, err := g.over().Preflight(t.Context(), "p", "other", preflight.Request{}, false)
	require.NoError(t, err)
	assert.Equal(t, model.Authored{Number: 88}, result.Pull, "the result names the pull request that it judged and no author")
	assert.Equal(t, []int{88}, g.reads)
	assert.Empty(t, g.asked, "no author is read")
	assert.Equal(t, 1, g.calls, "the preflight looks the pull request up once")
}

func TestNoneHostsOf_PassesOnlyAProjectThatDeclaresItsHostWithNoCredentials(t *testing.T) {
	t.Parallel()

	declared := func(credentials model.HostCredentials) map[string]config.Project {
		return map[string]config.Project{"p": {Project: projectOnTwoClusters(credentials)}}
	}

	for name, test := range map[string]struct {
		projects map[string]config.Project
		want     error
	}{
		"an unknown project":           {projects: map[string]config.Project{}, want: errUnknownProject},
		"a project with no host block": {projects: map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}, want: errNoNoneHost},
		"a host with no key":           {projects: declared(""), want: errNoNoneHost},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var checked []string

			check := noneHostsOf(config.Config{Projects: test.projects}, tableOfCheckedHosts("", &checked))

			require.ErrorIs(t, check(t.Context(), "p"), test.want)
			assert.Empty(t, checked, "the refusal runs no credential check")
		})
	}

	var checked []string

	require.NoError(t, noneHostsOf(config.Config{Projects: declared(model.HostCredentialsNone)}, tableOfCheckedHosts("", &checked))(t.Context(), "p"))
	assert.Equal(t, []string{"gpu", "gpu-b"}, checked, "a declared host passes the check of every cluster")

	checked = nil
	err := noneHostsOf(config.Config{Projects: declared(model.HostCredentialsNone)}, tableOfCheckedHosts("gpu-b", &checked))(t.Context(), "p")
	require.ErrorContains(t, err, "the build host gpu-b gets the SSH agent")
}

func TestHostStop_CheckCredentialsAsksTheCheckOfTheBuild(t *testing.T) {
	t.Parallel()

	var asked string

	stop := hostStop{credentials: func(_ context.Context, project string) error {
		asked = project

		return errFake
	}}

	require.ErrorIs(t, stop.CheckCredentials(t.Context(), "p"), errFake)
	assert.Equal(t, "p", asked)
}
