package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/workflow"
)

func TestLaneKind_ReviewKindWinsThenTheLaneThenFix(t *testing.T) {
	t.Parallel()

	assert.Equal(t, workflow.LaneReview, workflow.LaneKind(workflow.AgentKindReview, ""))
	assert.Equal(t, workflow.LaneReview, workflow.LaneKind(workflow.AgentKindReview, workflow.LaneFix))
	assert.Equal(t, workflow.LaneFix, workflow.LaneKind(workflow.AgentKindImplement, ""))
	assert.Equal(t, workflow.LaneRework, workflow.LaneKind(workflow.AgentKindImplement, workflow.LaneRework))
}
