package claude_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/agent/claude"
)

func TestCalls_TheResultLineReportsTheTotalCostInDollars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, line string
		want       float64
	}{
		{"success", `{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.0421}`, 0.0421},
		{"budget cap", `{"type":"result","subtype":"error_max_budget_usd","is_error":true,"total_cost_usd":2.5}`, 2.5},
		{"no cost field", `{"type":"result","subtype":"success"}`, 0},
		{"a cost that is no number", `{"type":"result","subtype":"success","total_cost_usd":"lots"}`, 0},
		{"a negative cost", `{"type":"result","subtype":"success","total_cost_usd":-1}`, 0},
		{"not a result line", `{"type":"assistant","total_cost_usd":9}`, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tc.want, claude.New(nil).Calls(tc.line).Cost, 0)
		})
	}
}
