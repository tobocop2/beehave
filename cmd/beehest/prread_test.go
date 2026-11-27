package main

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/model"
)

// readForge is a forge that serves the typed read and the assignees of an issue. It lists pull requests by state, and a search finds the ones in found.
type readForge struct {
	pullForge

	found    []model.PullRequest
	readErr  error
	issues   map[string][]string
	issueErr error
	reads    []int
}

func (f *readForge) PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error) {
	if slices.Contains(args, "--search") {
		return f.found, nil
	}

	return f.pullForge.PRList(ctx, limit, args...)
}

func (f *readForge) PullRead(_ context.Context, pr int) (model.PullFacts, error) {
	f.reads = append(f.reads, pr)

	return model.PullFacts{Number: pr, State: f.state, Draft: true, Labels: []string{"hold"}, Assignees: []string{"maintainer"}}, f.readErr
}

// Association reads the owner as the author of every pull request.
func (*readForge) Association(context.Context, int) (model.Association, error) {
	return model.AssociationOwner, nil
}

func (f *readForge) IssueAssignees(_ context.Context, repo string, number int) ([]string, error) {
	return f.issues[repo+"#"+strconv.Itoa(number)], f.issueErr
}

func (f *readForge) over() pulls {
	return pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return f, nil }}
}

func TestPRRead_FindsThePullRequestOfAnItemInTheOrderOfThePreflight(t *testing.T) {
	t.Parallel()

	row := func(number int) []model.PullRequest { return []model.PullRequest{{Number: number}} }

	for name, tc := range map[string]struct {
		listed map[string][]model.PullRequest
		found  []model.PullRequest
		want   int
	}{
		"the open one on the branch comes before every other": {listed: map[string][]model.PullRequest{"open": row(8), "merged": row(5), "closed": row(4)}, found: []model.PullRequest{{Number: 31, Body: "Closes #7"}}, want: 8},
		"an open one that names the item comes next":          {listed: map[string][]model.PullRequest{"merged": row(5), "closed": row(4)}, found: []model.PullRequest{{Number: 31, Body: "Closes #7"}}, want: 31},
		"the merged one comes before the closed one":          {listed: map[string][]model.PullRequest{"merged": row(5), "closed": row(4)}, want: 5},
		"the newest closed one comes last":                    {listed: map[string][]model.PullRequest{"closed": {{Number: 9}, {Number: 4}}}, want: 9},
		"an item with none finds none":                        {want: 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := &readForge{pullForge: pullForge{listed: tc.listed, state: "OPEN"}, found: tc.found}

			facts, err := f.over().ReadPull(t.Context(), "p", "7", true)

			require.NoError(t, err)
			assert.Equal(t, tc.want, facts.Number)

			if tc.want == 0 {
				assert.Empty(t, f.reads, "an item with no pull request reads nothing more")
			} else {
				assert.Equal(t, []int{tc.want}, f.reads)
				assert.True(t, facts.Draft, "the facts of the typed read come through")
			}
		})
	}
}

func TestPRRead_FailsWhenAReadFailsOrTheProjectIsUnknown(t *testing.T) {
	t.Parallel()

	listed := map[string][]model.PullRequest{"open": {{Number: 4}}}

	f := &readForge{pullForge: pullForge{listed: listed}, readErr: errFake}
	_, err := f.over().ReadPull(t.Context(), "p", "7", true)
	require.ErrorIs(t, err, errFake, "a failed typed read is an error and never a pull request that is not there")

	f = &readForge{pullForge: pullForge{listed: listed, listErr: errFake}}
	_, err = f.over().ReadPull(t.Context(), "p", "7", true)
	require.ErrorIs(t, err, errFake, "a failed list is an error")

	_, err = (&readForge{}).over().ReadPull(t.Context(), "zzz", "7", true)
	require.ErrorIs(t, err, errUnknownProject)
}

func TestPRRead_AReadWithNoAssigneeListOrABadLinkGivesFactsThatNameWhatIsUnread(t *testing.T) {
	t.Parallel()

	listed := map[string][]model.PullRequest{"open": {{Number: 4}}}
	facts := model.PullFacts{Number: 4, State: "OPEN", Author: "dev"}

	for name, tc := range map[string]struct {
		err  error
		want []string
	}{
		"no assignee list": {&model.IncompletePull{Facts: facts, Missing: []string{"assignees"}}, []string{"the read has no assignees"}},
		"a bad link":       {&model.LinkError{Facts: facts, Cause: errFake}, []string{errFake.Error()}},
	} {
		f := &readForge{pullForge: pullForge{listed: listed}, readErr: tc.err}

		got, err := f.over().ReadPull(t.Context(), "p", "7", true)

		require.NoError(t, err, name)
		assert.Equal(t, tc.want, got.Unread, name)
		assert.Equal(t, "dev", got.Author, name)
	}

	for name, err := range map[string]error{
		"no label list":                  &model.IncompletePull{Facts: facts, Missing: []string{"labels"}},
		"no draft flag":                  &model.IncompletePull{Facts: facts, Missing: []string{"draft"}},
		"no assignee list and no head":   &model.IncompletePull{Facts: facts, Missing: []string{"head", "assignees"}},
		"no assignee list and no labels": &model.IncompletePull{Facts: facts, Missing: []string{"labels", "assignees"}},
	} {
		f := &readForge{pullForge: pullForge{listed: listed}, readErr: err}

		_, got := f.over().ReadPull(t.Context(), "p", "7", true)

		require.Error(t, got, name+" stays an error")
	}
}

func TestIssueAssignees_ReadsALinkedIssueOnItsRepository(t *testing.T) {
	t.Parallel()

	f := &readForge{issues: map[string][]string{"#7": {"maintainer"}, "acme/tools#7": {"other"}}}

	logins, err := f.over().IssueAssignees(t.Context(), "p", "", 7)

	require.NoError(t, err)
	assert.Equal(t, []string{"maintainer"}, logins)

	logins, err = f.over().IssueAssignees(t.Context(), "p", "acme/tools", 7)

	require.NoError(t, err)
	assert.Equal(t, []string{"other"}, logins, "a link to another repository is read on that repository")

	f.issueErr = errFake
	_, err = f.over().IssueAssignees(t.Context(), "p", "", 7)
	require.ErrorIs(t, err, errFake)

	_, err = f.over().IssueAssignees(t.Context(), "zzz", "", 7)
	require.ErrorIs(t, err, errUnknownProject)

	_, err = (&preflightForge{}).over().IssueAssignees(t.Context(), "p", "", 7)
	require.ErrorIs(t, err, errNoOptionalRead, "a forge that cannot read assignees is an error and never an issue that nobody holds")
}
