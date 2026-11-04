package flow

import (
	"errors"
	"fmt"
	"slices"

	"github.com/tobocop2/obee/internal/workflow"
)

// stepKinds is the table of every kind of counted step that a run records: the start of the name `<kind>:<label>#<n>` of each step.
// script.name is the one place that names such a step, and it refuses a kind that is not in this table. The table is the list of builtins of
// the workflow package, less the builtins that record no counted step, plus the kinds of the steps that the engine records for itself.
// A test holds the table under the version of the steps (stepsVersion), so a rename, an addition or a removal of a kind fails that test
// until the version changes with it.
func stepKinds() []string {
	own := []string{loginStepName, scopeStep, scopeCheckStep}
	// batch_merge names its steps batch:<name>.<kind>#<item>. json, sandbox and struct record no step.
	none := []string{"batch_merge", "json", recordedSandbox, "struct"}

	var kinds []string

	for _, name := range workflow.Builtins() {
		if !slices.Contains(none, name) {
			kinds = append(kinds, name)
		}
	}

	kinds = append(kinds, own...)
	slices.Sort(kinds)

	return kinds
}

var errUnknownStepKind = errors.New("the step kind is not in the table of step kinds")

// checkStepKind refuses a step named with a kind that the table does not list. The fault is in the build, not in a workflow:
// a new builtin must add its kind to the table, and so to the golden data of the steps version, before it records a step.
func checkStepKind(kinds []string, kind string) error {
	if !slices.Contains(kinds, kind) {
		return fmt.Errorf("%w: %q. Add it to the table and bump stepsVersion", errUnknownStepKind, kind)
	}

	return nil
}
