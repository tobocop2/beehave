package claude_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/agent/claude"
)

const reviewSkillFolder = "/home/o/login/skills/review"

func TestLaunch_AStepWithNoSandboxGetsThePluginFolderOfItsSkills(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Guard("", "/run", nil, []string{reviewSkillFolder})

	assert.Equal(t, []string{"--add-dir", "/run/skills-plugin", "--plugin-dir", "/run/skills-plugin"}, launch.Args[:4], "the plugin folder is a working directory, so the Read tool reads the files of a skill")
	assert.Equal(t, map[string]string{"skills-plugin/skills/review": reviewSkillFolder}, launch.Copies)
	assert.Contains(t, launch.Files["skills-plugin/.claude-plugin/plugin.json"], `"name":"beehave-skills"`)
}

func TestLaunch_AGuardedStepWithOutputFileAndSkillsKeepsTheHookTheRulesAndThePlugin(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Guard("/bin/beehave check command", "/run", []string{outputFile}, []string{reviewSkillFolder})
	flags, settings := settingsOf(t, launch.Args)

	assert.Equal(t, []string{"--add-dir", "/state/outputs/7.review", "/run/skills-plugin", "--plugin-dir", "/run/skills-plugin"}, flags)
	assert.Contains(t, settings, "hooks")
	assert.Contains(t, settings, "permissions")
	assert.Len(t, launch.Copies, 1)
}

func TestLaunch_APlainStepWithNoSkillsGetsNoPluginFolder(t *testing.T) {
	t.Parallel()

	launch := claude.New(nil).Guard("g", "/run", nil, nil)

	assert.NotContains(t, launch.Args, "--plugin-dir")
	assert.Empty(t, launch.Copies)
	assert.Empty(t, launch.Files["skills-plugin/.claude-plugin/plugin.json"])
}
