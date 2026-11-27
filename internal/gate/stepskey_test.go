package gate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/model"
)

func TestStepsKey_AListWithNoTimeoutKeepsTheExactTextItHadBeforeStepsHadTimeouts(t *testing.T) {
	t.Parallel()

	steps := []model.GateSpec{{Name: "a", Command: "make"}, {Name: "b", Command: "make b"}}

	assert.Equal(t, `[{"a" "make"} {"b" "make b"}]`, stepsKey(steps))
	assert.Equal(t, "[]", stepsKey(nil))
}

func TestStepsKey_AStepWithATimeoutAddsItToTheText(t *testing.T) {
	t.Parallel()

	steps := []model.GateSpec{{Name: "a", Command: "make", Timeout: 90 * time.Second}, {Name: "b", Command: "make b"}}

	assert.Equal(t, `[{"a" "make" "1m30s"} {"b" "make b"}]`, stepsKey(steps))
}
