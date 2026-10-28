package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/store"
)

func TestDecode_ReadsAPayloadOrRefusesNamingTheCommand(t *testing.T) {
	t.Parallel()

	type row struct {
		ID string `json:"id"`
	}

	got, err := store.Decode[row]("bd show", `{"id":"x"}`)
	require.NoError(t, err)
	require.Equal(t, "x", got.ID)

	_, err = store.Decode[row]("bd show", `[1]`)
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
