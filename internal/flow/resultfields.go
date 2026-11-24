package flow

import (
	"cmp"
	"encoding/json"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

// revertField is the result field of gate that holds the verdict of the revert run.
const revertField = "revert"

// The outputs of each builtin that records a step, one function each. The fields of a function are the fields in workflow.ResultFields,
// and a test compares them, so the check of a workflow file and the run read the same names.

// commandOutputs records the fields of a command: what the program printed, its exit code, the number of tries, and no error.
func commandOutputs(out CommandOutput, attempts int) map[string]any {
	fields := programOutputs(out, attempts)
	fields[errorField] = ""

	return fields
}

// programOutputs records what a program printed, its exit code and the number of tries.
func programOutputs(out CommandOutput, attempts int) map[string]any {
	return map[string]any{"stdout": out.Stdout, "stderr": out.Stderr, "rc": out.RC, attemptsField: attempts}
}

// attemptsField is the result field that counts the tries of a call.
const attemptsField = "attempts"

// writeOutputs records the fields of a write: what the program printed, its exit code and tries, the key of the call, and whether the read-back found the write done.
func writeOutputs(run CommandOutput, attempts int, key string, skipped bool) map[string]any {
	out := programOutputs(run, attempts)
	out["key"], out["skipped"] = key, skipped

	return out
}

// gateOutputs records the fields of a GateResult. A gate with no step records an empty list, never a null.
// required holds the names that the gate list of the project had when the gate ran. A merge reads them from this record, never from the config of the day.
// checks is what the log of a candidate job gave. new_tests is the list of its new test names, and a gate with no checks records an empty list and a zero record.
// host_loss is an empty dict and host_losses is 0 here. Only a gate whose host job was lost and proved unable to run fills them: host_losses then counts
// the host losses of the item tree, this one included (recordLoss).
func gateOutputs(passed bool, head, tree, report string, steps []GateStep, required []string, checks *GateChecks) map[string]any {
	recorded := GateChecks{}
	if checks != nil {
		recorded = *checks
	}

	recorded.NewTests, recorded.Failed = append([]string{}, recorded.NewTests...), append([]string{}, recorded.Failed...)
	recorded.RevertPassed, recorded.Revert = append([]string{}, recorded.RevertPassed...), cmp.Or(recorded.Revert, model.RevertFail)

	return map[string]any{
		"passed": passed, "head": head, treeName: tree, "report": report, "steps": append([]GateStep{}, steps...), "required": append([]string{}, required...),
		"checks": recorded, "new_tests": recorded.NewTests, revertField: string(recorded.Revert), hostLossKey: map[string]any{}, hostLossesKey: 0,
	}
}

func teardownOutputs(down bool, reason string) map[string]any {
	return map[string]any{"down": down, reasonKey: reason}
}

func upOutputs(started bool, host string) map[string]any {
	return map[string]any{"started": started, hostKeyword: host}
}

// deferredOutputs records a host_up that started nothing because the host has a size table and the first gate starts it. Only this record holds the field deferred.
func deferredOutputs(host string) map[string]any {
	return map[string]any{"started": false, hostKeyword: host, "deferred": true}
}

// answerOutputs records the answer of a wait. timed_out is true when the deadline of the wait passed first, and the answer is then empty.
// answers holds the text of each person who answered, by name. It is an empty dict for a wait that names no people, never a null.
func answerOutputs(answer string, answers map[string]string, timedOut bool) map[string]any {
	if answers == nil {
		answers = map[string]string{}
	}

	return map[string]any{"answer": answer, "answers": answers, "timed_out": timedOut}
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

// prStateOutputs are the fields of pr_state: the state of the pull request, OPEN, CLOSED or MERGED.
func prStateOutputs(state string) map[string]any { return map[string]any{stateField: state} }

// pushOutputs are the fields of push: the head that the branch holds after the push, and the branch. A fork reads the branch to keep out of it.
func pushOutputs(branch, head string) map[string]any {
	return map[string]any{headKeyword: head, branchKeyword: branch}
}

// prHistoryOutputs are the fields of pr_history: the pull requests of the item, an empty list and never null for an item with none.
func prHistoryOutputs(pulls []model.PriorPull) map[string]any {
	return map[string]any{pullsField: append([]model.PriorPull{}, pulls...)}
}

// prReadOutputs are the fields of pr_read. A pull request that was not found has number 0 and found false, and every list is empty and never null.
func prReadOutputs(facts model.PullFacts) map[string]any {
	return map[string]any{
		"found": facts.Number != 0, numberField: facts.Number, stateField: facts.State, "draft": facts.Draft, "labels": append([]string{}, facts.Labels...),
		"author": facts.Author, assigneesField: append([]string{}, facts.Assignees...), "reviewers": append([]string{}, facts.Reviewers...),
		"links": append([]model.IssueLink{}, facts.Links...),
	}
}

// issueAssigneesOutputs are the fields of issue_assignees: the logins that hold the issue, an empty list and never null for an issue that nobody holds.
func issueAssigneesOutputs(logins []string) map[string]any {
	return map[string]any{assigneesField: append([]string{}, logins...)}
}

// itemStopOutputs are the fields of item_stop: whether the stop is the stop of the run, which is so for the item of the run.
func itemStopOutputs(recorded bool) map[string]any { return map[string]any{"recorded": recorded} }

// agentOutputs are the fields that every agent call returns. The outputs that the call declares come in addition.
// stop is "turns", "budget" or "context" when a cap ended the run, and empty otherwise.
// cost_usd is the total cost in dollars that the agent CLI reported for the run, or 0 when it reported none.
// cost_estimated is true when cost_usd is an estimate that beehave made from the tokens of the run, and false when the agent CLI reported the cost.
// cost_unknown is true when the run used tokens of a model that has no price, so cost_usd holds no dollars for it.
// rewrote is true when the commit the run started on is not in the history of the commit it ended on.
// skills lists the skills that the agent called, in the order of the calls. It is an empty list, never null, for a run that called none.
// committed is true when the commit the run ended on is not the commit it started on.
// commit_subject and commit_body are the message of the commit that the engine made for the step. Both are empty when the step wrote no message.
func agentOutputs(text, session, transcript, head, violations, stop string, spend agent.Spend, rewrote, committed bool, skills []string) map[string]any {
	out := spendOutputs(spend)
	out[textKeyword], out["session"], out["transcript"], out["head_sha"], out["violations"], out["stop"] = text, session, transcript, head, violations, stop
	out["rewrote"], out["committed"] = rewrote, committed
	out["skills"] = append([]string{}, skills...)
	out[commitSubjectField], out[commitBodyField] = "", ""

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

// spendOf reads the cost that the outputs of an agent call hold. A cost that is missing reads as no cost and no mark, as a run that cost nothing does.
// A cost that is there but is no number that costOf can read marks the spend unknown: a silent 0 would hide the spend from the cap.
func spendOf(outputs map[string]any) agent.Spend {
	var (
		cost       float64
		unreadable bool
	)

	unknown, _ := outputs[unknownField].(bool) //nolint:errcheck // a missing mark reads as false

	if raw, present := outputs[costField]; present {
		var readable bool

		cost, readable = costOf(raw)
		unreadable = !readable
	}

	estimated, _ := outputs[estimatedField].(bool) //nolint:errcheck // a missing mark reads as false

	return agent.Spend{Cost: cost, Estimated: estimated, Unknown: unknown || unreadable, Unreadable: unreadable}
}

// recordedSpend reads the cost of the recorded output of an agent step through spendOf, the reader of the run cap. An output that is not text, or is not
// the record of a step, marks the spend unreadable: the record holds a cost that cannot be read.
func recordedSpend(output any) agent.Spend {
	text, ok := output.(string)
	if !ok {
		return agent.Spend{Unknown: true, Unreadable: true}
	}

	var step recorded[map[string]any]

	if json.Unmarshal([]byte(text), &step) != nil {
		return agent.Spend{Unknown: true, Unreadable: true}
	}

	return spendOf(step.Outputs)
}

// costOf reads a dollar amount that is a float64 on a first run and a json.Number on a replay. It reports false, with 0 dollars, for a value that is
// no number, and for a number that does not fit a float64.
func costOf(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		cost, err := x.Float64()
		if err != nil {
			return 0, false
		}

		return cost, true
	default:
		return 0, false
	}
}

// floatBits is the bit size of a dollar amount.
const floatBits = 64

// agentFields are the fields that every agent call returns, as the view of a run reads them, except the cost, which only spendOf reads.
// A test decodes agentOutputs into it. The output verdict is declared by the call, so a reader takes it by its name.
type agentFields struct {
	Text       string `json:"text"`
	Session    string `json:"session"`
	Transcript string `json:"transcript"`
	Head       string `json:"head_sha"`
	Violations string `json:"violations"`
	Stop       string `json:"stop"`
	Rewrote    bool   `json:"rewrote"`
	Committed  bool   `json:"committed"`
}
