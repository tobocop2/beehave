package ci_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ci"
)

func TestHold_AMapThatNamesEachDerivedCommandToGateStepsHasNoDefect(t *testing.T) {
	t.Parallel()

	steps := []ci.Step{{Name: "a:vet", Command: "make vet"}, {Name: "a:tools", Command: "go install x@v1"}, {Name: "b:vet", Command: "make vet"}}

	assert.Empty(t, ci.Hold(steps, []string{"vet"}, ci.Map{"make vet": {"vet"}, "go install x@v1": {}}))
}

func TestHold_EachDefectIsOneLineInTheOrderUnmappedStaleUnknown(t *testing.T) {
	t.Parallel()

	steps := []ci.Step{{Name: "a:test", Command: "make test-real-tools"}, {Name: "a:vet", Command: "make vet"}}
	held := ci.Map{"make test": {"test"}, "make vet": {"vet", "lint"}, "make lint": {"lint", "test"}}

	assert.Equal(t, []string{
		"unmapped a:test: make test-real-tools",
		"stale make lint",
		"stale make test",
		"unknown-step lint",
	}, ci.Hold(steps, []string{"test", "vet"}, held))
}

func TestHold_AKeyMatchesOnlyTheExactCommandText(t *testing.T) {
	t.Parallel()

	steps := []ci.Step{{Name: "a:gov", Command: "make governance-verify"}}

	for name, key := range map[string]string{
		"a trailing space": "make governance-verify ",
		"a prefix":         "make governance",
		"a longer command": "make governance-verify all",
		"another case":     "make Governance-verify",
	} {
		assert.Contains(t, ci.Hold(steps, []string{"g"}, ci.Map{key: {"g"}}), "unmapped a:gov: make governance-verify", name)
	}
}

func TestParseMap_ReadsAnObjectOfCommandsToListsOfGateStepNames(t *testing.T) {
	t.Parallel()

	held, err := ci.ParseMap(`{"make test": ["test"], "go install x@v1": []}`)

	require.NoError(t, err)
	assert.Equal(t, ci.Map{"make test": {"test"}, "go install x@v1": {}}, held)
}

func TestParseMap_RefusesAKeyTwiceANullListABlankNameAndAnyOtherForm(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"a key twice":           `{"make test": ["nope"], "make test": ["test"]}`,
		"a null list":           `{"make test": null}`,
		"a null name":           `{"make test": [null]}`,
		"a blank name":          `{"make test": [" "]}`,
		"a blank key":           `{" ": ["test"]}`,
		"a text as the list":    `{"make test": "test"}`,
		"a number as a name":    `{"make test": [1]}`,
		"a list":                `["make test"]`,
		"a key that is no text": `{1: []}`,
		"an object not closed":  `{"make test": []`,
		"an object closed by ]": `{"make test": []]`,
		"a second object":       `{} {}`,
		"no text":               ``,
	} {
		_, err := ci.ParseMap(text)

		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "the gate_map must be one JSON object", name)
	}
}
