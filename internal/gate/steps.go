package gate

import (
	"slices"

	"github.com/tobocop2/obee/internal/model"
)

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

// Plan is the part of a project that fixes what a gate runs and which steps it reports: the gate string, the gate list and the test report.
// Submit reads it once, when the job text is made. A caller records it with the job and passes it to Run, so a run that starts again
// judges the job that it submitted and never the list of the file now.
type Plan struct {
	Gate       string           `json:"gate,omitempty"`
	Steps      []model.GateSpec `json:"steps,omitempty"`
	TestReport string           `json:"test_report,omitempty"`
}

// PlanOf is the plan of the project as it is now.
func PlanOf(project model.Project) Plan {
	return Plan{Gate: project.Gate, Steps: slices.Clone(project.GateSteps), TestReport: project.TestReport}
}

// Required is the names of the steps that a merge needs when the plan holds a gate list: each spec, then the tests step. A gate string has no list, and asks for none.
func (p Plan) Required() []string {
	if len(p.Steps) == 0 {
		return nil
	}

	return StepNames(p.apply(model.Project{}))
}

// Empty is true for a plan that holds nothing: the plan of a job that no Submit made.
func (p Plan) Empty() bool {
	return p.Gate == "" && len(p.Steps) == 0 && p.TestReport == ""
}

// apply gives the project the gate, the list and the test report of the plan. A plan that holds none of them (a job recorded before plans) leaves the project as it is.
func (p Plan) apply(project model.Project) model.Project {
	if p.Empty() {
		return project
	}

	project.Gate, project.GateSteps, project.TestReport = p.Gate, slices.Clone(p.Steps), p.TestReport

	return project
}

// Specs is the gate list of the project: its gate_steps in order, or the one command of its gate string.
func Specs(project model.Project) []model.GateSpec {
	if len(project.GateSteps) > 0 {
		return project.GateSteps
	}

	return []model.GateSpec{{Name: StepCommand, Command: project.Gate}}
}

// StepNames is the name of every step that a gate of the project gives: each spec, then the tests step when the project sets test_report.
// A merge needs a pass for each of them.
func StepNames(project model.Project) []string {
	var names []string

	for _, spec := range Specs(project) {
		names = append(names, spec.Name)
	}

	if project.TestReport != "" {
		names = append(names, StepTests)
	}

	return names
}

// Step is one step of a gate and its result.
type Step struct {
	Name   string
	Status Status
}
