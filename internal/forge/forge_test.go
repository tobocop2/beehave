package forge_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

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
