package model_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestGateSpec_TimeoutSecondsRoundsAPartOfASecondUp(t *testing.T) {
	t.Parallel()

	assert.EqualValues(t, 2, model.GateSpec{Timeout: 1500 * time.Millisecond}.TimeoutSeconds())
	assert.EqualValues(t, 3, model.GateSpec{Timeout: 3 * time.Second}.TimeoutSeconds())
}

func TestReservedGateSteps_NamesTheStepsOfACandidateJobAndTheGateProgramStep(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"head-gate", "secret-scan", "base-suite", "revert-run", "gate-program"}, model.ReservedGateSteps())
}
