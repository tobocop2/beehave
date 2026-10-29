package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// followUpFlow is a review call that declares a verdict and shows the verdict that the call returned.
func followUpFlow(option string) string {
	return bodyFlow("a = " + agentCallText(`outputs = ["verdict"], `+option) + `command(run = ["show", a.verdict])` + "\n")
}

func TestAgentStep_ForbiddenFollowUpsTurnAnApproveWithAnOpenFindingIntoChanges(t *testing.T) {
	t.Parallel()

	for class, want := range map[string]string{"minor": "changes", "deferred": "changes", "pre-existing": "changes", "blocking": "changes"} {
		rig := newAgentRig(t)
		rig.reply(`{"verdict":"approve","findings":[{"class":"`+class+`","text":"x"}]}`, exited)

		_, err := rig.run(t, followUpFlow(`follow_ups = "forbid", `), "Write to {{ .output_file }}\n")

		require.NoError(t, err, class)
		assert.Equal(t, []string{"show " + want}, rig.h.commands.ran(), class)
	}
}

func TestAgentStep_ForbiddenFollowUpsKeepAnApproveWhenNoFindingIsOpen(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"approve","findings":[]}`, exited)

	_, err := rig.run(t, followUpFlow(`follow_ups = "forbid", `), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"show approve"}, rig.h.commands.ran())
}

func TestAgentStep_AllowedFollowUpsKeepTheVerdictOfTheAgentWhateverTheFindings(t *testing.T) {
	t.Parallel()

	for _, option := range []string{"", `follow_ups = "allow", `} {
		rig := newAgentRig(t)
		rig.reply(`{"verdict":"approve","findings":[{"class":"minor","text":"x"}]}`, exited)

		_, err := rig.run(t, followUpFlow(option), "Write to {{ .output_file }}\n")

		require.NoError(t, err, option)
		assert.Equal(t, []string{"show approve"}, rig.h.commands.ran(), option)
	}
}

func TestAgentStep_ForbiddenFollowUpsKeepAVerdictThatIsNotApprove(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"changes","findings":[]}`, exited)

	_, err := rig.run(t, followUpFlow(`follow_ups = "forbid", `), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"show changes"}, rig.h.commands.ran())
}

func TestAgentStep_ForbiddenFollowUpsNeedFindingsOfAKnownClass(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]string{
		`{"verdict":"approve"}`:                                         `the agent did not write the output "findings"`,
		`{"verdict":"approve","findings":"x"}`:                          `the output "findings" must be a list`,
		`{"verdict":"approve","findings":[3]}`:                          `each finding must be an object`,
		`{"verdict":"approve","findings":[{"class":"odd","text":"x"}]}`: `the finding class "odd" is not one of`,
		`{"verdict":"approve","findings":[{"class":3,"text":"x"}]}`:     `the finding class "3" is not one of`,
	} {
		rig := newAgentRig(t)
		rig.reply(body, exited)

		_, err := rig.run(t, followUpFlow(`follow_ups = "forbid", `), "Write to {{ .output_file }}\n")

		require.ErrorContains(t, err, want, body)
	}
}

func TestAgentStep_ABadFollowUpsValueIsRefusedBeforeTheAgentRuns(t *testing.T) {
	t.Parallel()

	for _, value := range []string{`"maybe"`, `3`, `""`} {
		rig := newAgentRig(t)

		_, err := rig.run(t, followUpFlow(`follow_ups = `+value+`, `), "x")

		require.ErrorContains(t, err, "follow_ups", value)
		assert.Empty(t, rig.h.agents.seen(), value)
	}
}

func TestAgentStep_ABadFollowUpsValueThatTheCheckCannotSeeIsRefusedWhenTheCallRuns(t *testing.T) {
	t.Parallel()

	for _, value := range []string{`"maybe"`, `""`} {
		rig := newAgentRig(t)
		flow := bodyFlow("mode = " + value + "\na = " + agentCallText(`outputs = ["verdict"], follow_ups = mode, `))

		_, err := rig.run(t, flow, "x")

		require.ErrorContains(t, err, "follow_ups is "+value+". Use one of: allow, forbid", value)
		assert.Empty(t, rig.h.agents.seen(), value)
	}
}

func TestAgentStep_ForbiddenFollowUpsNeedTheVerdictInTheDeclaredOutputs(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	flow := bodyFlow("mode = \"forbid\"\na = " + agentCallText(`follow_ups = mode, `))

	_, err := rig.run(t, flow, "x")

	require.ErrorContains(t, err, `follow_ups is "forbid", so outputs must list "verdict"`)
	assert.Empty(t, rig.h.agents.seen())
}
