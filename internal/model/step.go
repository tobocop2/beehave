package model

// Step names a claim or release call of the engine around the workflow. The steps of the workflow itself are blocks of the workflow file.
type Step string

// The steps that the engine runs outside the workflow file.
const (
	StepClaim   Step = "claim"
	StepReady   Step = "ready"
	StepRelease Step = "release"
)
