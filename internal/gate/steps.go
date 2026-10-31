package gate

// Status is the result of one gate step. A step that gave no result line is unknown, never a pass.
type Status string

// The three results of a gate step.
const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusUnknown Status = "unknown"
)

// The names of the steps that a gate gives.
const (
	// StepCommand is the gate command. Its result is its exit code, or the state of its job on a build host.
	StepCommand = "command"
	// StepTests is the test report of a project that sets test_report. Its result is read from the report files.
	StepTests = "tests"
)

// Step is one step of a gate and its result.
type Step struct {
	Name   string
	Status Status
}
