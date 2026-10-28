package workflow_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"

	"github.com/tobocop2/obee/internal/workflow"
)

func loadFirst(t *testing.T, text string) workflow.Step {
	t.Helper()

	loaded, err := workflow.Load(fstest.MapFS{"w.hcl": {Data: []byte(text)}}, "w.hcl")
	require.NoError(t, err)

	return loaded.Steps[0]
}

func TestStepOutputs_AnAgentListsItsKindOutputsAndItsDeclaredOnes(t *testing.T) {
	t.Parallel()

	step := loadFirst(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"p.md\"\n  outputs = [\"verdict\"]\n  output_tries = 2\n}\n")

	got := map[string]cty.Type{}
	for _, o := range workflow.StepOutputs(step) {
		got[o.Name] = o.Type
	}

	assert.Equal(t, cty.String, got["verdict"])
	assert.Equal(t, cty.String, got["session"])
	assert.Equal(t, cty.String, got["violations"])
	assert.Len(t, got, 6)
}

func TestStepOutputs_AnOpenListOfDeclaredOutputsAddsNone(t *testing.T) {
	t.Parallel()

	step := loadFirst(t, "workflow \"w\" {}\nagent \"a\" {\n  prompt = \"p.md\"\n  outputs = var.names\n}\n")

	assert.Len(t, workflow.StepOutputs(step), 5)
}
