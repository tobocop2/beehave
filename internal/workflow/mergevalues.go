package workflow

import (
	"errors"
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	gatepkg "github.com/tobocop2/obee/internal/gate"
)

// The functions of this file own the rules for the arguments of the builtins that land pull requests: merge, batch_merge and preflight.
// The run and the check both call them.

// The errors for the steps argument of a merge call that merges.
var (
	errStepsMissing = errors.New("this merge call names no gate steps. Add `steps = <gate>.steps` to the call, or remove `merge = True` and the owner merges")
	errStepsShape   = errors.New("`steps` must be the steps of a gate. Pass `steps = <gate>.steps`")
	errStepsEmpty   = errors.New("`steps` is empty. The gate recorded no step for this head, so its result is unknown. Pass `steps = <gate>.steps` from a gate that ran")
)

// GateSteps reads the steps argument of a merge call that merges: the steps of the gate at the head.
// A call that leaves it out is refused, a list with no step is refused, and so is a step that is not a pass. An unknown step is not a pass.
func GateSteps(v starlark.Value) error {
	if v == nil {
		return errStepsMissing
	}

	steps, err := ToGo[[]any](v)
	if err != nil {
		return errStepsShape
	}

	if len(steps) == 0 {
		return errStepsEmpty
	}

	for _, each := range steps {
		if err := stepPassed(each); err != nil {
			return err
		}
	}

	return nil
}

// RequiredSteps checks that the steps argument of a merge call holds a step for every name in required. A name that is missing is refused,
// so a gate that ran only some steps of the gate list of the project cannot merge. An empty required list asks for nothing.
func RequiredSteps(v starlark.Value, required []string) error {
	if len(required) == 0 || v == nil {
		return nil
	}

	steps, err := ToGo[[]any](v)
	if err != nil {
		return errStepsShape
	}

	have := stepNames(steps)

	for _, name := range required {
		if !have[name] {
			return fmt.Errorf("the gate list of the project has the step %q, and `steps` has no result for it. A merge needs every step of the gate list. Run the project gate, which runs every step", name)
		}
	}

	return nil
}

// stepNames is the set of names in the steps of a merge call. An item that is not a step has none.
func stepNames(steps []any) map[string]bool {
	have := map[string]bool{}

	for _, each := range steps {
		if step, ok := each.(map[string]any); ok {
			if name, named := step["name"].(string); named {
				have[name] = true
			}
		}
	}

	return have
}

// stepPassed checks one step of the steps argument of a merge call.
func stepPassed(each any) error {
	step, ok := each.(map[string]any)
	name, named := step["name"].(string)
	status, stated := step["status"].(string)

	if !ok || !named || !stated {
		return errStepsShape
	}

	if status != string(gatepkg.StatusPass) {
		return fmt.Errorf("the gate step %q is %s. A merge needs every step of the gate to pass at the head. Run the gate again", name, status)
	}

	return nil
}

// GatedTree reads the tree argument of a merge call that merges. A call that leaves it out asks for no proof. A call that sets it to the empty text
// is refused: a program gate, or a resumed host job, records no tree, and the proof would be skipped without a word.
func GatedTree(v starlark.Value) (string, error) {
	if v == nil {
		return "", nil
	}

	tree, isText := starlark.AsString(v)
	if !isText {
		return "", fmt.Errorf("`tree` must be text, not %s", v.Type())
	}

	if tree == "" {
		return "", errors.New("`tree` is empty. The gate recorded no tree for this head (a gate that runs a program, or a resumed host job, records none). " +
			"Leave out `tree` to merge without the proof")
	}

	return tree, nil
}

// PullRequest is one member of the prs argument of a batch_merge call.
type PullRequest struct{ Item, Head string }

// errNoPullRequest is returned for a prs argument with no member.
var errNoPullRequest = errors.New("`prs` is empty. Pass a list of struct(item = ..., head = ...), one for each pull request to land")

// PullRequests reads the prs argument of a batch_merge call: a non-empty list of structs with the text fields item and head.
func PullRequests(v starlark.Value) ([]PullRequest, error) {
	list, err := ListOfEach(v, pullRequest, errors.New("`prs` must be a list of struct(item = ..., head = ...)"))
	if err != nil {
		return nil, err
	}

	if len(list) == 0 {
		return nil, errNoPullRequest
	}

	return list, nil
}

// pullRequest reads one entry of the prs argument.
func pullRequest(entry starlark.Value) (PullRequest, error) {
	pr, isStruct := entry.(*starlarkstruct.Struct)
	if !isStruct {
		return PullRequest{}, fmt.Errorf("`prs` holds a %s. Use struct(item = ..., head = ...)", entry.Type())
	}

	item, err := prField(pr, "item")
	if err != nil {
		return PullRequest{}, err
	}

	head, err := prField(pr, "head")

	return PullRequest{Item: item, Head: head}, err
}

// prField reads a text field of a member struct.
func prField(pr *starlarkstruct.Struct, name string) (string, error) {
	value, err := pr.Attr(name)
	if err != nil {
		return "", fmt.Errorf("a member of `prs` has no `%s`. Use struct(item = ..., head = ...)", name)
	}

	text, ok := starlark.AsString(value)
	if !ok || text == "" {
		return "", fmt.Errorf("`%s` of a member of `prs` must be text that is not empty", name)
	}

	return text, nil
}

// OwnerIdentity reads the own dict into its logins and its addresses. An absent dict has none.
func OwnerIdentity(v starlark.Value) (logins, emails []string, err error) {
	dict, ok := Optional[*starlark.Dict](v)
	if !ok {
		return nil, nil, errors.New("`own` must be a dict with `logins` and `emails`. Write {\"logins\": [...], \"emails\": [...]}")
	}

	if dict == nil {
		return nil, nil, nil
	}

	if logins, err = ownerList(dict, "logins"); err != nil {
		return nil, nil, err
	}

	emails, err = ownerList(dict, "emails")

	return logins, emails, err
}

// ownerList reads one list of the own dict. A list that the dict leaves out is empty.
func ownerList(dict *starlark.Dict, key string) ([]string, error) {
	list, found, err := dict.Get(starlark.String(key))
	if !found {
		return nil, err
	}

	return Words("own."+key, list)
}
