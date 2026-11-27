package claude_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/agent/claude"
)

// The rate-limited-*.jsonl fixtures are built from the code of the Claude CLI 2.1.325 that writes these lines, not from a captured run: a rate_limit_event
// carries the rate_limit_info that the CLI builds from the response headers, and the assistant line of a refused request carries error "rate_limit".

// The reasons that the adapter gives for a refusal that no wait cures.
const (
	deniedPause   = "the provider paused the usage of the organization, and no wait ends the pause: contact the support of the provider"
	deniedCredits = "the provider asks for usage credits, and no wait buys them: add usage credits or use another model"
)

// fixtureLines returns the lines of a fixture file.
func fixtureLines(t *testing.T, name string) []string {
	t.Helper()

	return strings.Split(strings.TrimSpace(fixture(t, name)), "\n")
}

func TestCalls_ARejectedRateLimitEventReportsALimit(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, fixture string }{
		{"five_hour", "rate-limited-five-hour.jsonl"},
		{"seven_day", "rate-limited-seven-day.jsonl"},
		{"overage_out_of_credits", "rate-limited-overage.jsonl"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seen := claude.Adapter{}.Calls(fixtureLines(t, tc.fixture)[0])

			assert.NotEqual(t, agent.Seen{}, seen, "the adapter reads nothing from the rejected rate_limit_event")
			assert.NotNil(t, seen.Limit, "the rejected rate_limit_event reports no limit")
		})
	}
}

func TestCalls_ARejectedRateLimitEventCarriesItsKindWindowAndResetTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, fixture string
		want          agent.Limit
	}{
		{"seven_day_with_no_overage_is_a_rate_limit", "rate-limited-seven-day.jsonl", agent.Limit{Kind: agent.LimitRate, Window: "seven_day", ResetsAt: time.Unix(1795392000, 0).UTC()}},
		{"five_hour_with_overage_not_provisioned_is_a_rate_limit", "rate-limited-five-hour.jsonl", agent.Limit{Kind: agent.LimitRate, Window: "five_hour", ResetsAt: time.Unix(1795006800, 0).UTC()}},
		{"overage_out_of_credits_takes_the_earlier_spend_reset", "rate-limited-overage.jsonl", agent.Limit{Kind: agent.LimitSpend, Window: "seven_day", ResetsAt: time.Unix(1795132800, 0).UTC()}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seen := claude.Adapter{}.Calls(fixtureLines(t, tc.fixture)[0])

			require.NotNil(t, seen.Limit)
			assert.Equal(t, tc.want, *seen.Limit)
		})
	}
}

func TestCalls_ASpendLimitResetsAtTheEarlierOfThePlanResetAndTheSpendReset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, info string
		want       time.Time
	}{
		{"plan_resets_first", `"resetsAt":1795000000,"overageResetsAt":1795100000`, time.Unix(1795000000, 0).UTC()},
		{"spend_resets_first", `"resetsAt":1795100000,"overageResetsAt":1795000000`, time.Unix(1795000000, 0).UTC()},
		{"only_the_plan_reset", `"resetsAt":1795000000`, time.Unix(1795000000, 0).UTC()},
		{"only_the_spend_reset", `"overageResetsAt":1795000000`, time.Unix(1795000000, 0).UTC()},
		{"no_reset", `"rateLimitType":"overage"`, time.Time{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			line := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","overageStatus":"rejected","overageDisabledReason":"out_of_credits","isUsingOverage":false,` + tc.info + `}}`
			seen := claude.Adapter{}.Calls(line)

			require.NotNil(t, seen.Limit)
			assert.Equal(t, agent.LimitSpend, seen.Limit.Kind)
			assert.Equal(t, tc.want, seen.Limit.ResetsAt)
		})
	}
}

// The overageDisabledReason values are the enum of the CLI 2.1.325. The CLI writes org_spend_cap_reached as org_level_disabled_until on the stream.
func TestCalls_ARefusedOverageIsASpendLimitOnlyWhenPaidOverageWasAvailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reason string
		want   agent.LimitKind
	}{
		{"out_of_credits", agent.LimitSpend},
		{"org_level_disabled_until", agent.LimitSpend},
		{"overage_not_provisioned", agent.LimitRate},
		{"org_level_disabled", agent.LimitRate},
		{"seat_tier_level_disabled", agent.LimitRate},
		{"member_level_disabled", agent.LimitRate},
		{"seat_tier_zero_credit_limit", agent.LimitRate},
		{"group_zero_credit_limit", agent.LimitRate},
		{"member_zero_credit_limit", agent.LimitRate},
		{"org_service_level_disabled", agent.LimitRate},
		{"no_limits_configured", agent.LimitRate},
		{"fetch_error", agent.LimitRate},
		{"unknown", agent.LimitRate},
	}

	for _, tc := range tests {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()

			line := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1795100000,"rateLimitType":"five_hour","overageStatus":"rejected",` +
				`"overageResetsAt":1795000000,"overageDisabledReason":"` + tc.reason + `","isUsingOverage":false}}`
			want := agent.Limit{Kind: tc.want, Window: "five_hour", ResetsAt: time.Unix(1795100000, 0).UTC()}
			if tc.want == agent.LimitSpend {
				want.ResetsAt = time.Unix(1795000000, 0).UTC()
			}

			seen := claude.Adapter{}.Calls(line)

			require.NotNil(t, seen.Limit)
			assert.Equal(t, want, *seen.Limit)
		})
	}

	t.Run("no_reason", func(t *testing.T) {
		t.Parallel()

		seen := claude.Adapter{}.Calls(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1795100000,"overageStatus":"rejected","isUsingOverage":false}}`)

		require.NotNil(t, seen.Limit)
		assert.Equal(t, agent.Limit{Kind: agent.LimitRate, ResetsAt: time.Unix(1795100000, 0).UTC()}, *seen.Limit)
	})
}

func TestCalls_ARejectedRateLimitEventWithNoUsableResetTimeHasTheZeroTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, info string
		want       agent.Limit
	}{
		{"missing", `"rateLimitType":"five_hour","isUsingOverage":false`, agent.Limit{Kind: agent.LimitRate, Window: "five_hour"}},
		{"zero", `"resetsAt":0,"isUsingOverage":false`, agent.Limit{Kind: agent.LimitRate}},
		{"negative", `"resetsAt":-5,"isUsingOverage":false`, agent.Limit{Kind: agent.LimitRate}},
		{"not_a_number", `"resetsAt":null,"isUsingOverage":false`, agent.Limit{Kind: agent.LimitRate}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seen := claude.Adapter{}.Calls(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected",` + tc.info + `}}`)

			require.NotNil(t, seen.Limit)
			assert.Equal(t, tc.want, *seen.Limit)
		})
	}
}

func TestCalls_ARejectedRateLimitEventWhilePaidOverageServesIsNoLimit(t *testing.T) {
	t.Parallel()

	line := fixtureLines(t, "rate-limited-overage-in-use.jsonl")[0]

	assert.Equal(t, agent.Seen{}, claude.Adapter{}.Calls(line), "paid overage serves the request, so the plan limit refuses no work: %s", line)
}

func TestCalls_ARefusalThatAsksForUsageCreditsIsDeniedAndNoLimit(t *testing.T) {
	t.Parallel()

	lines := fixtureLines(t, "rate-limited-credits-required.jsonl")
	tests := []struct{ name, line string }{
		{"assistant_long_context_credits_required", lines[0]},
		{"assistant_model_requires_usage_credits", lines[1]},
		{"event_with_no_window", lines[2]},
		{"event_with_the_overage_window", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"overage","isUsingOverage":false,"errorCode":"credits_required"}}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seen := claude.Adapter{}.Calls(tc.line)

			assert.Nil(t, seen.Limit, "a wait buys no usage credits, so the line is no limit")
			assert.Equal(t, deniedCredits, seen.Denied)
		})
	}
}

func TestCalls_ACreditsErrorCodeOnAPlanWindowIsALimit(t *testing.T) {
	t.Parallel()

	seen := claude.Adapter{}.Calls(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1795100000,"rateLimitType":"five_hour","isUsingOverage":false,"errorCode":"credits_required"}}`)

	require.NotNil(t, seen.Limit, "the plan window resets, so a wait cures the refusal")
	assert.Equal(t, agent.Limit{Kind: agent.LimitRate, Window: "five_hour", ResetsAt: time.Unix(1795100000, 0).UTC()}, *seen.Limit)
	assert.Empty(t, seen.Denied)
}

func TestCalls_AnOrganizationPauseIsDeniedAndNoLimit(t *testing.T) {
	t.Parallel()

	lines := fixtureLines(t, "rate-limited-paused.jsonl")
	tests := []struct{ name, line string }{
		{"event", lines[0]},
		{"assistant_usage_limit_reached", lines[1]},
		{"event_that_also_asks_for_credits", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","isUsingOverage":false,"dailyCapPaused":true,"errorCode":"credits_required"}}`},
		{
			"assistant_line_that_also_asks_for_credits",
			`{"type":"assistant","message":{"content":[]},"error":"rate_limit","api_error":"model_requires_usage_credits","api_error_params":{"rate_limit_info":{"dailyCapPaused":true}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seen := claude.Adapter{}.Calls(tc.line)

			assert.Nil(t, seen.Limit, "no reset ends a pause, so the line is no limit")
			assert.Equal(t, deniedPause, seen.Denied)
		})
	}
}

func TestCalls_APauseFlagThatIsNotTrueIsALimit(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, line string }{
		{"event", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","isUsingOverage":false,"dailyCapPaused":false}}`},
		{"assistant_line", `{"type":"assistant","message":{"content":[]},"error":"rate_limit","api_error":"usage_limit_reached","api_error_params":{"rate_limit_info":{"status":"rejected"}}}`},
		{"assistant_line_with_params_of_another_shape", `{"type":"assistant","message":{"content":[]},"error":"rate_limit","api_error_params":"none"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			seen := claude.Adapter{}.Calls(tc.line)

			require.NotNil(t, seen.Limit)
			assert.Equal(t, agent.Limit{Kind: agent.LimitRate}, *seen.Limit)
			assert.Empty(t, seen.Denied)
		})
	}
}

func TestCalls_AnAllowedOrWarningRateLimitEventIsNoLimit(t *testing.T) {
	t.Parallel()

	lines := fixtureLines(t, "rate-limited-allowed.jsonl")
	tests := []struct{ name, line string }{
		{"allowed", lines[0]},
		{"allowed_warning", lines[1]},
		{"no_rate_limit_info", `{"type":"rate_limit_event"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, agent.Seen{}, claude.Adapter{}.Calls(tc.line), "a rate_limit_event that allows the request is no limit")
		})
	}
}

func TestCalls_AnAssistantRateLimitErrorIsALimitWithNoTime(t *testing.T) {
	t.Parallel()

	overload := fixtureLines(t, "rate-limited-assistant-error.jsonl")[0]
	line := strings.Replace(overload, "Opus is experiencing high load, please use /model to switch to Sonnet", "API Error: Request rejected (429)", 1)
	require.NotEqual(t, overload, line, "the control: the text is the one thing that changes")

	seen := claude.Adapter{}.Calls(line)

	assert.NotEqual(t, agent.Seen{}, seen, "the adapter reads nothing from an assistant rate_limit error")
	require.NotNil(t, seen.Limit)
	assert.Equal(t, agent.Limit{Kind: agent.LimitRate}, *seen.Limit, "an assistant rate_limit line that does not say overload is a rate limit")
}

func TestCalls_AnAssistantRateLimitLineThatSaysOverloadIsAnOverloadWithNoTime(t *testing.T) {
	t.Parallel()

	highLoad := fixtureLines(t, "rate-limited-assistant-error.jsonl")[0]
	apiError := strings.Replace(strings.Replace(highLoad, "Opus is experiencing high load, please use /model to switch to Sonnet", "API Error: 529", 1),
		`"is_api_error_message":true`, `"is_api_error_message":true,"api_error":"overloaded"`, 1)
	require.NotEqual(t, highLoad, apiError, "the control: the text and the api_error change")

	for name, line := range map[string]string{"the high load text of the CLI": highLoad, "the overloaded api_error": apiError} {
		seen := claude.Adapter{}.Calls(line)

		require.NotNil(t, seen.Limit, name)
		assert.Equal(t, agent.Limit{Kind: agent.LimitOverload}, *seen.Limit, name)
		assert.Equal(t, "overload", seen.Limit.Name())
		assert.Empty(t, seen.Denied, name)
	}
}

func TestCalls_ARefusalThatAsksForCreditsIsNoOverloadEvenWithTheHighLoadText(t *testing.T) {
	t.Parallel()

	credits := fixtureLines(t, "rate-limited-credits-required.jsonl")[0]
	line := strings.Replace(credits, `"text":"`, `"text":"The model is experiencing high load. `, 1)
	require.NotEqual(t, credits, line, "the control: the text is the one thing that changes")

	seen := claude.Adapter{}.Calls(line)

	assert.Nil(t, seen.Limit, "no wait cures a refusal that asks for credits")
	assert.Equal(t, deniedCredits, seen.Denied)
}

func TestCalls_AnAssistantUsageLimitReachedLineIsReadLikeARateLimitEvent(t *testing.T) {
	t.Parallel()

	// The line is the usage_limit_reached line of the paused fixture with the pause flag off: the person's own spend cap (org_level_disabled_until).
	pausedLine := fixtureLines(t, "rate-limited-paused.jsonl")[1]
	line := strings.Replace(pausedLine, `"dailyCapPaused":true`, `"dailyCapPaused":false`, 1)
	require.NotEqual(t, pausedLine, line, "the control: the pause flag is the one thing that changes")

	seen := claude.Adapter{}.Calls(line)

	require.NotNil(t, seen.Limit)
	assert.Equal(t, agent.Limit{Kind: agent.LimitSpend, Window: "five_hour", ResetsAt: time.Unix(1795006800, 0).UTC()}, *seen.Limit)
	assert.Empty(t, seen.Denied)
}

func TestCalls_AnAssistantUsageLimitReachedLineWithAnEarlierSpendResetResetsAtTheSpendReset(t *testing.T) {
	t.Parallel()

	line := `{"type":"assistant","message":{"content":[]},"error":"rate_limit","api_error":"usage_limit_reached","api_error_params":{"rate_limit_info":` +
		`{"status":"rejected","resetsAt":1795100000,"overageResetsAt":1795000000,"rateLimitType":"five_hour","overageStatus":"rejected",` +
		`"overageDisabledReason":"org_level_disabled_until","isUsingOverage":false}}}`

	seen := claude.Adapter{}.Calls(line)

	require.NotNil(t, seen.Limit)
	assert.Equal(t, agent.Limit{Kind: agent.LimitSpend, Window: "five_hour", ResetsAt: time.Unix(1795000000, 0).UTC()}, *seen.Limit)
}

func TestCalls_AnAssistantUsageLimitReachedLineOfAPlanLimitKeepsItsWindowAndPlanReset(t *testing.T) {
	t.Parallel()

	line := `{"type":"assistant","message":{"content":[]},"error":"rate_limit","api_error":"usage_limit_reached","api_error_params":{"rate_limit_info":` +
		`{"status":"rejected","resetsAt":1795000000,"overageResetsAt":1795100000,"rateLimitType":"seven_day","overageStatus":"rejected",` +
		`"overageDisabledReason":"overage_not_provisioned","isUsingOverage":false}}}`

	seen := claude.Adapter{}.Calls(line)

	require.NotNil(t, seen.Limit)
	assert.Equal(t, agent.Limit{Kind: agent.LimitRate, Window: "seven_day", ResetsAt: time.Unix(1795000000, 0).UTC()}, *seen.Limit)
}

func TestCalls_AnAssistantLineWithAnotherErrorOrOnARejectedLineOfAnotherTypeIsNoLimit(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, line string }{
		{"another_error", `{"type":"assistant","message":{"content":[]},"error":"server_error"}`},
		{"no_error", `{"type":"assistant","message":{"content":[]}}`},
		{"a_result_line", `{"type":"result","subtype":"success","is_error":true,"error":"rate_limit","rate_limit_info":{"status":"rejected"}}`},
		{"not_an_object", `["rate_limit_event"]`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Nil(t, claude.Adapter{}.Calls(tc.line).Limit)
		})
	}
}
