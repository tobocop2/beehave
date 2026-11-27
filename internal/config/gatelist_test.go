package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

const listProject = `repo = "o/n", tracker = "github", forge = "github", base_branch = "main", test_report = "reports/*.xml"`

func TestLoadFolder_AGateListKeepsTheOrderOfTheDictAndGateSetsNone(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(listProject+`, gate_steps = {"fmt": "make fmt", "lint": "make lint", "test": "make test"}`))

	require.Empty(t, problems)
	require.Empty(t, project.Gate)
	require.Equal(t, []model.GateSpec{{Name: "fmt", Command: "make fmt"}, {Name: "lint", Command: "make lint"}, {Name: "test", Command: "make test"}}, project.GateSteps)
}

func TestLoadFolder_AGateStepTakesItsOwnTimeout(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(listProject+`, gate_steps = {"build": {"command": "make build", "timeout": "10m"}, "lint": {"command": "make lint"}, "test": "make test"}`))

	require.Empty(t, problems)
	require.Equal(t, []model.GateSpec{{Name: "build", Command: "make build", Timeout: 10 * time.Minute}, {Name: "lint", Command: "make lint"}, {Name: "test", Command: "make test"}}, project.GateSteps)
}

func TestLoadFolder_AGateListIsRefusedWithGateAndForABadStep(t *testing.T) {
	t.Parallel()

	cases := map[string]string{ //nolint:gosec // step names, not credentials
		`, gate = "make", gate_steps = {"a": "make a"}`:              "both set",
		`, gate_steps = {"tests": "make a"}`:                         "takes that name",
		`, gate_steps = {"a b": "make a"}`:                           "step name",
		`, gate_steps = {"head-gate": "make a"}`:                     `"head-gate". The gate job of a candidate head takes that name. Rename the step.`,
		`, gate_steps = {"secret-scan": "make a"}`:                   `"secret-scan". The gate job`,
		`, gate_steps = {"gate-program": "make a"}`:                  `"gate-program". The gate job`,
		`, gate_steps = {"base-suite": "make a"}`:                    `"base-suite". The gate job`,
		`, gate_steps = {"a": " "}`:                                  "blank command",
		`, gate_steps = ["a"]`:                                       "want a dict",
		`, gate_steps = {"a": 1}`:                                    "want a command or a dict",
		`, gate_steps = {1: "make a"}`:                               "want a step name",
		`, gate_steps = {"a": {"timeout": "1m"}}`:                    "needs a `command`",
		`, gate_steps = {"a": {"command": "x", "t": 1}}`:             "want only `command` and `timeout`",
		`, gate_steps = {"a": {"command": "x", "timeout": 5}}`:       "want a duration string",
		`, gate_steps = {"a": {"command": "x", "timeout": "soon"}}`:  "at least 1s",
		`, gate_steps = {"a": {"command": "x", "timeout": "500ms"}}`: "at least 1s",
		`, gate_steps = {}`:                                          "`gate` must be a non-empty string",
	}

	for args, want := range cases {
		_, problems := loadProjectText(t, call(listProject+args))

		require.Len(t, problems, 1, args)
		require.Contains(t, problems[0].Error(), want, args)
	}
}
