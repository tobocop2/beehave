package flow

import (
	"context"

	"github.com/tobocop2/beehave/internal/workflow"
)

// latestFor returns the newest run of the item of the start, after the replace that the start asks for.
func (c *Client) latestFor(in ItemInput) (run, bool, error) {
	if err := c.replaceOpen(in.Item, in.Project, in.OnOpen); err != nil {
		return run{}, false, err
	}

	return latestRun(c.store(), in.Item)
}

// planReplacing plans one spawned item after the replace that the spawn call asks for.
func (e *Engine) planReplacing(ctx context.Context, in ItemInput, id string, taken []string, policy workflow.OnOpen) (spawnedRun, error) {
	if err := e.asClient().replaceOpen(id, in.Project, policy); err != nil { //nolint:contextcheck // the cancel and the release bound their own work, as in moveHeld
		return spawnedRun{}, err
	}

	return e.planOne(ctx, in, id, taken)
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
