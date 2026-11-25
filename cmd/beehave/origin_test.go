package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store/beads"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
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

func (f *authorForge) on(tracker model.TrackerName) pulls {
	project := config.Project{Project: model.Project{Name: "p", Tracker: tracker}}

	return pulls{projects: map[string]config.Project{"p": project}, forge: func(model.Project) (forge.Forge, error) { return f, nil }}
}

func TestOrigin_ReadsWhoWroteTheIssueOfAGitHubItemAndItsPullRequest(t *testing.T) {
	t.Parallel()

	f := &authorForge{readForge: readForge{pullForge: pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 31}}}}}, authors: map[int]model.Association{7: model.AssociationOwner, 31: model.AssociationContributor}}

	origin, err := f.on(storegithub.Tracker).Origin(t.Context(), "p", "7")

	require.NoError(t, err)
	assert.Equal(t, model.Origin{Issue: model.Authored{Number: 7, Association: model.AssociationOwner}, Pull: model.Authored{Number: 31, Association: model.AssociationContributor}}, origin)
	assert.Equal(t, []int{7, 31}, f.asked)
}

func TestOrigin_ReadsTheAuthorOfThePullRequestThatReadPullFinds(t *testing.T) {
	t.Parallel()

	row := func(number int) []model.PullRequest { return []model.PullRequest{{Number: number}} }

	for name, test := range map[string]struct {
		listed map[string][]model.PullRequest
		found  []model.PullRequest
	}{
		"an open one on the branch": {listed: map[string][]model.PullRequest{"open": row(8), "merged": row(5)}, found: []model.PullRequest{{Number: 31, Body: "Closes #7"}}},
		"an open one that names it": {listed: map[string][]model.PullRequest{"merged": row(5), "closed": row(4)}, found: []model.PullRequest{{Number: 31, Body: "Closes #7"}}},
		"a merged one":              {listed: map[string][]model.PullRequest{"merged": row(5), "closed": row(4)}},
		"the newest closed one":     {listed: map[string][]model.PullRequest{"closed": {{Number: 9}, {Number: 4}}}},
		"none":                      {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := &authorForge{readForge: readForge{pullForge: pullForge{listed: test.listed, state: "OPEN"}, found: test.found}, authors: map[int]model.Association{}}

			facts, err := f.on(beads.Tracker).ReadPull(t.Context(), "p", "7")
			require.NoError(t, err)

			origin, err := f.on(beads.Tracker).Origin(t.Context(), "p", "7")
			require.NoError(t, err)

			assert.Equal(t, facts.Number, origin.Pull.Number, "the rule reads the author of the pull request that pr_read gives the workflow")
		})
	}
}

func TestOrigin_AnItemOfAnotherTrackerReadsNoIssue(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		tracker model.TrackerName
		item    string
	}{
		"an item of beads":                   {beads.Tracker, "7"},
		"a GitHub item that is not a number": {storegithub.Tracker, "bb-7"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := &authorForge{readForge: readForge{pullForge: pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 31}}}}}, authors: map[int]model.Association{31: model.AssociationNone}}

			origin, err := f.on(test.tracker).Origin(t.Context(), "p", test.item)

			require.NoError(t, err)
			assert.Equal(t, model.Origin{Pull: model.Authored{Number: 31, Association: model.AssociationNone}}, origin)
			assert.Equal(t, []int{31}, f.asked, "only the pull request has an author to read")
		})
	}
}

func TestOrigin_AnItemWithNoPullRequestReadsOnlyItsIssue(t *testing.T) {
	t.Parallel()

	f := &authorForge{authors: map[int]model.Association{7: model.AssociationFirstTimer}}

	origin, err := f.on(storegithub.Tracker).Origin(t.Context(), "p", "7")

	require.NoError(t, err)
	assert.Equal(t, model.Origin{Issue: model.Authored{Number: 7, Association: model.AssociationFirstTimer}}, origin)
}

func TestOrigin_AFailedReadIsAnError(t *testing.T) {
	t.Parallel()

	listed := map[string][]model.PullRequest{"open": {{Number: 31}}}

	for name, test := range map[string]struct {
		over    pulls
		project string
		want    error
	}{
		"an unknown project":             {over: (&authorForge{}).on(storegithub.Tracker), project: "zzz", want: errUnknownProject},
		"the read of the issue":          {over: (&authorForge{failing: 7}).on(storegithub.Tracker), project: "p", want: errFake},
		"the lookup of the pull request": {over: (&authorForge{readForge: readForge{pullForge: pullForge{listErr: errFake}}}).on(storegithub.Tracker), project: "p", want: errFake},
		"the read of the pull request":   {over: (&authorForge{readForge: readForge{pullForge: pullForge{listed: listed}}, failing: 31}).on(storegithub.Tracker), project: "p", want: errFake},
		"a forge that cannot read it":    {over: (&readForge{pullForge: pullForge{listed: listed}}).over(), project: "p", want: errNoOptionalRead},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := test.over.Origin(t.Context(), test.project, "7")

			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestOrigin_AGitHubItemTooLongForAnIssueNumberIsAnError(t *testing.T) {
	t.Parallel()

	f := &authorForge{}

	_, err := f.on(storegithub.Tracker).Origin(t.Context(), "p", "99999999999999999999999")

	require.ErrorContains(t, err, "item 99999999999999999999999 is not the number of an issue")
	assert.Empty(t, f.asked)
}

func TestOrigin_AProjectWhoseForgeCannotBeBuiltIsAnError(t *testing.T) {
	t.Parallel()

	over := pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }}

	_, err := over.Origin(t.Context(), "p", "7")

	require.ErrorIs(t, err, forge.ErrNoForge)
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
