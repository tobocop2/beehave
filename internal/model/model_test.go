package model_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestParseAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		want    model.Agent
		wantErr string
	}{
		{name: "takes_claude", text: "claude", want: model.AgentClaude},
		{name: "takes_opencode", text: "opencode", want: model.AgentOpenCode},
		{name: "rejects_unknown", text: "gpt", wantErr: `unknown agent "gpt": use one of claude, opencode`},
		{name: "rejects_empty", text: "", wantErr: `unknown agent "": use one of claude, opencode`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := model.ParseAgent(tc.text)
			if tc.wantErr != "" {
				require.ErrorIs(t, err, model.ErrUnknownAgent)
				require.EqualError(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseStep_RoundTripsEveryStep(t *testing.T) {
	t.Parallel()

	require.Len(t, model.Steps(), 12)

	for _, step := range model.Steps() {
		got, err := model.ParseStep(string(step))
		require.NoError(t, err)
		require.Equal(t, step, got)
	}
}

func TestParseStep_RejectsUnknown(t *testing.T) {
	t.Parallel()

	_, err := model.ParseStep("deploy")
	require.ErrorIs(t, err, model.ErrUnknownStep)
	require.ErrorContains(t, err, "use one of claim, claim-gate, plan")
}

func TestStep_IsAgentOnlyForTheThreeAgentSteps(t *testing.T) {
	t.Parallel()

	var agents []model.Step

	for _, step := range model.Steps() {
		if step.IsAgent() {
			agents = append(agents, step)
		}
	}

	require.Equal(t, []model.Step{model.StepPlan, model.StepImplement, model.StepReview}, agents)
}

func TestStep_OnlyTheOwnerWaitHasNoDeadline(t *testing.T) {
	t.Parallel()

	for _, step := range model.Steps() {
		require.Equal(t, step != model.StepOwnerWait, step.HasDeadline(), step)
	}
}
