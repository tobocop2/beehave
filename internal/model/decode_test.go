package model_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestDecode_ReadsOneValue(t *testing.T) {
	t.Parallel()

	got, err := model.Decode[[]int]("gh list", " [1, 2]\n")
	require.NoError(t, err)
	require.Equal(t, []int{1, 2}, got)
}

func TestDecode_RefusesAndNamesTheCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want string
	}{
		{"null", "null", "the answer is null"},
		{"padded_null", " null\n", "the answer is null"},
		{"text_after_the_value", "[] []", "after top-level value"},
		{"wrong_shape", `{"a":1}`, "cannot unmarshal object"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := model.Decode[[]int]("gh list", tc.text)

			var bad *model.DecodeError
			require.ErrorAs(t, err, &bad)
			require.Equal(t, "gh list", bad.Command)
			require.ErrorContains(t, err, "`gh list`")
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestDecode_KeepsTheCauseReachable(t *testing.T) {
	t.Parallel()

	_, err := model.Decode[[]int]("gh list", "null")
	require.ErrorIs(t, err, model.ErrNullAnswer)

	_, err = model.Decode[[]int]("gh list", "[] []")

	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax)
}
