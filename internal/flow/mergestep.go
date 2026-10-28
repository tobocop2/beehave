package flow

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"

	"github.com/tobocop2/obee/internal/hclfile"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

// The values of the `state` output of a merge step. While the step waits for the owner, the run shows mergeOwner.
const (
	mergeDone     = "merged"
	mergeSentBack = "sent_back"
	mergeOwner    = "owner_merge"
)

// errNoMerger is returned when a merge step asks to merge and the engine has no merger.
var errNoMerger = errors.New("this engine has no merger. Remove `merge = true` from the merge step, and the owner merges the pull request")

// MergeSettings are the values of a merge step that merges. A value that the step leaves out is empty.
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

// mergeSettings evaluates the settings of a merge step. An expression that cannot be evaluated fails the step with the position of the expression.
func (c *call) mergeSettings(step *workflow.Merge) (MergeSettings, error) {
	s, err := c.mergeTexts(step)
	if err != nil {
		return s, err
	}

	if s.Logins, s.Emails, err = c.own(step.Own); err != nil {
		return s, err
	}

	if s.ForbidText, err = c.words(step.ForbidText); err != nil {
		return s, err
	}

	s.Approved, err = c.flag(step.Approved)

	return s, err
}

// mergeTexts evaluates the text settings of a merge step: its repository, its base, its hold label and its `children` rule.
func (c *call) mergeTexts(step *workflow.Merge) (MergeSettings, error) {
	var s MergeSettings

	var err error

	for _, text := range []struct {
		expr hcl.Expression
		into *string
	}{{step.Repo, &s.Repo}, {step.Base, &s.Base}, {step.Hold, &s.Hold}} {
		if *text.into, err = c.text(text.expr); err != nil {
			return s, err
		}
	}

	s.Retarget, err = c.retarget(step.Children)

	return s, err
}

// retarget evaluates `children`. An absent value and "refuse" are false, "retarget" is true, and any other value is an error that names it.
func (c *call) retarget(expr hcl.Expression) (bool, error) {
	children, err := c.text(expr)
	if err != nil {
		return false, err
	}

	switch children {
	case "", "refuse":
		return false, nil
	case "retarget":
		return true, nil
	}

	return false, hclfile.At(expr.Range(), fmt.Sprintf("`children` is %q. Use \"refuse\" or \"retarget\".", children))
}

// own evaluates the `own` object into its logins and its addresses. An absent `own` has none.
func (c *call) own(expr hcl.Expression) (logins, emails []string, err error) {
	if absent(expr) {
		return nil, nil, nil
	}

	value, err := c.value(expr)
	if err != nil {
		return nil, nil, err
	}

	if value.IsNull() || !value.Type().IsObjectType() {
		return nil, nil, hclfile.At(expr.Range(), "`own` must be an object with `logins` and `emails`. Write { logins = [...], emails = [...] }.")
	}

	for key, into := range map[string]*[]string{"logins": &logins, "emails": &emails} {
		if *into, err = ownList(value, key, expr); err != nil {
			return nil, nil, err
		}
	}

	return logins, emails, nil
}

// ownList reads one list of strings of the `own` object. A list that is not there is empty.
func ownList(value cty.Value, key string, expr hcl.Expression) ([]string, error) {
	if !value.Type().HasAttribute(key) {
		return nil, nil
	}

	list, err := convert.Convert(value.GetAttr(key), cty.List(cty.String))
	if err != nil || list.IsNull() {
		return nil, hclfile.At(expr.Range(), "`own."+key+"` must be a list of strings.")
	}

	return elements(list), nil
}

// runMerge pins the commit in `head`, marks the pull request ready, and merges it only when `merge` is true.
// It then reads the pull request until the owner merges it or sends it back. The wait is a durable sleep.
func runMerge(c *call, step *workflow.Merge) (stepResult, error) {
	head, err := c.text(step.Head)
	if err != nil {
		return stepResult{}, err
	}

	merges, err := c.flag(step.Merge)
	if err != nil {
		return stepResult{}, err
	}

	settings, err := c.settingsToMerge(step, merges)
	if err != nil {
		return stepResult{}, err
	}

	if err := c.w.readyAndMerge(head, merges, settings); err != nil {
		return stepResult{}, err
	}

	return c.w.awaitOwner(!merges)
}

// settingsToMerge returns the settings of a step that merges. A step that does not merge has none and needs no merger.
func (c *call) settingsToMerge(step *workflow.Merge, merges bool) (MergeSettings, error) {
	if !merges {
		return MergeSettings{}, nil
	}

	if c.w.e.deps.Merge == nil {
		return MergeSettings{}, errNoMerger
	}

	return c.mergeSettings(step)
}

// readyAndMerge marks the pull request ready at head. It merges the pull request only when merges is true.
func (w *walker) readyAndMerge(head string, merges bool, settings MergeSettings) error {
	ready := func(ctx context.Context, project, item string) error { return w.e.markReady(ctx, project, item, head) }
	if err := w.e.lifecycle(w.ctx, model.StepReady, ready, w.in.Project, w.in.Item); err != nil {
		return err
	}

	if !merges {
		return nil
	}

	return w.e.mergeReady(w.ctx, w.in.Project, w.in.Item, head, settings)
}

// awaitOwner reads the pull request until the owner merges it or sends it back. When ownerMerges is true, the run first shows that it waits for the owner.
// The state and the reason go into the result of the item. The reason is empty after a merge.
func (w *walker) awaitOwner(ownerMerges bool) (stepResult, error) {
	reason, err := w.watchOwner(ownerMerges)
	if err != nil {
		return stepResult{}, err
	}

	state := mergeDone
	if reason != "" {
		state = mergeSentBack
	}

	w.merge, w.sentBack = state, reason

	return stepResult{Outputs: map[string]any{"state": state, "reason": reason}}, nil
}

// watchOwner shows that the run waits for the owner when ownerMerges is true, then watches the pull request.
func (w *walker) watchOwner(ownerMerges bool) (string, error) {
	var err error

	if ownerMerges {
		err = show(w.ctx, w.in.Item, mergeKey, mergeOwner, "that it waits for the owner to merge")
	}

	var reason string

	if err == nil {
		reason, err = w.e.watchReady(w.ctx, w.in.Project, w.in.Item)
	}

	return reason, err
}

// flag evaluates an optional argument that is true or false. An absent argument is false.
func (c *call) flag(expr hcl.Expression) (bool, error) {
	if absent(expr) {
		return false, nil
	}

	return c.w.scope.Bool(expr, c.round)
}
