package flow

// The outputs of each builtin that records a step, one function each. The fields of a function are the fields in workflow.ResultFields,
// and a test compares them, so the check of a workflow file and the run read the same names.

func commandOutputs(stdout, stderr string, rc int) map[string]any {
	return map[string]any{"stdout": stdout, "stderr": stderr, "rc": rc}
}

// writeOutputs records the fields of a write: the fields of a command, the key of the call, and whether the read-back found the write done.
func writeOutputs(stdout, stderr string, rc int, key string, skipped bool) map[string]any {
	out := commandOutputs(stdout, stderr, rc)
	out["key"], out["skipped"] = key, skipped

	return out
}

// gateOutputs records the fields of a GateResult. A gate with no step records an empty list, never a null.
func gateOutputs(passed bool, head, tree, report string, steps []GateStep) map[string]any {
	return map[string]any{"passed": passed, "head": head, treeName: tree, "report": report, "steps": append([]GateStep{}, steps...)}
}

func teardownOutputs(down bool, reason string) map[string]any {
	return map[string]any{"down": down, reasonKey: reason}
}

func upOutputs(started bool, host string) map[string]any {
	return map[string]any{"started": started, hostKeyword: host}
}

// answerOutputs records the answer of a wait. timed_out is true when the deadline of the wait passed first, and the answer is then empty.
// answers holds the text of each person who answered, by name. It is an empty dict for a wait that names no people, never a null.
func answerOutputs(answer string, answers map[string]string, timedOut bool) map[string]any {
	if answers == nil {
		answers = map[string]string{}
	}

	return map[string]any{"answer": answer, "answers": answers, "timed_out": timedOut}
}

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

// prCommentOutputs are the fields of pr_comment: the number of the pull request.
func prCommentOutputs(number int) map[string]any { return map[string]any{numberField: number} }

// openOutputs are the fields of pr_open: the number of the pull request, whether this call opened it, and the id of its native stack (empty for a pull request in none).
func openOutputs(r OpenResult) map[string]any {
	return map[string]any{numberField: r.Number, "created": r.Created, "stack": r.Stack}
}

// boardOutputs are the fields of board_sync: the number of the pull request or issue, and the status that the read-back showed.
func boardOutputs(number int, status string) map[string]any {
	return map[string]any{numberField: number, statusKeyword: status}
}

// prOutputs are the fields of pr_assign: the number of the pull request, and the issues that the call assigned besides it. issues is an empty list, never null.
func prOutputs(number int, issues []int) map[string]any {
	if issues == nil {
		issues = []int{}
	}

	return map[string]any{numberField: number, "issues": issues}
}

// agentOutputs are the fields that every agent call returns. The outputs that the call declares come in addition.
// stop is "turns" or "budget" when a cap ended the run, and empty otherwise.
// cost_usd is the total cost in dollars that the agent CLI reported for the run, or 0 when it reported none.
// rewrote is true when the commit the run started on is not in the history of the commit it ended on.
func agentOutputs(text, session, transcript, head, violations, stop string, cost float64, rewrote bool) map[string]any {
	return map[string]any{textKeyword: text, "session": session, "transcript": transcript, "head_sha": head, "violations": violations, "stop": stop, costField: cost, "rewrote": rewrote}
}

// costField is the output of an agent call that holds the cost of the run in dollars.
const costField = "cost_usd"

// floatBits is the bit size of a dollar amount.
const floatBits = 64

// agentFields are the fields that every agent call returns, as the view of a run reads them. A test decodes agentOutputs into it.
// The output verdict is declared by the call, so a reader takes it by its name.
type agentFields struct {
	Text       string  `json:"text"`
	Session    string  `json:"session"`
	Transcript string  `json:"transcript"`
	Head       string  `json:"head_sha"`
	Violations string  `json:"violations"`
	Stop       string  `json:"stop"`
	Cost       float64 `json:"cost_usd"`
	Rewrote    bool    `json:"rewrote"`
}
