package flow

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

const skillPrompt = "Write to {{ .output_file }}\nRun {{ skill \"review\" }}, then {{ skill \"oracle\" }}, then {{ skill \"review\" }} again.\n"

func TestAgentStep_TheSkillsThatThePromptDeclaresReachTheAgentRequest(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(""), skillPrompt)

	require.NoError(t, err)
	require.Len(t, rig.h.agents.seen(), 1)

	request := rig.h.agents.seen()[0]

	assert.Equal(t, []string{"review", "oracle"}, request.Skills, "each declared name once, in the order of the prompt")
	assert.Contains(t, request.Prompt, "Run review, then oracle, then review again.", "the function renders the plain name")
	assert.Nil(t, request.Sandbox, "the skills do not turn the sandbox on")
}

func TestAgentStep_ASandboxedStepGetsTheSkillsThatThePromptDeclares(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`sandbox = sandbox(network = ["api.example.org"]), `), skillPrompt)

	require.NoError(t, err)
	require.Len(t, rig.h.agents.seen(), 1)
	assert.Equal(t, []string{"review", "oracle"}, rig.h.agents.seen()[0].Skills)
	assert.Equal(t, &model.Sandbox{Network: []string{"api.example.org"}}, sandboxSeen(t, rig))
}

func TestAgentStep_APromptThatDeclaresNoSkillGivesTheAgentNoSkill(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(""), "Write to {{ .output_file }}\n")

	require.NoError(t, err)
	require.Len(t, rig.h.agents.seen(), 1)
	assert.Empty(t, rig.h.agents.seen()[0].Skills)
}

func TestAgentStep_ASkillNameThatIsNoFolderNameStopsTheStepBeforeTheAgentStarts(t *testing.T) {
	t.Parallel()

	for call, want := range map[string]string{
		`{{ skill "a/b" }}`:  "is not the name of a skill",
		`{{ skill ".." }}`:   "is not the name of a skill",
		`{{ skill "" }}`:     "is not the name of a skill",
		`{{ skill "a\\b" }}`: "is not the name of a skill",
	} {
		t.Run(call, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)

			_, err := rig.run(t, agentFlow(""), "Write to {{ .output_file }}\n"+call)

			require.ErrorContains(t, err, want)
			assert.Empty(t, rig.h.agents.seen())
		})
	}
}

func TestRecalledSkills_ReadBackTheSkillsThatRecordSkillsWrote(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(recordSkills([]string{"review", "oracle"}))
	require.NoError(t, err)

	var round map[string]any
	require.NoError(t, json.Unmarshal(raw, &round))

	got, err := recalledSkills(round)
	require.NoError(t, err)
	assert.Equal(t, []string{"review", "oracle"}, got)
}

func TestRecalledSkills_ARecordWithNoSkillsReplaysWithNone(t *testing.T) {
	t.Parallel()

	got, err := recalledSkills(nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, recordSkills(nil), "a step with no skill records nothing")

	_, err = recalledSkills(map[string]any{recordedSkills: "{"})
	require.ErrorContains(t, err, "read the recorded skills of the step")
}

func TestRecall_ACorruptSkillsOrSandboxRecordStopsTheStepAtTheCallSite(t *testing.T) {
	t.Parallel()

	for _, record := range []map[string]any{{recordedSkills: "{"}, {recordedSandbox: "{not json"}} {
		a := agentArgs{file: filepath.Join(t.TempDir(), "outputs", "out.json")}

		require.Error(t, a.recall(record))
	}

	ok := agentArgs{file: filepath.Join(t.TempDir(), "outputs", "out.json")}

	require.NoError(t, ok.recall(map[string]any{recordedSkills: `["review"]`}))
	assert.Equal(t, []string{"review"}, ok.skills)
}

func TestRecallLimits_ARecordThatHoldsASandboxOrSkillsThatCannotBeReadStopsTheReplay(t *testing.T) {
	t.Parallel()

	var a agentArgs

	require.ErrorContains(t, a.recallLimits(map[string]any{recordedSandbox: "{not json"}), "read the recorded sandbox of the step")
	require.ErrorContains(t, a.recallLimits(map[string]any{recordedSkills: "{"}), "read the recorded skills of the step")

	require.NoError(t, a.recallLimits(map[string]any{recordedSandbox: `{"network":["x.org"]}`, recordedSkills: `["review"]`}))
	assert.Equal(t, []string{"x.org"}, a.box.Network)
}

func TestAgentStep_ASkillStepStartsItsPromptWithTheSkillLineOfTheAdapter(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.skillLine = func(login, skill string) string { return "/line-of-" + login + ":" + skill }
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`skill = "sweep-skill", `), skillPrompt)

	require.NoError(t, err)
	require.Len(t, rig.h.agents.seen(), 1)

	request := rig.h.agents.seen()[0]

	assert.True(t, strings.HasPrefix(request.Prompt, "/line-of-"+testLogin+":sweep-skill\n"), "the line of the adapter is the first line of the prompt: %q", request.Prompt)
	assert.Contains(t, request.Prompt, "Run review, then oracle", "the prompt follows the line")
	assert.Equal(t, []string{"review", "oracle", "sweep-skill"}, request.Skills, "the skills of the step are the declared ones and then the skill keyword")
}

func TestAgentStep_ASkillStepOnAnAdapterWithNoSkillLineDoesNotStart(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.skillLine = func(string, string) string { return "" }
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`skill = "sweep-skill", `), skillPrompt)

	require.ErrorContains(t, err, "runner "+testLogin, "the refusal names the runner")
	require.ErrorContains(t, err, "sweep-skill")
	assert.Empty(t, rig.h.agents.seen(), "no agent started")
}

func TestAgentStep_ASkillKeywordThatIsNoFolderNameStopsTheStepAndAnEmptyOneIsNoSkill(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`skill = "a/b", `), skillPrompt)

	require.ErrorContains(t, err, "is not the name of a skill")
	assert.Empty(t, rig.h.agents.seen())

	rig = newAgentRig(t)
	rig.reply("", exited)

	_, err = rig.run(t, agentFlow(`skill = "", `), skillPrompt)

	require.NoError(t, err)
	require.Len(t, rig.h.agents.seen(), 1)
	assert.False(t, strings.HasPrefix(rig.h.agents.seen()[0].Prompt, "/"), "an empty skill puts no line in the prompt")
	assert.Equal(t, []string{"review", "oracle"}, rig.h.agents.seen()[0].Skills)
}

func TestAgentStep_ASkillThatThePromptAlreadyDeclaresIsGivenOnce(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`skill = "oracle", `), skillPrompt)

	require.NoError(t, err)
	require.Len(t, rig.h.agents.seen(), 1)
	assert.Equal(t, []string{"review", "oracle"}, rig.h.agents.seen()[0].Skills)
}

func TestAgentStep_ASkillStepOnAnEngineWithNoSkillLineDoesNotStart(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.noSkillLine = true
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`skill = "sweep-skill", `), skillPrompt)

	require.ErrorContains(t, err, "has no line that loads a skill before the first turn")
	assert.Empty(t, rig.h.agents.seen(), "no agent started")
}
