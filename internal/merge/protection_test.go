package merge_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/merge"
	"github.com/tobocop2/beehest/internal/model"
)

// shielded is the fake forge that also reads the protection of a branch. reads lists the branches it was asked about.
type shielded struct {
	*world
	protection model.Protection
	err        error
	reads      []string
}

func (s *shielded) Protection(_ context.Context, branch string) (model.Protection, error) {
	s.reads = append(s.reads, branch)

	return s.protection, s.err
}

func shield(t *testing.T, protection model.Protection) *shielded {
	t.Helper()

	return &shielded{world: newWorld(t), protection: protection}
}

func credentialed() merge.Request {
	req := request()
	req.Credentialed = true
	req.ForgeName = "github"

	return req
}

var full = model.Protection{PullRequest: true, RequiredStatuses: []string{model.ContextGate, model.ContextReview}}

func TestRun_AProjectWithCredentialsMergesIntoABaseThatRequiresBothStatusesAndAPullRequest(t *testing.T) {
	t.Parallel()

	s := shield(t, full)

	result := merge.Run(context.Background(), s, credentialed())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, []string{"main"}, s.reads, "the protection of the base is read")
	assert.Equal(t, []string{"MergePR 5 " + sha}, s.writes)
}

func TestRun_TheOrderOfTheRequiredStatusesDoesNotMatterAndOtherStatusesMayBeRequired(t *testing.T) {
	t.Parallel()

	s := shield(t, model.Protection{PullRequest: true, RequiredStatuses: []string{"ci", model.ContextReview, "test", model.ContextGate}})

	result := merge.Run(context.Background(), s, credentialed())

	assert.Equal(t, merge.Done, result.Stop)
}

func TestRun_AProjectWithCredentialsIsRefusedBeforeAnyWriteWhenTheBaseLacksARule(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		protection model.Protection
		missing    string
	}{
		"no rule at all":      {model.Protection{}, "a pull request and beehest/gate and beehest/review"},
		"no gate status":      {model.Protection{PullRequest: true, RequiredStatuses: []string{model.ContextReview}}, "beehest/gate."},
		"no review status":    {model.Protection{PullRequest: true, RequiredStatuses: []string{model.ContextGate}}, "beehest/review."},
		"no pull request":     {model.Protection{RequiredStatuses: full.RequiredStatuses}, "a pull request."},
		"other status only":   {model.Protection{PullRequest: true, RequiredStatuses: []string{"ci"}}, "beehest/gate and beehest/review."},
		"statuses and no pr":  {model.Protection{RequiredStatuses: []string{model.ContextGate}}, "a pull request and beehest/review."},
		"nil status list":     {model.Protection{PullRequest: true}, "beehest/gate and beehest/review."},
		"gate inside a name":  {model.Protection{PullRequest: true, RequiredStatuses: []string{"xbeehest/gate", model.ContextReview}}, "beehest/gate."},
		"review status twice": {model.Protection{PullRequest: true, RequiredStatuses: []string{model.ContextReview, model.ContextReview}}, "beehest/gate."},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := shield(t, tc.protection)

			result := merge.Run(context.Background(), s, credentialed())

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Equal(t, merge.Plain, result.Reason)
			assert.Empty(t, s.writes, "the refusal comes before any write")
			assert.Empty(t, merges(s.world))
			assert.Contains(t, all(result), "REFUSE: the base main does not require "+tc.missing)
			assert.Contains(t, all(result), "Add a ruleset on main that requires a pull request and the statuses beehest/gate and beehest/review")
		})
	}
}

func TestRun_TheProtectionRefusalStopsBeforeTheStatusPostAndTheReadyStep(t *testing.T) {
	t.Parallel()

	s := shield(t, model.Protection{})
	s.set(t, map[string]any{"isDraft": true})
	req, _ := posting(s.world)
	req.Credentialed, req.ForgeName, req.OwnDraft = true, "github", pr

	result := merge.Run(context.Background(), s, req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Empty(t, s.writes, "no SetDraft, no status and no MergePR happen for a base that would not require the statuses")
}

func TestRun_AProjectWithCredentialsReadsTheProtectionOfItsOwnBase(t *testing.T) {
	t.Parallel()

	s := shield(t, full)
	req := credentialed()
	req.Base = "integration/r2"
	s.set(t, map[string]any{"baseRefName": "integration/r2"})

	result := merge.Run(context.Background(), s, req)

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, []string{"integration/r2"}, s.reads)
}

func TestRun_AFailedReadOfTheProtectionDefersAProjectWithCredentials(t *testing.T) {
	t.Parallel()

	s := shield(t, full)
	s.err = errDown

	result := merge.Run(context.Background(), s, credentialed())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Transient, result.Reason, "a read that failed is a cause that can pass")
	assert.Empty(t, s.writes)
	assert.Contains(t, all(result), "REFUSE: cannot read the protection of the base main: down")
}

// noRead is a forge that answers the protection read as the host does for a forge that lacks it.
type noRead struct{ *world }

func (noRead) Protection(context.Context, string) (model.Protection, error) {
	return model.Protection{}, merge.ErrNoProtectionRead
}

func TestRun_AForgeThatCannotReadTheProtectionRefusesAProjectWithCredentialsAndNamesTheForge(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func(*world) merge.Forge{
		"no read method":     func(w *world) merge.Forge { return w },
		"the host's no-read": func(w *world) merge.Forge { return noRead{w} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)

			result := merge.Run(context.Background(), build(w), credentialed())

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Equal(t, merge.Plain, result.Reason, "a forge that cannot read will not read on a later try")
			assert.Empty(t, w.writes)
			assert.Contains(t, all(result), "REFUSE: the forge github cannot read the protection of the base main.")
			assert.Contains(t, all(result), "remove `credentials` from the project")
		})
	}
}

func TestRun_AProjectWithNoCredentialsKeepsItsGuardsOnABaseWithNoBeehestStatusRule(t *testing.T) {
	t.Parallel()

	for name, protection := range map[string]model.Protection{
		"no rule":             {},
		"a pull request only": {PullRequest: true},
		"another status only": {PullRequest: true, RequiredStatuses: []string{"ci"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := shield(t, protection)

			result := merge.Run(context.Background(), s, request())

			assert.Equal(t, merge.Done, result.Stop)
			assert.Equal(t, []string{"MergePR 5 " + sha}, s.writes)
		})
	}
}

func TestRun_AProjectWithNoCredentialsIsToldItNeedsThemWhenTheBaseRequiresABeehestStatus(t *testing.T) {
	t.Parallel()

	for name, required := range map[string]string{"gate": model.ContextGate, "review": model.ContextReview} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := shield(t, model.Protection{RequiredStatuses: []string{"ci", required}})

			result := merge.Run(context.Background(), s, request())

			assert.Equal(t, merge.Refused, result.Stop)
			assert.Empty(t, s.writes)
			assert.Contains(t, all(result), "REFUSE: the base main requires the status "+required+", and the project has no `credentials` key, so beehest cannot post it.")
			assert.Contains(t, all(result), "The project needs credentials")
		})
	}
}

func TestRun_AProjectWithNoCredentialsGoesOnWhenTheProtectionCannotBeRead(t *testing.T) {
	t.Parallel()

	s := shield(t, model.Protection{})
	s.err = errDown

	result := merge.Run(context.Background(), s, request())

	require.Equal(t, merge.Done, result.Stop, "the forge still enforces its own rules, so a read that fails is no reason to refuse")
	assert.Equal(t, []string{"MergePR 5 " + sha}, s.writes)
}

func TestRun_AProjectWithNoCredentialsGoesOnOnAForgeThatCannotReadTheProtection(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	result := merge.Run(context.Background(), noRead{w}, request())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, []string{"MergePR 5 " + sha}, w.writes)
}
