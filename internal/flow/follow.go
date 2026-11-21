package flow

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// followPoll is how long the follow waits before it looks for a new phase of a run that goes on.
const followPoll = time.Second

// FollowedEntry is one entry of the stream of an agent phase, with the id of the phase that wrote it.
type FollowedEntry struct {
	Phase string
	AgentEntry
}

// Follow gives to emit each entry of the agent streams of a run, in the order of its phases, as the entries arrive. target is a run id or an item id,
// and an item id names its newest run. The follow ends when the run has ended and every phase stream is read, or when ctx ends. An ended run gives its streams again.
func (c *Client) Follow(ctx context.Context, target string, emit func(FollowedEntry)) error {
	id, err := c.followedRun(target)
	if err != nil {
		return err
	}

	read := map[string]bool{}

	for done := false; !done && err == nil; {
		if done, err = c.followOnce(ctx, id, read, emit); !done && err == nil {
			err = pause(ctx, followPoll)
		}
	}

	return err
}

// pause waits for d, or returns the error of ctx when it ends first.
func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// followOnce reads each phase of the run that read does not hold yet, and adds it there. It reports true when the run had ended before the look and the look found no new phase.
func (c *Client) followOnce(ctx context.Context, id string, read map[string]bool, emit func(FollowedEntry)) (bool, error) {
	over, phases, err := c.lookAt(id)
	if err != nil {
		return false, err
	}

	fresh := slices.DeleteFunc(phases, func(phase string) bool { return read[phase] })
	for _, phase := range fresh {
		read[phase] = true

		if err := c.readPhase(ctx, phase, emit); err != nil {
			return false, err
		}
	}

	return over && len(fresh) == 0, nil
}

// followedRun returns the run that a target names: a run id, or else the newest run of the item.
func (c *Client) followedRun(target string) (string, error) {
	if _, err := c.runRow(target); err == nil {
		return target, nil
	}

	last, found, err := latestRun(c.flow, target)
	if err != nil {
		return "", err
	}

	if !found {
		return "", fmt.Errorf("%w: %q has no run. Run beehave status to see the run ids", errNoSuchRun, target)
	}

	return last.Status.ID, nil
}

// lookAt reports whether a run has ended, and lists its agent phases. The state is read before the list, so a phase that starts after the read shows at the next look.
func (c *Client) lookAt(id string) (bool, []string, error) {
	row, err := c.runRow(id)
	if err != nil {
		return false, nil, err
	}

	phases, err := c.phasesOf(id)

	return run{Status: row}.terminal(), phases, err
}

// phasesOf lists the ids of the agent phases of a run, the oldest first.
func (c *Client) phasesOf(id string) ([]string, error) {
	rows, err := dbos.ListWorkflows(c.flow, dbos.WithFilterParentWorkflowID(id), dbos.WithFilterName(phaseName))
	if err != nil {
		return nil, fmt.Errorf("list the agent phases of %s: %w", id, err)
	}

	slices.SortFunc(rows, func(a, b dbos.WorkflowStatus) int { return a.CreatedAt.Compare(b.CreatedAt) })

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}

	return ids, nil
}

// readPhase gives each entry of the stream of one phase to emit. The library ends the read when the phase is no longer open.
func (c *Client) readPhase(ctx context.Context, phase string, emit func(FollowedEntry)) error {
	entries, err := dbos.ReadStreamAsync[AgentEntry](c.flow, phase, agentStream)
	if err != nil {
		return fmt.Errorf("read the agent stream of %s: %w", phase, err)
	}

	for err == nil {
		select {
		case next, open := <-entries:
			if !open || next.Closed {
				return nil
			}

			err = deliver(phase, next, emit)
		case <-ctx.Done():
			err = ctx.Err()
		}
	}

	return err
}

// deliver gives the entry of a read to emit, or returns the error that the read holds.
func deliver(phase string, next dbos.StreamValue[AgentEntry], emit func(FollowedEntry)) error {
	if next.Err != nil {
		return fmt.Errorf("read the agent stream of %s: %w", phase, next.Err)
	}

	emit(FollowedEntry{Phase: phase, AgentEntry: next.Value})

	return nil
}
