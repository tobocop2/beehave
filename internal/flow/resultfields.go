package flow

// The outputs of each builtin that records a step, one function each. The fields of a function are the fields in workflow.ResultFields,
// and a test compares them, so the check of a workflow file and the run read the same names.

func commandOutputs(stdout string, rc int) map[string]any {
	return map[string]any{"stdout": stdout, "rc": rc}
}

func gateOutputs(passed bool, head, tree, report string) map[string]any {
	return map[string]any{"passed": passed, "head": head, treeName: tree, "report": report}
}

func answerOutputs(answer string) map[string]any { return map[string]any{"answer": answer} }

func mergeOutputs(state, reason string) map[string]any {
	return map[string]any{"state": state, reasonKey: reason}
}

func createOutputs(id string, created bool) map[string]any {
	return map[string]any{"id": id, "created": created}
}

func statusOutputs(id, status string) map[string]any {
	return map[string]any{"id": id, "status": status}
}

func commentOutputs(id string) map[string]any { return map[string]any{"id": id} }

// agentOutputs are the fields that every agent call returns. The outputs that the call declares come in addition.
func agentOutputs(text, session, transcript, head, violations string) map[string]any {
	return map[string]any{"text": text, "session": session, "transcript": transcript, "head_sha": head, "violations": violations}
}
