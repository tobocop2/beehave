package forgetest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/forge/forgetest"
	"github.com/tobocop2/obee/internal/model"
)

var errDown = errors.New("down")

// recorder is a Reporter that keeps the failures instead of failing the test.
type recorder struct {
	failures []string
}

func (*recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// memForge serves pull request 7. wrong changes the body; down fails every call.
type memForge struct {
	wrong bool
	down  bool
}

func (m memForge) fail() error {
	if m.down {
		return errDown
	}

	return nil
}

func (m memForge) Reviews(context.Context, int) ([]model.Review, error) {
	return []model.Review{{User: model.Account{Login: forgetest.SevenReviewer}, State: model.ReviewApproved}}, m.fail()
}

func (m memForge) PRBody(context.Context, int) (string, error) {
	if m.wrong {
		return "wrong", nil
	}

	return forgetest.SevenBody, m.fail()
}

func (m memForge) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return model.PullRequest{Title: forgetest.SevenTitle}, m.fail()
}

func (m memForge) PRList(context.Context, int, ...string) ([]model.PullRequest, error) {
	return []model.PullRequest{{Number: forgetest.Seven}}, m.fail()
}

func (m memForge) PRChecks(context.Context, int) ([]model.Check, error) {
	return []model.Check{{Name: forgetest.SevenCheck, Bucket: model.CheckPass}}, m.fail()
}

func (m memForge) OpenChangeRequests(context.Context, int) ([]model.ChangeRequest, error) {
	return nil, m.fail()
}

func (m memForge) UnresolvedThreads(context.Context, int) ([]model.ReviewThread, error) {
	return nil, m.fail()
}

func (m memForge) StackID(context.Context, int) (string, error) { return "", m.fail() }

func (m memForge) ExistingReview(context.Context, int) ([]string, error) { return nil, m.fail() }

func (m memForge) Overlap(context.Context, model.OverlapRequest) (model.Overlap, error) {
	return model.Overlap{}, m.fail()
}

func TestCheckForgeReads_FailsAForgeThatReturnsAWrongBody(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	forgetest.CheckForgeReads(r, memForge{})
	assert.Empty(t, r.failures)

	r = &recorder{}
	forgetest.CheckForgeReads(r, memForge{wrong: true})
	assert.Contains(t, r.failures, "body: got wrong, want Closes #5")

	r = &recorder{}
	forgetest.CheckForgeReads(r, memForge{down: true})
	assert.Contains(t, r.failures, "Reviews: unexpected error: down")
}

func TestCheckForgeFails_AcceptsAForgeThatErrorsEverywhereAndFailsOneThatDoesNot(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	forgetest.CheckForgeFails(r, memForge{down: true})
	assert.Empty(t, r.failures)

	r = &recorder{}
	forgetest.CheckForgeFails(r, memForge{})
	assert.Contains(t, r.failures, "Overlap: want an error from a failed command, got none")
}

func TestTestForge_RunsBothSuitesOnAFactory(t *testing.T) {
	t.Parallel()

	forgetest.TestForge(t, func(t *testing.T, broken bool) forge.Forge {
		t.Helper()

		return memForge{down: broken}
	})
}
