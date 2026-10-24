package model

import (
	"errors"
	"fmt"
)

// Step names one node of the item workflow.
type Step string

// The steps of the item workflow, in run order.
const (
	StepClaim     Step = "claim"
	StepClaimGate Step = "claim-gate"
	StepPlan      Step = "plan"
	StepImplement Step = "implement"
	StepGate      Step = "gate"
	StepRound     Step = "round"
	StepReview    Step = "review"
	StepRecord    Step = "record"
	StepOwnerWait Step = "owner-wait"
	StepReady     Step = "ready"
	StepIntegrate Step = "integrate"
	StepRelease   Step = "release"
)

// ErrUnknownStep is returned when text names no known step.
var ErrUnknownStep = errors.New("unknown step")

// Steps lists every step in run order.
func Steps() []Step {
	return []Step{
		StepClaim, StepClaimGate, StepPlan, StepImplement, StepGate, StepRound,
		StepReview, StepRecord, StepOwnerWait, StepReady, StepIntegrate, StepRelease,
	}
}

// ParseStep returns the step that text names.
func ParseStep(text string) (Step, error) {
	for _, step := range Steps() {
		if string(step) == text {
			return step, nil
		}
	}

	return "", fmt.Errorf("%w %q: use one of %s", ErrUnknownStep, text, joinNames(Steps()))
}

// IsAgent reports whether the step starts an agent process.
func (s Step) IsAgent() bool {
	return s == StepPlan || s == StepImplement || s == StepReview
}

// HasDeadline reports whether the step runs under a time limit. The wait for the owner has none.
func (s Step) HasDeadline() bool {
	return s != StepOwnerWait
}
