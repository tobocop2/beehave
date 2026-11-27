package remediation_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/workflow"
)

func TestCheck_AcceptsTheShippedWorkflowsOnThePathRules(t *testing.T) {
	t.Parallel()

	found, _ := workflow.Check("workflow.star")

	problems := make([]string, len(found))
	for i, d := range found {
		problems[i] = fmt.Sprintf("%d:%d %s", d.Line, d.Col, d.Text)
	}

	assert.Empty(t, problems)
}
