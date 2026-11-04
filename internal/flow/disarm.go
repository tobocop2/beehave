package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// lister lists workflows of the store. A running engine lists through its context and the client through its connection.
type lister func(opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error)

// disarmCanceled turns off the queued merges that a canceled run queued, for a cancel that no live run saw: the engine was stopped when the owner canceled.
// The store already holds what the call needs. Each merge of the run is a child workflow, and its input is the project, the item, the head and the settings
// that the merge was queued with. A merge that was never armed, or that the forge already merged, makes the call do nothing. The call is safe to repeat.
// It runs with the timeout of the disarm of a live run, on a context that the caller's cancel does not reach. A merger of nil turns nothing off.
func disarmCanceled(ctx context.Context, list lister, m merger, id string) error {
	if m == nil {
		return nil
	}

	rows, err := list(dbos.WithFilterParentWorkflowID(id), dbos.WithFilterName(exclusiveName), dbos.WithFilterLoadInput(true))
	if err != nil {
		return fmt.Errorf("list the merges of run %s: %w", id, err)
	}

	off, cancel := context.WithTimeout(context.WithoutCancel(ctx), disarmTimeout)
	defer cancel()

	var failed error

	for _, row := range rows {
		failed = errors.Join(failed, disarmRow(off, m, row))
	}

	return failed
}

// disarmRow turns off the merge that one child workflow queued. A child that is not a merge is skipped.
func disarmRow(ctx context.Context, m merger, row dbos.WorkflowStatus) error {
	var in ExclusiveInput
	if err := json.Unmarshal([]byte(fmt.Sprint(row.Input)), &in); err != nil {
		return fmt.Errorf("read the input of the merge %s: %w", row.ID, err)
	}

	if in.Kind != kindMerge {
		return nil
	}

	if err := m.Disarm(ctx, in.Project, in.Item, in.Head, in.Settings); err != nil {
		return fmt.Errorf("the queued merge of item %s may still be armed and must be turned off by hand: %w", in.Item, err)
	}

	return nil
}
