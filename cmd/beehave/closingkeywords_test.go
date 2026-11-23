package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

// keywordForge reads the closing references through the GitHub adapter over a scripted gh and answers every other call as the pull fake does.
type keywordForge struct {
	*pullForge

	github *forgegithub.Forge
}

func (k keywordForge) ClosingRefs(ctx context.Context, pr int) (model.ClosingRefs, error) {
	return k.github.ClosingRefs(ctx, pr)
}

// intoIntegration is the gh answer for a pull request into integration/r2 with the given body: GitHub lists no closing issue for it.
func intoIntegration(body string) string {
	return `{"data":{"repository":{"defaultBranchRef":{"name":"main"},"pullRequest":{"baseRefName":"integration/r2",` +
		`"baseRepository":{"nameWithOwner":"owner/name"},"body":"` + body + `","commits":{"totalCount":1,"nodes":[{"commit":{"message":"add a"}}]},` +
		`"closingIssuesReferences":{"totalCount":0,"nodes":[]}}}}}`
}

func TestPulls_ClosingIssuesOfAPullRequestIntoAnIntegrationBranchGrowWhenTheBodyGainsAKeyword(t *testing.T) {
	t.Parallel()

	read := func(body string) []int {
		gh, err := forgegithub.New("owner/name", proctest.Always(model.CommandResult{Stdout: intoIntegration(body)}))
		require.NoError(t, err)

		built := keywordForge{pullForge: &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 7}}}}, github: gh}
		p := pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return built, nil }}

		issues, found, err := p.ClosingIssues(t.Context(), "p", "bb-1")
		require.NoError(t, err)
		assert.True(t, found)

		return issues
	}

	assert.Equal(t, []int{12}, read("Closes #12"), "the first review")
	assert.Equal(t, []int{12, 99}, read(`Closes #12\nCloses #99`), "a new keyword after the first review shows in the scope read, so the run asks the owner")
}
