package flow

import "github.com/tobocop2/obee/internal/agent"

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
// required holds the names that the gate list of the project had when the gate ran. A merge reads them from this record, never from the config of the day.
func gateOutputs(passed bool, head, tree, report string, steps []GateStep, required []string) map[string]any {
	return map[string]any{
		"passed": passed, "head": head, treeName: tree, "report": report, "steps": append([]GateStep{}, steps...), "required": append([]string{}, required...),
	}
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
// cost_estimated is true when cost_usd is an estimate that obee made from the tokens of the run, and false when the agent CLI reported the cost.
// cost_unknown is true when the run used tokens of a model that has no price, so cost_usd holds no dollars for it.
// rewrote is true when the commit the run started on is not in the history of the commit it ended on.
func agentOutputs(text, session, transcript, head, violations, stop string, spend agent.Spend, rewrote bool) map[string]any {
	out := spendOutputs(spend)
	out[textKeyword], out["session"], out["transcript"], out["head_sha"], out["violations"], out["stop"] = text, session, transcript, head, violations, stop
	out["rewrote"] = rewrote

	return out
}

// costField is the output of an agent call that holds the cost of the run in dollars.
const costField = "cost_usd"

// The other outputs of an agent call that describe its cost.
const (
	estimatedField = "cost_estimated"
	unknownField   = "cost_unknown"
)

// spendOutputs are the outputs that hold a cost: the dollars and both marks.
func spendOutputs(spend agent.Spend) map[string]any {
	return map[string]any{costField: spend.Cost, estimatedField: spend.Estimated, unknownField: spend.Unknown}
}

// spendOf reads the cost that the outputs of an agent call hold. An output that is missing or has another type reads as no cost and no mark.
func spendOf(outputs map[string]any) agent.Spend {
	cost, _ := outputs[costField].(float64)        //nolint:errcheck // a missing or ill-typed cost reads as 0
	estimated, _ := outputs[estimatedField].(bool) //nolint:errcheck // a missing mark reads as false
	unknown, _ := outputs[unknownField].(bool)     //nolint:errcheck // a missing mark reads as false

	return agent.Spend{Cost: cost, Estimated: estimated, Unknown: unknown}
}

// floatBits is the bit size of a dollar amount.
const floatBits = 64

// agentFields are the fields that every agent call returns, as the view of a run reads them. A test decodes agentOutputs into it.
// The output verdict is declared by the call, so a reader takes it by its name.
type agentFields struct {
	agent.Spend

	Text       string `json:"text"`
	Session    string `json:"session"`
	Transcript string `json:"transcript"`
	Head       string `json:"head_sha"`
	Violations string `json:"violations"`
	Stop       string `json:"stop"`
	Rewrote    bool   `json:"rewrote"`
}
