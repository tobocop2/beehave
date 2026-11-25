package flow

import (
	"context"
	"encoding/json"
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/landing"
	"github.com/tobocop2/beehave/internal/workflow"
)

// landingProofKind is the step kind and the builtin name of landing_proof.
const landingProofKind = "landing_proof"

var errNoLandingProver = &absentError{message: "this build cannot prove a landing. Use a build that sets a batch host", step: "prove a landing"}

// proofCall is a call of landing_proof: the tips of the base before and after the landing, and the members that landed.
type proofCall struct {
	base       string
	start, end landing.Tip
	landed     []landing.Member
}

// landingProof checks on the forge that each landed member is on the base, merged or on the vehicle commit from start to end, in one recorded step.
// It returns the members that it cannot prove. An empty landed list reads nothing. A read that fails ends the step.
func (s *script) landingProof(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		call              proofCall
		label             string
		start, end, given starlark.Value
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{baseKeyword: &call.base, "start": &start, "end": &end, "landed": &given, nameKeyword: &label}); err != nil {
		return nil, err
	}

	if err := call.read(start, end, given); err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}

	if s.e.deps.Batch == nil {
		return nil, errNoLandingProver
	}

	return s.recordProof(label, call)
}

// recordProof runs the proof of the call as a recorded step, so a replay returns the first answer.
func (s *script) recordProof(label string, call proofCall) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	outputs, err := s.durableOutputs(landingProofKind, label, nil, func(ctx context.Context, _ string) (stepResult, error) { return s.proveLanding(ctx, call) })
	if err != nil {
		return nil, err
	}

	return proofResult(outputs)
}

// proveLanding asks the batch host for the members that it cannot prove. An empty landed list asks nothing.
func (s *script) proveLanding(ctx context.Context, call proofCall) (stepResult, error) {
	unproven := []landing.Outcome{}

	if len(call.landed) > 0 {
		found, err := s.e.deps.Batch.Prove(ctx, s.in.Project, call.base, call.start, call.end, call.landed)
		if err != nil {
			return stepResult{}, fmt.Errorf("prove the landing on %s: %w", call.base, err)
		}

		unproven = append(unproven, found...)
	}

	return stepResult{Outputs: landingProofOutputs(unproven)}, nil
}

// proofResult is the result of landing_proof from the outputs of its step: a first run and a replay decode the same way.
func proofResult(outputs map[string]any) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var proof struct {
		Unproven []landing.Outcome `json:"unproven"`
	}

	data, err := json.Marshal(outputs)
	if err == nil {
		err = json.Unmarshal(data, &proof)
	}

	if err != nil {
		return nil, fmt.Errorf("read the record of landing_proof: %w", err)
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{unprovenField: outcomes(proof.Unproven)}), nil
}

// read reads the tips and the landed members of the call.
func (c *proofCall) read(start, end, given starlark.Value) (err error) {
	if c.start, err = tipValue("start", start); err != nil {
		return err
	}

	if c.end, err = tipValue("end", end); err != nil {
		return err
	}

	c.landed, err = landedMembers(given)

	return err
}

// tipValue reads a tip: a struct with the text fields commit and tree, such as the result of base_tip.
func tipValue(keyword string, v starlark.Value) (landing.Tip, error) {
	commit, err := textField(keyword, v, commitField)
	if err != nil {
		return landing.Tip{}, err
	}

	tree, err := textField(keyword, v, treeName)

	return landing.Tip{Commit: commit, Tree: tree}, err
}

// landedMembers reads the landed list: one struct with the text fields item and head for each member.
func landedMembers(v starlark.Value) ([]landing.Member, error) {
	entries, ok := workflow.Elements(v)
	if !ok {
		return nil, fmt.Errorf("`landed` must be a list of struct(item = ..., head = ...), not %s", v.Type())
	}

	landed := make([]landing.Member, 0, len(entries))

	for i, entry := range entries {
		member, err := landedMember(fmt.Sprintf("landed[%d]", i), entry)
		if err != nil {
			return nil, err
		}

		landed = append(landed, member)
	}

	return landed, nil
}

// landedMember reads one member of the landed list. The keyword names its place in the error.
func landedMember(keyword string, entry starlark.Value) (landing.Member, error) {
	item, err := textField(keyword, entry, itemName)
	if err != nil {
		return landing.Member{}, err
	}

	head, err := textField(keyword, entry, headKeyword)

	return landing.Member{Item: item, Head: head}, err
}

// textField reads a field of a struct that must be text that is not empty. The keyword names the value in the error.
func textField(keyword string, v starlark.Value, field string) (string, error) {
	holder, isStruct := v.(*starlarkstruct.Struct)
	if !isStruct {
		return "", fmt.Errorf("`%s` must be a struct with the field %s, not %s", keyword, field, v.Type())
	}

	value, err := holder.Attr(field)
	text, isText := starlark.AsString(value)

	if err != nil || !isText || text == "" {
		return "", fmt.Errorf("`%s` needs the field %s as text that is not empty", keyword, field)
	}

	return text, nil
}
