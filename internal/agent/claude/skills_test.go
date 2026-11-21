package claude_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
)

// toolResult is a user line that holds the result of one tool call.
func toolResult(t *testing.T, id string, isError bool, text string) string {
	t.Helper()

	block := map[string]any{"type": "tool_result", "tool_use_id": id, "content": text}
	if isError {
		block["is_error"] = true
	}

	return toJSON(t, map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{block}}})
}

func skillCall(t *testing.T, id, name string) string {
	t.Helper()

	return assistant(t, map[string]any{"type": "tool_use", "id": id, "name": "Skill", "input": map[string]any{"skill": name}})
}

func TestCalls_ASkillCallKeepsItsIDSoTheCallerCanPairItWithItsResult(t *testing.T) {
	t.Parallel()

	seen := claude.New(nil).Calls(skillCall(t, "toolu_1", "review"))

	assert.Equal(t, []agent.Call{{ID: "toolu_1", Text: "review"}}, seen.Skills)
}

func TestCalls_OnlyAToolResultWithNoErrorSucceeds(t *testing.T) {
	t.Parallel()

	adapter := claude.New(nil)

	assert.Equal(t, []string{"toolu_1"}, adapter.Calls(toolResult(t, "toolu_1", false, "Launching skill: review")).Succeeded)
	assert.Empty(t, adapter.Calls(toolResult(t, "toolu_2", true, "<tool_use_error>Unknown skill: review</tool_use_error>")).Succeeded,
		"an Unknown skill answer is an error result, so the skill did not load")
	assert.Empty(t, adapter.Calls(toolResult(t, "", false, "ok")).Succeeded, "a result with no id pairs with no call")
	assert.Empty(t, adapter.Calls(assistant(t, map[string]any{"type": "tool_result", "tool_use_id": "toolu_3"})).Succeeded,
		"a result counts only on a user line")
}

func TestCalls_TheSkillOfThePluginOfTheEngineIsRecordedByItsPlainName(t *testing.T) {
	t.Parallel()

	adapter := claude.New(nil)

	for line, want := range map[string]string{
		"beehave-skills:review":  "review",
		"review":                 "review",
		"other-plugin:review":    "other-plugin:review",
		"xbeehave-skills:review": "xbeehave-skills:review",
		"beehave-skills-review":  "beehave-skills-review",
	} {
		seen := adapter.Calls(skillCall(t, "toolu_1", line))
		require.Len(t, seen.Skills, 1, line)
		assert.Equal(t, want, seen.Skills[0].Text, "only the plugin that the engine builds loses its prefix: %s", line)
	}
}

func TestSandbox_ARunWithSkillsPassesAPluginFolderThatHoldsOnlyThoseSkills(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Sandbox(plan(), "", "/run", nil, []string{"/home/o/login/skills/review", "/home/o/login/skills/oracle"})
	flags, settings := settingsOf(t, launch.Args)

	assert.Equal(t, []string{"--setting-sources", "", "--strict-mcp-config", "--disallowedTools", "WebFetch", "WebSearch", "--add-dir", "/run/skills-plugin", "--plugin-dir", "/run/skills-plugin"}, flags,
		"the setting sources stay empty: the plugin folder is the only source of skills, and a working directory, so the Read tool and the shell read its files")
	assert.Equal(t, map[string]string{
		"skills-plugin/skills/review": "/home/o/login/skills/review",
		"skills-plugin/skills/oracle": "/home/o/login/skills/oracle",
	}, launch.Copies)

	var manifest struct {
		Name string `json:"name"`
	}

	require.NoError(t, json.Unmarshal([]byte(launch.Files["skills-plugin/.claude-plugin/plugin.json"]), &manifest))
	assert.Equal(t, "beehave-skills", manifest.Name)

	box, isBox := settings["sandbox"].(map[string]any) // the settings decode as untyped JSON
	require.True(t, isBox)
	files, isFiles := box["filesystem"].(map[string]any) // the settings decode as untyped JSON
	require.True(t, isFiles)
	assert.Contains(t, files["allowRead"], "/run/skills-plugin", "the shell reads the files of the skills")
	assert.Contains(t, files["denyWrite"], "/run/skills-plugin", "the agent cannot change a skill")
	assert.Contains(t, files["denyRead"], "/home/o/.ssh")

	rules, isRules := settings["permissions"].(map[string]any) // the settings decode as untyped JSON
	require.True(t, isRules)
	assert.Contains(t, rules["deny"], "Edit(//run/skills-plugin/**)")
}

func TestSandbox_ARunWithNoSkillsPassesNoPluginFolder(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Sandbox(plan(), "", "/run", nil, nil)

	assert.NotContains(t, launch.Args, "--plugin-dir")
	assert.Empty(t, launch.Copies)
	assert.NotContains(t, launch.Files, "skills-plugin/.claude-plugin/plugin.json")
}
