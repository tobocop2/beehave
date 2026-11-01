package flow

import (
	"context"
	"errors"
	"fmt"

	"go.starlark.net/starlark"

	gatepkg "github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/workflow"
)

// The values of the `state` output of a merge call. While the call waits for the owner, the run shows mergeOwner.
const (
	mergeDone     = "merged"
	mergeSentBack = "sent_back"
	mergeClosed   = "closed"
	mergeOwner    = "owner_merge"
)

// errNoMerger is returned when a merge call asks to merge and the engine has no merger.
var errNoMerger = &absentError{message: "this engine has no merger. Remove `merge = True` from the merge call, and the owner merges the pull request", step: "merge the pull request"}

// MergeSettings are the values of a merge call that merges. A value that the call leaves out is empty.
// Logins and Emails are the identities of `own`. Children is "refuse" or "retarget". Approved is true when another step accepted the head.
type MergeSettings struct {
	Repo       string
	Base       string
	Hold       string
	Logins     []string
	Emails     []string
	ForbidText []string
	Approved   bool
	Retarget   bool
	// GatedTree is the tree id that the gate recorded for the head. When it is set, the merge compares the tree of the base tip with it.
	GatedTree string
	// Intent is the record that batch_merge wrote before the merge. It lets a pull request that is already merged count as done.
	Intent *merge.Intent
}

// mergeArgs are the arguments of a merge call: the head to merge, whether to merge, the label of the call and the settings of a merge.
type mergeArgs struct {
	head, label string
	merges      bool
	settings    MergeSettings
}

// mergeCall unpacks the arguments of the merge builtin.
func mergeCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (m mergeArgs, err error) {
	var own, forbid, tree, steps starlark.Value

	children, s := "refuse", &m.settings

	err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		headKeyword: &m.head, "repo": &s.Repo, baseKeyword: &s.Base, holdKeyword: &s.Hold, ownKeyword: &own, "approved": &s.Approved, "tree": &tree,
		"steps": &steps, "forbid_text": &forbid, "children": &children, mergeKey: &m.merges, nameKeyword: &m.label,
	})
	if err != nil {
		return m, err
	}

	if m.merges {
		if s.GatedTree, err = mergeProof(tree, steps); err != nil {
			return m, err
		}
	}

	return m, s.read(children, forbid, own)
}

// mergeProof reads the `tree` and `steps` arguments of a merge call that merges. It returns the gated tree.
func mergeProof(tree, steps starlark.Value) (string, error) {
	gated, err := gatedTree(tree)
	if err != nil {
		return "", err
	}

	return gated, everyStepPassed(steps)
}

// everyStepPassed reads the `steps` argument of a merge call that merges: the steps of the gate at the head.
// A call that leaves it out is refused, a list with no step is refused, and so is a step that is not a pass. An unknown step is not a pass.
func everyStepPassed(v starlark.Value) error {
	if v == nil {
		return errStepsMissing
	}

	steps, err := toGo[[]any](v)
	if err != nil {
		return errStepsShape
	}

	if len(steps) == 0 {
		return errors.New("`steps` is empty. The gate recorded no step for this head, so its result is unknown. Pass `steps = <gate>.steps` from a gate that ran")
	}

	for _, each := range steps {
		if err := stepPassed(each); err != nil {
			return err
		}
	}

	return nil
}

// stepPassed checks one step of the `steps` argument of a merge call.
func stepPassed(each any) error {
	step, ok := each.(map[string]any)
	name, named := step["name"].(string)
	status, stated := step[statusKeyword].(string)

	if !ok || !named || !stated {
		return errStepsShape
	}

	if status != passStatus {
		return fmt.Errorf("the gate step %q is %s. A merge needs every step of the gate to pass at the head. Run the gate again", name, status)
	}

	return nil
}

// The statuses of a gate step, as the gate package names them.
const (
	passStatus = string(gatepkg.StatusPass)
	failStatus = string(gatepkg.StatusFail)
	// unknownStatus is a step that gave no result, as a voided gate leaves every step.
	unknownStatus = string(gatepkg.StatusUnknown)
)

// errStepsMissing is returned when a merge call that merges names no gate steps. A merge with no gate result would land a head that no check passed.
var errStepsMissing = errors.New("this merge call names no gate steps. Add `steps = <gate>.steps` to the call, or remove `merge = True` and the owner merges")

// errStepsShape is returned when the `steps` argument of a merge call is not the list that a gate records.
var errStepsShape = errors.New("`steps` must be the steps of a gate. Pass `steps = <gate>.steps`")

// gatedTree reads the `tree` argument of a merge call. A call that leaves it out asks for no proof. A call that sets it to the empty text is refused:
// a program gate, or a resumed host job, records no tree, and the proof would be skipped without a word.
func gatedTree(v starlark.Value) (string, error) {
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

// read checks the `children`, `forbid_text` and `own` arguments of a merge call and keeps them in s.
func (s *MergeSettings) read(children string, forbid, own starlark.Value) (err error) {
	if s.Retarget, err = retarget(children); err != nil {
		return err
	}

	if s.ForbidText, err = words("forbid_text", forbid); err != nil {
		return err
	}

	s.Logins, s.Emails, err = ownerIdentity(own)

	return err
}

// retarget reads `children`: "refuse" is false, "retarget" is true, and any other value is an error that names it.
func retarget(children string) (bool, error) {
	switch children {
	case "", "refuse":
		return false, nil
	case "retarget":
		return true, nil
	}

	return false, fmt.Errorf("`children` is %q. Use \"refuse\" or \"retarget\"", children)
}

// ownerIdentity reads the `own` dict into its logins and its addresses. An absent dict has none.
func ownerIdentity(v starlark.Value) (logins, emails []string, err error) {
	dict, ok := optional[*starlark.Dict](v)
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

// ownerList reads one list of the `own` dict. A list that the dict leaves out is empty.
func ownerList(dict *starlark.Dict, key string) ([]string, error) {
	list, found, err := dict.Get(starlark.String(key))
	if !found {
		return nil, err
	}

	return words("own."+key, list)
}

// merge pins the commit in `head`, marks the pull request ready, and merges it only when `merge` is true.
// It then reads the pull request until the owner merges it or sends it back. The wait is a durable sleep.
func (s *script) merge(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	m, err := mergeCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	if m.merges && s.e.deps.Merge == nil {
		return nil, errNoMerger
	}

	if !m.merges {
		m.settings = MergeSettings{}
	}

	if err := s.readyAndMerge(m); err != nil {
		return nil, err
	}

	return s.awaitOwner(m.label, !m.merges)
}

// readyAndMerge marks the pull request ready at the head of the call. It merges the pull request only when the call merges.
// A call that merges first checks the scope of the pull request against the first review.
func (s *script) readyAndMerge(m mergeArgs) error {
	if m.merges {
		if err := s.holdScope(m.label); err != nil {
			return err
		}
	}

	pinned, err := s.e.readyStep(s.ctx, s.in.Project, s.in.Item, m.head)
	if err != nil {
		return err
	}

	if !m.merges {
		return nil
	}

	return s.e.mergeReady(s.ctx, s.in.Project, s.in.Item, pinned, m.settings)
}

// awaitOwner reads the pull request until the owner merges it or sends it back. When ownerMerges is true, the run first shows that it waits for the owner.
// The state and the reason go into the result of the item. The reason is empty after a merge.
func (s *script) awaitOwner(label string, ownerMerges bool) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	end, err := s.watchOwner(ownerMerges)
	if err != nil {
		return nil, err
	}

	state, reason := mergeDone, end.Reason

	switch {
	case end.Closed:
		state, s.closed = mergeClosed, end.Reason
	case end.Reason != "":
		state, s.sentBack = mergeSentBack, end.Reason
	}

	s.mergeState = state

	return s.durable("merge", label, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: mergeOutputs(state, reason)}, nil
	})
}

// watchOwner shows that the run waits for the owner when ownerMerges is true, then watches the pull request.
func (s *script) watchOwner(ownerMerges bool) (watchEnd, error) {
	if ownerMerges {
		if err := show(s.ctx, s.in.Item, mergeKey, mergeOwner, "that it waits for the owner to merge"); err != nil {
			return watchEnd{}, err
		}
	}

	return s.e.watchReady(s.ctx, s.in.Project, s.in.Item)
}
