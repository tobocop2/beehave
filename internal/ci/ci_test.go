package ci_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/ci"
)

func TestNewRefusal_EachProblemIsOneFailLineAndTheLinesStay(t *testing.T) {
	t.Parallel()

	refusal := ci.NewRefusal([]string{"a.yaml: not a workflow file", "b.yaml: not a workflow file"}, []string{"step x: make"})

	assert.Equal(t, "FAIL: a.yaml: not a workflow file\nFAIL: b.yaml: not a workflow file", refusal.Error())
	assert.Equal(t, []string{"step x: make"}, refusal.Lines)
}
