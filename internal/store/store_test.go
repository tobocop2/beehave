package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
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

func TestRefuseDuplicate_MatchesTitlesIgnoringCaseAndBlanks(t *testing.T) {
	t.Parallel()

	open := []store.OpenTitle{{ID: "a-1", Title: "Other"}, {ID: "a-2", Title: "  Fix \t the   Bug "}}

	tests := []struct {
		name    string
		request model.CreateRequest
		wantErr bool
	}{
		{"same_title_other_case_and_blanks", model.CreateRequest{Title: "fix the bug"}, true},
		{"different_title", model.CreateRequest{Title: "fix the other bug"}, false},
		{"duplicate_allowed", model.CreateRequest{Title: "fix the bug", AllowDuplicate: true}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := store.RefuseDuplicate(tc.request, open)
			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			var refusal *store.Error
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, store.CodeDuplicate, refusal.Code)
			require.ErrorContains(t, err, "a-2 is open")
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

func TestCheckCreate_RefusesAnInvalidRequestWithCodeTwo(t *testing.T) {
	t.Parallel()

	body := filepath.Join(t.TempDir(), "body.md")
	require.NoError(t, os.WriteFile(body, []byte("finding"), 0o600))
	require.NoError(t, store.CheckCreate(model.CreateRequest{Title: "t", BodyFile: body, Severity: model.SeverityP1}))

	err := store.CheckCreate(model.CreateRequest{})
	require.ErrorContains(t, err, "REFUSE: ")

	var refusal *store.Error
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, store.CodeRefused, refusal.Code)
	require.Error(t, errors.Unwrap(err))
}
