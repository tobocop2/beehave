package flow

import (
	"encoding/json"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// reusableRuns is the most gate runs that earlierPass reads, newest first.
const reusableRuns = 200

// earlier is a gate run that passed on every step, and the steps it recorded.
type earlier struct {
	run   string
	steps []GateStep
}

// earlierPass finds the newest finished gate run whose key is the key. The runs of the exclusive queue are the store: each finished gate keeps its key in its output
// only when it passed on every step, so a red or a void gate is never found. An empty key finds nothing.
// A run that cannot be read is skipped, and a failed read finds nothing: the gate then runs, which is the safe side.
func (e *Engine) earlierPass(key GateKey, project string) (earlier, bool) {
	if key.ID == "" {
		return earlier{}, false
	}

	rows, err := dbos.ListWorkflows(dbos.WithoutCancel(e.ctx), dbos.WithFilterName(exclusiveName), dbos.WithFilterStatus(dbos.WorkflowStatusSuccess),
		dbos.WithFilterLoadOutput(true), dbos.WithFilterSortDesc(), dbos.WithFilterLimit(reusableRuns))
	if err != nil {
		return earlier{}, false
	}

	for _, row := range rows {
		if steps, ok := passOf(row.Output, key.ID, project); ok {
			return earlier{run: row.ID, steps: steps}, true
		}
	}

	return earlier{}, false
}

// passOf reads the steps of a finished gate run when its output holds the key id and the name of the project.
func passOf(output any, id, project string) ([]GateStep, bool) {
	var out ExclusiveOutput
	if text, ok := output.(string); !ok || json.Unmarshal([]byte(text), &out) != nil || out.Key != id || out.Project != project {
		return nil, false
	}

	return stepsOf(out.Outputs)
}

// stepsOf reads the steps list of a recorded gate. A record that is not a pass on every step reads as not found.
func stepsOf(outputs map[string]any) ([]GateStep, bool) {
	raw, err := json.Marshal(outputs["steps"])
	if err != nil {
		return nil, false
	}

	var steps []GateStep
	if json.Unmarshal(raw, &steps) != nil {
		return nil, false
	}

	return steps, outputs["passed"] == true && everyStepIsAPass(steps)
}
