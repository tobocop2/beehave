package store

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestUnreadable_KeepsTheCauseOfADecodeErrorAndPassesAnyOtherErrorThrough(t *testing.T) {
	t.Parallel()

	other := errors.New("not a decode error")

	got := Unreadable(&model.DecodeError{Command: "bd x", Cause: model.ErrNullAnswer})

	var refusal *Error

	require.ErrorAs(t, got, &refusal)
	require.ErrorIs(t, got, model.ErrNullAnswer)
	require.Equal(t, CodeRefused, refusal.Code)
	require.ErrorContains(t, got, "`bd x`")

	require.Equal(t, other, Unreadable(other))
	require.NoError(t, Unreadable(nil))
}
