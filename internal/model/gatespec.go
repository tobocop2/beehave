package model

import "time"

// The names of the steps that the gate job of a candidate head runs apart. A gate list may not use them: the log of the job names a step by its name.
const (
	StepHeadGate  = "head-gate"
	StepSecrets   = "secret-scan"
	StepBaseSuite = "base-suite"
	StepRevertRun = "revert-run"
	// StepGateProgram is the step that the engine adds when the gate refuses a head that changed the gate program.
	StepGateProgram = "gate-program"
)

// RevertVerdict is what the revert run says of the new tests of a candidate: pass when each one fails or does not build on the base code.
type RevertVerdict string

// The verdicts of the revert run. Fail also covers a run that proves nothing: no new test, a cut log or a revert report with no test case.
const (
	RevertPass RevertVerdict = "pass"
	RevertFail RevertVerdict = "fail"
)

// ReservedGateSteps are the step names that the gate job of a candidate head takes.
func ReservedGateSteps() []string {
	return []string{StepHeadGate, StepSecrets, StepBaseSuite, StepRevertRun, StepGateProgram}
}

// GateSpec is one step of a gate list: a name, the command line that runs it, and its own time limit (zero for none: only the limit of the whole gate applies).
type GateSpec struct {
	Name    string
	Command string
	Timeout time.Duration
}

// TimeoutSeconds is Timeout in whole seconds, rounded up, so the limit that a wrap writes is never shorter than Timeout.
func (s GateSpec) TimeoutSeconds() int64 {
	seconds := int64(s.Timeout / time.Second)
	if s.Timeout%time.Second > 0 {
		seconds++
	}

	return seconds
}
