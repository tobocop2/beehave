package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestAlert_StringNamesTheNumberTheRuleTheSeverityAndTheLink(t *testing.T) {
	t.Parallel()

	alert := model.Alert{Number: 7, Rule: "go/sql-injection", Severity: "high", URL: "https://example.test/alerts/7"}

	assert.Equal(t, "#7 go/sql-injection (high) https://example.test/alerts/7", alert.String())
}
