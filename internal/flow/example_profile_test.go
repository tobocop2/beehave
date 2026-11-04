package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

// implementPrompt runs the example with the given project and env additions and returns the text that the implement agent received.
func implementPrompt(t *testing.T, project model.Project, env map[string]string) string {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.facts = func(string) model.Project { return project }

	for key, value := range env {
		rig.h.opts.Env[key] = value
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	for _, request := range rig.h.agents.requests {
		if strings.Contains(request.Prompt, "Acceptance: the script decides") {
			return request.Prompt
		}
	}

	require.FailNow(t, "the run started no implement agent")

	return ""
}

func TestExampleProfile_TheBriefRulesAndTheGateAndTheLimitsOfTheProfileReachTheImplementAgent(t *testing.T) {
	t.Parallel()

	text := implementPrompt(t, model.Project{
		BriefRules: []string{"name the tempting wrong fix"}, Gate: "make check-profile", Limits: []string{"never run the reset command"},
	}, nil)

	require.Contains(t, text, "name the tempting wrong fix")
	require.Contains(t, text, "The gate is `make check-profile`.")
	require.Contains(t, text, "Hard limits, commands never run and real user data:\n\n- never run the reset command")
}

func TestExampleProfile_TheAcceptanceAndBaseResultLinesReachTheImplementAgent(t *testing.T) {
	t.Parallel()

	text := implementPrompt(t, model.Project{}, map[string]string{"ACCEPTANCE": "/work/accept.sh", "BASE_RESULT": "/work/base.out"})

	require.Contains(t, text, "ACCEPTANCE=/work/accept.sh BASE_RESULT=/work/base.out")
	require.Contains(t, text, "Base of this pull request: `integration`")
}

func TestExampleProfile_AnItemWithNoAcceptanceScriptSaysSo(t *testing.T) {
	t.Parallel()

	text := implementPrompt(t, model.Project{}, nil)

	require.Contains(t, text, "The run names no acceptance script")
	require.NotContains(t, text, "ACCEPTANCE=")
}

func TestExampleProfile_AProjectWithABuildHostSendsEveryBuildThereAndNeverToThisMachine(t *testing.T) {
	t.Parallel()

	text := implementPrompt(t, model.Project{BuildHost: "rig-1", Forge: "github"}, nil)

	require.Contains(t, text, "Run every build, test and lint on the build host. Never run one on this machine.")
	require.Contains(t, text, "Build host: `rig-1`.")
	require.Contains(t, text, "Push the branch when the first commit is green")
	require.NotContains(t, text, "has no build host")
}

func TestExampleProfile_AProjectWithNoBuildHostAndNoForgeBuildsHereAndPushesNothing(t *testing.T) {
	t.Parallel()

	text := implementPrompt(t, model.Project{}, nil)

	require.Contains(t, text, "This project has no build host: run builds and tests on this machine.")
	require.Contains(t, text, "This project has no forge: commit on the branch and push nothing.")
	require.NotContains(t, text, "Never run one on this machine")
}
