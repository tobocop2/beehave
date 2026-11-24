package forge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/forge/forgetest"
	"github.com/tobocop2/beehave/internal/model"
)

// failedReads is a fake that answers every optional read with an error. It tests the suite checks only; it does not run any adapter.
type failedReads struct{ assignees, ready, card, place error }

func (f failedReads) IssueAssignees(context.Context, string, int) ([]string, error) {
	return nil, f.assignees
}

func (f failedReads) ReadyMarks(context.Context, int) ([]string, error) { return nil, f.ready }

func (f failedReads) PlaceCard(context.Context, model.Card) error { return f.place }

func (f failedReads) CardStatus(context.Context, model.Card) (string, error) { return "", f.card }

func TestOptionalReadChecks_AcceptAFakeThatAnswersWithErrors(t *testing.T) {
	t.Parallel()

	failed := failedReads{
		assignees: errors.New("IssueAssignees: the forge is down"),
		ready:     errors.New("ReadyMarks: the forge is down"),
		card:      errors.New("CardStatus: the forge is down"),
		place:     errors.New("PlaceCard: the forge is down"),
	}

	forgetest.CheckAssigneeReadsFail(t, failed)
	forgetest.CheckReadyReadsFail(t, failed)
	forgetest.CheckBoarderFails(t, failed)
}

func TestRefuse_FormatsTheMessageWithCodeTwo(t *testing.T) {
	t.Parallel()

	err := forge.Refuse("forge: %d bad", 3)
	require.Equal(t, forge.CodeRefused, err.Code)
	require.EqualError(t, err, "forge: 3 bad")
	require.NoError(t, errors.Unwrap(err))
}

func TestRefusal_AddsTheCommandOnlyForAnAnswerOfTheWrongShape(t *testing.T) {
	t.Parallel()

	plain := errors.New("boom")
	bad := &model.DecodeError{Command: "gh pr list", Cause: model.ErrNullAnswer}

	tests := []struct {
		name  string
		cause error
		want  string
	}{
		{"other_error_keeps_the_base", plain, "forge: cannot read"},
		{"decode_error_adds_the_command", bad, "forge: cannot read: " + bad.Error()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := forge.Refusal("forge: cannot read", tc.cause)
			require.EqualError(t, err, tc.want)
			require.ErrorIs(t, err, tc.cause)
			require.Equal(t, forge.CodeRefused, err.Code)
		})
	}
}

func TestError_ShowsTheMessageOfTheForgeAfterItsOwnMessage(t *testing.T) {
	t.Parallel()

	err := forge.Refuse("cannot merge pull request %d", 4)
	require.EqualError(t, err, "cannot merge pull request 4")

	err.Detail = "head branch was modified"
	require.EqualError(t, err, "cannot merge pull request 4: head branch was modified")
}
