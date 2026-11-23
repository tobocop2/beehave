package flow

import (
	"context"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/workflow"
)

// underReplace runs start, which reads the newest run of the item and starts or joins a run. With the policy replace it first cancels the open run, and it holds probe.lock from the cancel
// to the end of start, so a reader that holds the lock never sees the item with no open run between the cancel and the new run.
func (c *Client) underReplace(in ItemInput, start func() error) error {
	if in.OnOpen != workflow.OnOpenReplace {
		return start()
	}

	return c.underProbe(func() error {
		if err := c.replaceHeld(in.Item, in.Project); err != nil {
			return err
		}

		return start()
	})
}

// planReplacing plans one spawned item after the replace that the spawn call asks for.
func (e *Engine) planReplacing(ctx context.Context, in ItemInput, id string, taken []string, policy workflow.OnOpen) (spawnedRun, error) {
	if err := e.refuseAncestor(in, id, policy); err != nil {
		return spawnedRun{}, err
	}

	if err := e.asClient().replaceOpen(id, in.Project, policy); err != nil { //nolint:contextcheck // the cancel and the release bound their own work, as in moveHeld
		return spawnedRun{}, err
	}

	return e.planOne(ctx, in, id, taken)
}

// refuseAncestor refuses a replace of an item that a run above the spawning run executes: the cancel would stop the run that spawns it.
func (e *Engine) refuseAncestor(in ItemInput, id string, policy workflow.OnOpen) error {
	if policy != workflow.OnOpenReplace {
		return nil
	}

	for above := in.Parent; above != ""; {
		if item, ok := itemOfRun(above); ok && item == id {
			return fmt.Errorf("item %s is an ancestor of item %s, and on_open=%q would cancel the run that spawns it. Leave it out of items", id, in.Item, workflow.OnOpenReplace)
		}

		next, err := e.parentOf(above)
		if err != nil {
			return fmt.Errorf("read the run %s above item %s: %w", above, in.Item, err)
		}

		above = next
	}

	return nil
}

// parentOf returns the id of the run that spawned the run, or an empty text when the run has no parent.
func (e *Engine) parentOf(id string) (string, error) {
	rows, err := e.store().List(dbos.WithFilterWorkflowIDs(id))
	if err != nil || len(rows) == 0 {
		return "", err
	}

	return rows[0].ParentWorkflowID, nil
}

// replaceOpen cancels the open run of the item, with its children, when the policy is replace. The next attempt then starts through planOf, because a canceled run is terminal.
// The read of the run, the project check and the cancel run under probe.lock, so a mover of the run finishes first and the read sees the rewound run.
func (c *Client) replaceOpen(item, project string, policy workflow.OnOpen) error {
	if policy != workflow.OnOpenReplace {
		return nil
	}

	return c.underProbe(func() error { return c.replaceHeld(item, project) })
}

// replaceHeld is replaceOpen while probe.lock is held. It reads the run again: a run that ended since needs no cancel, and a run of another project is refused before any cancel.
// A failed run is left alone, so planOf refuses it as it does for a start with no option.
func (c *Client) replaceHeld(item, project string) error {
	last, found, err := latestRun(c.store(), item)
	if err != nil || !found || last.terminal() {
		return err
	}

	if err := startProject(last, item, project); err != nil {
		return err
	}

	return c.cancelHeld(item, last)
}
