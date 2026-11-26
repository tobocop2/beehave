package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

// shieldedShip is the forge fake of the merge scripts with the optional status side: it reads the protection of a branch.
type shieldedShip struct {
	*shipForge
	protection model.Protection
}

func (shieldedShip) PostStatus(context.Context, model.CommitStatus) error { return nil }

func (s shieldedShip) Protection(context.Context, string) (model.Protection, error) {
	return s.protection, nil
}

// withWriter puts a writer credential and the forge name github on the project of the pulls.
func withWriter(p pulls) pulls {
	project := p.projects["p"]
	project.Forge = "github"
	project.Credentials = map[model.Role]model.Credential{model.RoleWriter: {}}
	p.projects = map[string]config.Project{"p": project}

	return p
}

func TestMerge_AProjectWithCredentialsIsRefusedOnABaseThatRequiresNoStatusAndMergesNothing(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	p = withWriter(p)
	p.forge = func(model.Project) (forge.Forge, error) { return shieldedShip{shipForge: f}, nil }

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, strings.Join(result.Lines, "\n"), "REFUSE: the base integration does not require a pull request and beehave/gate and beehave/review.")
	assert.Empty(t, f.writes, "the host marks the request as one of a project with credentials, so the protection guard runs")
}

func TestMerge_AProjectWithCredentialsMergesOnABaseThatRequiresBothStatusesAndAPullRequest(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	p = withWriter(p)
	full := model.Protection{PullRequest: true, RequiredStatuses: []string{model.ContextGate, model.ContextReview}}
	p.forge = func(model.Project) (forge.Forge, error) { return shieldedShip{shipForge: f, protection: full}, nil }

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Done, result.Stop, strings.Join(result.Lines, "\n"))
	assert.Equal(t, []string{"MergePR 5 " + gated}, f.writes)
}

func TestMerge_AProjectWithCredentialsOnAForgeThatCannotReadTheProtectionIsRefusedWithTheForgeName(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	p = withWriter(p)

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, strings.Join(result.Lines, "\n"), "REFUSE: the forge github cannot read the protection of the base integration.")
	assert.Empty(t, f.writes)
}

func TestMerge_AProjectWithNoCredentialsOnAForgeThatCannotReadTheProtectionKeepsItsGuards(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Done, result.Stop, strings.Join(result.Lines, "\n"))
	assert.Equal(t, []string{"MergePR 5 " + gated}, f.writes)
}

func TestMerge_AProjectWithNoCredentialsIsToldItNeedsThemWhenTheBaseRequiresABeehaveStatus(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	p.forge = func(model.Project) (forge.Forge, error) {
		return shieldedShip{shipForge: f, protection: model.Protection{RequiredStatuses: []string{model.ContextGate}}}, nil
	}

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, strings.Join(result.Lines, "\n"), "the project has no `credentials` key")
	assert.Empty(t, f.writes)
}

func TestOpened_AForgeWithoutStatusesGivesTheNoProtectionReadError(t *testing.T) {
	t.Parallel()

	_, err := opened{noTreeForge{}}.Protection(t.Context(), "main")

	require.ErrorIs(t, err, forge.ErrNoCapability)
}

func TestOpened_AForgeWithStatusesPassesTheProtectionThrough(t *testing.T) {
	t.Parallel()

	want := model.Protection{PullRequest: true, RequiredStatuses: []string{"x"}}

	got, err := opened{shieldedShip{shipForge: &shipForge{}, protection: want}}.Protection(t.Context(), "main")

	require.NoError(t, err)
	assert.Equal(t, want, got)
}
