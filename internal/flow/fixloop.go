// Package flow runs the item workflow: plan, implement, gate, review and fix rounds, with a wait for the owner.
package flow

// Verdict is the result of one review.
type Verdict string

const (
	VerdictApprove Verdict = "approve"
	VerdictChanges Verdict = "changes"
)

// roundAction says what the item does after a review.
type roundAction string

const (
	// actionStop ends the fix loop: the review approved.
	actionStop roundAction = "stop"
	// actionResume runs the next fix round in the agent session of the last round.
	actionResume roundAction = "resume"
	// actionFresh runs the next fix round in a new agent session.
	actionFresh roundAction = "fresh"
	// actionWaitOwner pauses the item until the owner answers.
	actionWaitOwner roundAction = "wait-owner"
)

const (
	freshRound   = 4
	maxFixRounds = 5
)

// nextRound returns the action after a review. fixed counts the fix rounds already run.
// Rounds 1 to 3 resume the session, round 4 starts a fresh agent, round 5 resumes it, and after round 5 the owner decides.
func nextRound(fixed int, verdict Verdict) roundAction {
	switch {
	case verdict == VerdictApprove:
		return actionStop
	case fixed >= maxFixRounds:
		return actionWaitOwner
	case fixed+1 == freshRound:
		return actionFresh
	default:
		return actionResume
	}
}
