package store

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestRefuseDecode_KeepsTheCauseOfEitherKindOfError(t *testing.T) {
	t.Parallel()

	other := errors.New("not a decode error")

	tests := []struct {
		name string
		err  error
		want error
	}{
		{"decode_error_gives_its_cause", &model.DecodeError{Command: "bd x", Cause: model.ErrNullAnswer}, model.ErrNullAnswer},
		{"any_other_error_is_the_cause", other, other},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := refuseDecode("bd x", tc.err)
			require.ErrorIs(t, got, tc.want)
			require.Equal(t, CodeRefused, got.Code)
			require.ErrorContains(t, got, "`bd x`")
		})
	}
}
