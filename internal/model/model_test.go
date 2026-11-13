package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestParseSeverity(t *testing.T) {
	t.Parallel()

	for _, severity := range model.Severities() {
		got, err := model.ParseSeverity(string(severity))
		require.NoError(t, err)
		assert.Equal(t, severity, got)
	}

	for _, text := range []string{"", "P4", "p0", "P?"} {
		_, err := model.ParseSeverity(text)
		require.ErrorIs(t, err, model.ErrUnknownSeverity, text)
	}
}

func TestItem_LineSplitsOnTheUnitSeparator(t *testing.T) {
	t.Parallel()

	item := model.Item{ID: "p-1", Severity: model.SeverityP1, Hold: true, Parent: "p-0", Assignee: "ann", Title: "a\tb"}
	assert.Equal(t, "p-1\x1fP1\x1f1\x1fp-0\x1fann\x1fa\tb", item.Line())
	item.Hold = false
	assert.Equal(t, "p-1\x1fP1\x1f0\x1fp-0\x1fann\x1fa\tb", item.Line())
}

func TestIsDigits(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]bool{"0": true, "2021": true, "": false, "12a": false, "-1": false, "٣": false, "/": false, ":": false} {
		assert.Equal(t, want, model.IsDigits(text), text)
	}
}
