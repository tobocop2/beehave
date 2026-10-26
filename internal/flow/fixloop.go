// Package flow runs the item workflow: plan, implement, gate, review and fix rounds, with a wait for the owner.
package flow

import (
	"errors"
	"fmt"
)

// Verdict is the result of one review.
type Verdict string

const (
	VerdictApprove Verdict = "approve"
	VerdictChanges Verdict = "changes"
)

// ErrUnknownVerdict is returned when text names no known verdict.
var ErrUnknownVerdict = errors.New("unknown verdict")

// ParseVerdict returns the verdict that text names.
func ParseVerdict(text string) (Verdict, error) {
	for _, verdict := range []Verdict{VerdictApprove, VerdictChanges} {
		if string(verdict) == text {
			return verdict, nil
		}
	}

	return "", fmt.Errorf("%w %q: use %s or %s", ErrUnknownVerdict, text, VerdictApprove, VerdictChanges)
}

// Action says what the item does after a review.
type Action string

const (
	// ActionStop ends the fix loop: the review approved.
	ActionStop Action = "stop"
	// ActionResume runs the next fix round in the agent session of the last round.
	ActionResume Action = "resume"
	// ActionFresh runs the next fix round in a new agent session.
	ActionFresh Action = "fresh"
	// ActionWaitOwner pauses the item until the owner answers.
	ActionWaitOwner Action = "wait-owner"
)

const (
	freshRound   = 4
	maxFixRounds = 5
)

// NextRound returns the action after a review. fixed counts the fix rounds already run.
// Rounds 1 to 3 resume the session, round 4 starts a fresh agent, round 5 resumes it, and after round 5 the owner decides.
func NextRound(fixed int, verdict Verdict) Action {
	switch {
	case verdict == VerdictApprove:
		return ActionStop
	case fixed >= maxFixRounds:
		return ActionWaitOwner
	case fixed+1 == freshRound:
		return ActionFresh
	default:
		return ActionResume
	}
}
