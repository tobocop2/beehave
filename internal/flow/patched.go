package flow

import (
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/workflow"
)

// patched is the builtin patched(name). It is true for a run that has not passed this call and false for a run that has, so an author changes a step
// before the step that an open run reached and keeps the old code in the false branch. DBOS records the marker DBOS.patch-<name> and decides the answer.
func (s *script) patched(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var name string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &name}); err != nil {
		return nil, err
	}

	if err := s.patchScope(name); err != nil {
		return nil, err
	}

	patched, err := dbos.Patch(s.ctx, name)

	return starlark.Bool(patched), err
}

// patchScope refuses a call outside a workflow run, a name that the signature refuses, and a name that an earlier call of the run used.
func (s *script) patchScope(name string) error {
	if workflowID(s.ctx) == "" {
		return errNoScope
	}

	if err := workflow.CheckPatchName(name); err != nil {
		return fmt.Errorf("patched: %w", err)
	}

	if s.patches[name] {
		return fmt.Errorf("patched name %q is used twice in this run. Give each call its own name", name)
	}

	if s.patches == nil {
		s.patches = map[string]bool{}
	}

	s.patches[name] = true

	return nil
}
