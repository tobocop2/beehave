package flow

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// gateRecord is what a run keeps of a gate it recorded: the verdict, the head and the tree that the gate ran on, and its steps.
type gateRecord struct {
	Passed bool       `json:"passed"`
	Head   string     `json:"head"`
	Tree   string     `json:"tree"`
	Steps  []GateStep `json:"steps"`
}

// decodeGate reads the record of a gate from the outputs of its step.
func decodeGate(outputs map[string]any) (record gateRecord, err error) {
	data, err := json.Marshal(outputs)
	if err != nil {
		return record, err
	}

	return record, json.Unmarshal(data, &record)
}

// keepGate remembers the gate that a call recorded, by its step name, so a merge can check a gate value against it. A replay of the call keeps it again.
// A record that does not decode is not kept, so a merge that names its step finds no gate and refuses: the trust fails closed.
func (s *script) keepGate(outputs map[string]any) {
	step, isText := outputs[stepField].(string)
	if !isText || step == "" {
		return
	}

	record, err := decodeGate(outputs)
	if err != nil {
		return
	}

	if s.gates == nil {
		s.gates = map[string]gateRecord{}
	}

	s.gates[step] = record
}

// trustedGate returns the record of the gate that the `gate` value of a merge call names. Only a gate that this run recorded counts, so a value that a workflow made up has no record.
// The merge reads the verdict and the tree from the record of the run and never from the value, so a value that carries other fields changes nothing.
// The gate must have passed with every step a pass, and gated_tree must be the tree that it ran on.
func (s *script) trustedGate(gate starlark.Value, gatedTree string) (gateRecord, error) {
	step, err := gateStepOf(gate)
	if err != nil {
		return gateRecord{}, err
	}

	record, recorded := s.gates[step]
	if !recorded {
		return gateRecord{}, fmt.Errorf("`gate` names the step %s, and no gate call of this run recorded it. Pass the value that gate() returned in this run", step)
	}

	if !record.Passed || !everyStepIsAPass(record.Steps) {
		return gateRecord{}, fmt.Errorf("the gate of the step %s did not pass, so the merge posts no status and merges nothing", step)
	}

	if record.Tree == "" || record.Tree != gatedTree {
		return gateRecord{}, fmt.Errorf("gated_tree is %q and the gate of the step %s ran on the tree %q. Pass gated_tree = gate_result.tree", gatedTree, step, record.Tree)
	}

	return record, nil
}

// gateStepOf reads the step name that a gate value carries in its field step.
func gateStepOf(gate starlark.Value) (string, error) {
	result, isStruct := gate.(*starlarkstruct.Struct)
	if !isStruct {
		return "", fmt.Errorf("`gate` must be the result of a gate call, not %s. Pass the value that gate() returned", gate.Type())
	}

	field, err := result.Attr(stepField)
	step, isText := starlark.AsString(field)

	if err != nil || !isText || step == "" {
		return "", errors.New("`gate` is not the result of a gate call. Pass the value that gate() returned")
	}

	return step, nil
}

// gateStatuses lists the statuses of a member whose gate passed: beehave/gate on its head, and beehave/review on its head when a review approved it.
// A status stands for the head it names, so a later push to the member leaves its new head with none. A call with no gate posts none.
func (s *script) gateStatuses(call batchCall) ([]model.CommitStatus, error) {
	if !given(call.gate) {
		return nil, nil
	}

	record, err := s.trustedGate(call.gate, call.gatedTree)
	if err != nil {
		return nil, err
	}

	m := call.members[0]

	var statuses []model.CommitStatus
	if m.Approved {
		statuses = append(statuses, model.CommitStatus{SHA: m.Head, Context: model.ContextReview, State: model.StatusSuccess, Description: "The review approved this head."})
	}

	return append(statuses, model.CommitStatus{SHA: m.Head, Context: model.ContextGate, State: model.StatusSuccess, Description: "The gate passed on the union commit " + record.Head + "."}), nil
}

// mergeOne merges the one pull request of a call with `member`. A gate that this run did not record, or one that did not pass, ends the call before any step.
// A base tip with another tree than expect_tree ends the call as unsettled with no merge.
func (s *script) mergeOne(call batchCall) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	statuses, err := s.gateStatuses(call)
	if err != nil {
		return nil, fmt.Errorf("merge %q: %w", call.name, err)
	}

	m, base := call.members[0], &batchBase{s: s, call: call}

	out, err := base.Merge(s.ctx, m, call.gatedTree, statuses)
	if err != nil {
		return nil, fmt.Errorf("merge %q: merge %s: %w", call.name, m.Item, err)
	}

	tip, err := runStep(s.e, s.ctx, "batch:"+call.name+".tip", base.Tip, dbos.WithStepMaxRetries(stepRetries))
	if err != nil {
		return nil, fmt.Errorf("merge %q: %w", call.name, err)
	}

	return oneResult(out, tip), nil
}

// mergedField is the result field of a merge that holds the members that landed, or whether the one member landed.
const mergedField = "merged"

// mergeStatusText is the text of the status field of a merge of one pull request, by the status of the outcome.
var mergeStatusText = map[batch.MergeStatus]string{batch.StatusMerged: "merged", batch.StatusRefused: "refused", batch.StatusUnsettled: "unsettled"} //nolint:gochecknoglobals // a read-only table

// oneResult is the struct of a merge of one pull request: whether it merged, the status, the whole reason of a refusal, and the tip after the call.
func oneResult(out batch.MergeOutcome, tip batch.Tip) starlark.Value { //nolint:ireturn // the builtin contract of starlark-go
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		mergedField: starlark.Bool(out.Status == batch.StatusMerged), statusKeyword: starlark.String(mergeStatusText[out.Status]), reasonKey: starlark.String(out.Reason),
		treeName: starlark.String(tip.Tree), commitField: starlark.String(tip.Commit),
	})
}

// handOffCall marks the pull requests of `prs` ready and watches them until the owner merges them. It lands nothing.
func (s *script) handOffCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call := batchCall{handOff: true}

	var prs starlark.Value

	set := &call.settings

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"prs": &prs, baseKeyword: &set.Base, repoKeyword: &set.Repo, nameKeyword: &call.name})
	if err != nil {
		return nil, err
	}

	if err := s.batchScope(call.name); err != nil {
		return nil, err
	}

	if err := call.fill(prs, nil, nil, nil); err != nil {
		return nil, err
	}

	return s.handOff(call)
}
