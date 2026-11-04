package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

const listProject = `repo = "o/n", tracker = "github", forge = "github", beads_prefix = "p", base_branch = "main", test_report = "reports/*.xml"`

func TestLoadFolder_AGateListKeepsTheOrderOfTheDictAndGateSetsNone(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(listProject+`, gate_steps = {"fmt": "make fmt", "lint": "make lint", "test": "make test"}`))

	require.Empty(t, problems)
	require.Empty(t, project.Gate)
	require.Equal(t, []model.GateSpec{{Name: "fmt", Command: "make fmt"}, {Name: "lint", Command: "make lint"}, {Name: "test", Command: "make test"}}, project.GateSteps)
}

func TestLoadFolder_AGateListIsRefusedWithGateAndForABadStep(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		`, gate = "make", gate_steps = {"a": "make a"}`: "both set",
		`, gate_steps = {"tests": "make a"}`:            "takes that name",
		`, gate_steps = {"a b": "make a"}`:              "step name",
		`, gate_steps = {"a": " "}`:                     "blank command",
		`, gate_steps = ["a"]`:                          "want a dict",
		`, gate_steps = {"a": 1}`:                       "want a step name and a command",
		`, gate_steps = {}`:                             "`gate` must be a non-empty string",
	}

	for args, want := range cases {
		_, problems := loadProjectText(t, call(listProject+args))

		require.Len(t, problems, 1, args)
		require.Contains(t, problems[0].Error(), want, args)
	}
}
