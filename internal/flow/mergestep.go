package flow

import (
	"context"
	"errors"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/model"
)

// The values of the `state` output of a merge call. While the call waits for the owner, the run shows mergeOwner.
const (
	mergeDone     = "merged"
	mergeSentBack = "sent_back"
	mergeOwner    = "owner_merge"
)

// errNoMerger is returned when a merge call asks to merge and the engine has no merger.
var errNoMerger = errors.New("this engine has no merger. Remove `merge = True` from the merge call, and the owner merges the pull request")

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
}

// mergeArgs are the arguments of a merge call: the head to merge, whether to merge, the label of the call and the settings of a merge.
type mergeArgs struct {
	head, label string
	merges      bool
	settings    MergeSettings
}

// mergeCall unpacks the arguments of the merge builtin.
func mergeCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (m mergeArgs, err error) {
	var (
		children    = "refuse"
		own, forbid starlark.Value
		s           = &m.settings
	)

	err = starlark.UnpackArgs(b.Name(), args, kwargs, "head", &m.head, "repo?", &s.Repo, "base?", &s.Base, "hold?", &s.Hold, "own?", &own, "approved?", &s.Approved,
		"forbid_text?", &forbid, "children?", &children, "merge?", &m.merges, "name?", &m.label)
	if err != nil {
		return m, err
	}

	return m, s.read(children, forbid, own)
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

	if err := s.readyAndMerge(m.head, m.merges, m.settings); err != nil {
		return nil, err
	}

	return s.awaitOwner(m.label, !m.merges)
}

// readyAndMerge marks the pull request ready at head. It merges the pull request only when merges is true.
func (s *script) readyAndMerge(head string, merges bool, settings MergeSettings) error {
	ready := func(ctx context.Context, project, item string) error { return s.e.markReady(ctx, project, item, head) }
	if err := s.e.lifecycle(s.ctx, model.StepReady, ready, s.in.Project, s.in.Item); err != nil {
		return err
	}

	if !merges {
		return nil
	}

	return s.e.mergeReady(s.ctx, s.in.Project, s.in.Item, head, settings)
}

// awaitOwner reads the pull request until the owner merges it or sends it back. When ownerMerges is true, the run first shows that it waits for the owner.
// The state and the reason go into the result of the item. The reason is empty after a merge.
func (s *script) awaitOwner(label string, ownerMerges bool) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	reason, err := s.watchOwner(ownerMerges)
	if err != nil {
		return nil, err
	}

	state := mergeDone
	if reason != "" {
		state = mergeSentBack
	}

	s.mergeState, s.sentBack = state, reason

	return s.durable("merge", label, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: map[string]any{"state": state, "reason": reason}}, nil
	})
}

// watchOwner shows that the run waits for the owner when ownerMerges is true, then watches the pull request.
func (s *script) watchOwner(ownerMerges bool) (string, error) {
	if ownerMerges {
		if err := show(s.ctx, s.in.Item, mergeKey, mergeOwner, "that it waits for the owner to merge"); err != nil {
			return "", err
		}
	}

	return s.e.watchReady(s.ctx, s.in.Project, s.in.Item)
}
