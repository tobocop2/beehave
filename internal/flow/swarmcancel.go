package flow

import (
	"context"
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/workflow"
)

// cancelOutcome is what the cancel step records: whether the call ended the run.
type cancelOutcome struct {
	Ended bool `json:"ended"`
}

// cancel ends one run that a spawn call of this run returned, with its children. A run that already ended stays as it is.
func (s *script) cancel(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		target starlark.Value
		label  string
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{runKeyword: &target, nameKeyword: &label}); err != nil {
		return nil, err
	}

	ref, err := s.childRef(target)
	if err != nil {
		return nil, err
	}

	done, err := s.cancelOnce(label, ref)
	if err != nil {
		return nil, err
	}

	return cancelValue(done), nil
}

// cancelOnce records whether the run is open, as the step cancel:<label>#<n>.open, and then cancels it as the step cancel:<label>#<n>. The read is recorded
// before any cancel, so a cancel step that runs again after a crash between the cancel and its record still reads that this call ended the run.
func (s *script) cancelOnce(label string, ref spawnedRun) (cancelOutcome, error) {
	open, err := runStep(s.e, s.ctx, s.nextName(cancelKind, label)+openSuffix, func(context.Context) (bool, error) { return s.e.isOpen(ref) })
	if err != nil {
		return cancelOutcome{}, err
	}

	return countedStep(s, cancelKind, label, func(context.Context) (cancelOutcome, error) { return s.e.cancelChild(s.in.Project, ref, open) })
}

// childRef reads one run that spawn returned and refuses it when this run did not spawn it. It returns the latest attempt of the run: the run that gather
// started again last for it after a failure, or the run itself, as send and progress do.
func (s *script) childRef(target starlark.Value) (spawnedRun, error) {
	ref, err := oneRef(target)
	if err != nil {
		return ref, fmt.Errorf("run must be one of the runs that spawn returned, and %s is not one", target)
	}

	latest, err := s.liveRuns([]spawnedRun{ref})
	if err != nil {
		return ref, err
	}

	return latest[0], nil
}

// cancelValue makes the struct that cancel returns: its one field is true when the call ended the run.
func cancelValue(done cancelOutcome) *starlarkstruct.Struct {
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{workflow.CancelledField: starlark.Bool(done.Ended)})
}

// isOpen reads whether a run is still open in the store.
func (e *Engine) isOpen(ref spawnedRun) (bool, error) {
	looks, err := e.readRuns([]spawnedRun{ref})
	if err != nil {
		return false, err
	}

	return looks[0].open, nil
}

// cancelChild cancels a run that is still open, as beehave cancel does, and the children of a run that is canceled already. It reports whether the store
// now holds the run as canceled and the run was open at the recorded read. A run that ended before the cancel, with success or not, is no cancel of this
// call: the store leaves an ended run as it is.
func (e *Engine) cancelChild(project string, ref spawnedRun, open bool) (cancelOutcome, error) {
	now, err := e.readRuns([]spawnedRun{ref})
	if err == nil {
		err = e.cancelLook(project, now[0])
	}

	if err != nil {
		return cancelOutcome{}, err
	}

	after, err := e.readRuns([]spawnedRun{ref})

	return cancelOutcome{Ended: err == nil && open && after[0].canceled}, err
}
