package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/store"
)

func TestDecodeRows_ReadsAListOrRefusesNamingTheCommand(t *testing.T) {
	t.Parallel()

	got, err := store.DecodeRows[int]("bd show", `[1]`, 3, "bd list")
	require.NoError(t, err)
	require.Equal(t, []int{1}, got)

	_, err = store.DecodeRows[int]("bd show", `{"id":"x"}`, 3, "bd list")
	require.ErrorContains(t, err, "`bd show`")

	var refusal *store.Error
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, store.CodeRefused, refusal.Code)
}

func TestDecodeRows_RefusesAListAtTheLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		wantLen int
		wantErr string
	}{
		{"under_the_limit", `[1,2]`, 2, ""},
		{"at_the_limit", `[1,2,3]`, 0, "bd list returned 3 rows"},
		{"unreadable", `{`, 0, "`bd x`"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rows, err := store.DecodeRows[int]("bd x", tc.text, 3, "bd list")
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			require.Len(t, rows, tc.wantLen)
		})
	}
}

func TestReadableTitle_RefusesWhatIsMissing(t *testing.T) {
	t.Parallel()

	title := "t"
	got, err := store.ReadableTitle(&title)
	require.NoError(t, err)
	require.Equal(t, "t", got)

	_, err = store.ReadableTitle(nil)
	require.ErrorContains(t, err, "no readable title")
}
