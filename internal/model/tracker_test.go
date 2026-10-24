package model_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestParseTracker(t *testing.T) {
	t.Parallel()

	for _, name := range model.Trackers() {
		got, err := model.ParseTracker(string(name))
		require.NoError(t, err)
		assert.Equal(t, name, got)
	}

	_, err := model.ParseTracker("jira")
	require.ErrorIs(t, err, model.ErrUnknownTracker)
	assert.EqualError(t, err, `unknown tracker "jira": use one of beads, github`)
}

func TestParseForge(t *testing.T) {
	t.Parallel()

	for _, name := range model.Forges() {
		got, err := model.ParseForge(string(name))
		require.NoError(t, err)
		assert.Equal(t, name, got)
	}

	_, err := model.ParseForge("gitlab")
	require.ErrorIs(t, err, model.ErrUnknownForge)
	assert.EqualError(t, err, `unknown forge "gitlab": use one of github`)
}

func TestCreateRequest_Validate(t *testing.T) {
	t.Parallel()

	body := filepath.Join(t.TempDir(), "body.md")
	require.NoError(t, os.WriteFile(body, []byte("finding\n"), 0o600))

	empty := filepath.Join(t.TempDir(), "empty.md")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))

	tests := map[string]struct {
		request model.CreateRequest
		want    string
	}{
		"no_title":           {model.CreateRequest{BodyFile: body, Severity: model.SeverityP1}, "create needs --title"},
		"no_body_file":       {model.CreateRequest{Title: "t", Severity: model.SeverityP1}, "create needs --body-file with the finding and its reproduction"},
		"missing_body_file":  {model.CreateRequest{Title: "t", BodyFile: body + ".gone", Severity: model.SeverityP1}, "create needs --body-file"},
		"empty_body_file":    {model.CreateRequest{Title: "t", BodyFile: empty, Severity: model.SeverityP1}, "create needs --body-file"},
		"no_severity":        {model.CreateRequest{Title: "t", BodyFile: body}, "create needs --severity P0, P1, P2 or P3, got ''"},
		"severity_p4":        {model.CreateRequest{Title: "t", BodyFile: body, Severity: "P4"}, "got 'P4'"},
		"unknown_severity_p": {model.CreateRequest{Title: "t", BodyFile: body, Severity: model.SeverityUnknown}, "got 'P?'"},
	}

	for name, tt := range tests {
		err := tt.request.Validate()
		require.ErrorIs(t, err, model.ErrInvalidRequest, name)
		require.ErrorContains(t, err, tt.want, name)
	}

	for _, severity := range model.Severities() {
		require.NoError(t, model.CreateRequest{Title: "t", BodyFile: body, Severity: severity}.Validate())
	}
}
