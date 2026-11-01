package flow

// The outputs of each builtin that records a step, one function each. The fields of a function are the fields in workflow.ResultFields,
// and a test compares them, so the check of a workflow file and the run read the same names.

func commandOutputs(stdout, stderr string, rc int) map[string]any {
	return map[string]any{"stdout": stdout, "stderr": stderr, "rc": rc}
}

// gateOutputs records the fields of a GateResult. A gate with no step records an empty list, never a null.
func gateOutputs(passed bool, head, tree, report string, steps []GateStep) map[string]any {
	return map[string]any{"passed": passed, "head": head, treeName: tree, "report": report, "steps": append([]GateStep{}, steps...)}
}

func teardownOutputs(down bool, reason string) map[string]any {
	return map[string]any{"down": down, reasonKey: reason}
}

func answerOutputs(answer string) map[string]any { return map[string]any{"answer": answer} }

func mergeOutputs(state, reason string) map[string]any {
	return map[string]any{"state": state, reasonKey: reason}
}

func createOutputs(id string, created bool) map[string]any {
	return map[string]any{"id": id, "created": created}
}

func statusOutputs(id, status string) map[string]any {
	return map[string]any{"id": id, statusKeyword: status}
}

func commentOutputs(id string) map[string]any { return map[string]any{"id": id} }

// agentOutputs are the fields that every agent call returns. The outputs that the call declares come in addition.
// stop is "turns" or "budget" when a cap ended the run, and empty otherwise.
func agentOutputs(text, session, transcript, head, violations, stop string) map[string]any {
	return map[string]any{"text": text, "session": session, "transcript": transcript, "head_sha": head, "violations": violations, "stop": stop}
}

// agentFields are the fields that every agent call returns, as the view of a run reads them. A test decodes agentOutputs into it.
// The output verdict is declared by the call, so a reader takes it by its name.
type agentFields struct {
	Text       string `json:"text"`
	Session    string `json:"session"`
	Transcript string `json:"transcript"`
	Head       string `json:"head_sha"`
	Violations string `json:"violations"`
	Stop       string `json:"stop"`
}
