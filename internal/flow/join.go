package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	// joinPoll is how often a joining run reads the store, when the clock names no interval.
	joinPoll = time.Second
	// engineGone is how long no engine may hold the state folder before a joining run stops waiting, when the clock names no time.
	engineGone = 10 * time.Second
)

// errOtherBuild is returned when the engine that runs is not this build. It dequeues only runs of its own version, so a run for another build would wait for ever.
var errOtherBuild = errors.New("the engine that runs is another build")

// ErrEngineGone is returned when the engine that held the state folder stops while a joined run waits. The run stays queued, and the next engine of the same build takes it.
var ErrEngineGone = errors.New("the engine stopped; the run is queued")

// attemptPlan says what a run call does for an item: join the run with the id Join, or start attempt Attempt.
type attemptPlan struct {
	Join    string
	Attempt int
}

// planOf decides what a run call does with the latest run of the item. The engine that runs the item and the client that joins it both ask here.
// An open run is joined. A run that the launch of this engine resumed, and that ended since, answers the call. A failed run is refused: only beehave resume continues it.
// A finished or canceled run, or no run, starts the next attempt.
func planOf(last run, found bool, in ItemInput, version string, resumed bool) (attemptPlan, error) {
	switch {
	case found && !last.terminal():
		return joinPlan(last, in, version)
	case found && resumed:
		return attemptPlan{Join: last.Status.ID}, nil
	case found && last.failed():
		return attemptPlan{}, failedError(last, in.Item)
	}

	return attemptPlan{Attempt: last.Attempt + 1}, nil
}

// joinPlan joins an open run. It refuses a run of another project or another version.
func joinPlan(open run, in ItemInput, version string) (attemptPlan, error) {
	held, err := open.project()
	if err != nil {
		return attemptPlan{}, err
	}

	if held != in.Project {
		return attemptPlan{}, conflictError(in.Item, held, in.Project)
	}

	if open.Status.ApplicationVersion != version {
		return attemptPlan{}, strandedError(open, version)
	}

	return attemptPlan{Join: open.Status.ID}, nil
}

// EngineRuns reports whether an engine holds the state folder, as beehave serve or another beehave run does.
func (c *Client) EngineRuns() (bool, error) {
	return engineRuns(c.dir)
}

// JoinRun gives the item to the engine that holds the state folder and waits for its result.
// The run enters the items queue with the id of its attempt, so two joins of one item make one run. The engine dequeues it, and agents.max_concurrent applies.
// The run picks its login at its first agent step, as a run that beehave run starts does. When no engine holds the state folder for a while, the call returns ErrEngineGone and the run stays queued.
func (c *Client) JoinRun(ctx context.Context, item, project, file string) (ItemResult, error) {
	in, err := pinnedInput(item, project, file)
	if err != nil {
		return ItemResult{}, err
	}

	if err := in.check(); err != nil {
		return ItemResult{}, err
	}

	if err := c.sameBuildAsEngine(); err != nil {
		return ItemResult{}, err
	}

	handle, err := c.planAndHandle(in)
	if err != nil {
		return ItemResult{}, err
	}

	return c.await(ctx, handle, item)
}

// planAndHandle plans the attempt for the item and returns the run to wait for.
func (c *Client) planAndHandle(in ItemInput) (dbos.WorkflowHandle[ItemResult], error) {
	last, found, err := latestRun(c.flow, in.Item)
	if err != nil {
		return nil, err
	}

	plan, err := planOf(last, found, in, c.version, false)
	if err != nil {
		return nil, err
	}

	return c.handleOf(plan, in)
}

// sameBuildAsEngine refuses a build that differs from the version of the live engine.
// The engine writes its version into the version file, so the check reads the version that runs. When no engine holds the state folder, the check reads the newest version that an engine registered.
func (c *Client) sameBuildAsEngine() error {
	live, err := c.liveVersion()
	if err != nil {
		return err
	}

	if live != c.version {
		return fmt.Errorf("%w: restart beehave serve (it runs %s and this is %s)", errOtherBuild, live, c.version)
	}

	return nil
}

// liveVersion returns the version of the engine that holds the state folder. With no live engine, or one that is shutting down, it returns the newest version that an engine registered.
func (c *Client) liveVersion() (string, error) {
	running, live, err := engineState(c.dir)
	if err != nil {
		return "", err
	}

	if running && live != "" {
		return live, nil
	}

	latest, err := dbos.GetLatestApplicationVersion(c.flow)
	if err != nil {
		return "", fmt.Errorf("read the version of the engine: %w", err)
	}

	return latest.Name, nil
}

// handleOf retrieves the run to join, or enqueues the next attempt for the version of this build.
func (c *Client) handleOf(plan attemptPlan, in ItemInput) (dbos.WorkflowHandle[ItemResult], error) {
	if plan.Join != "" {
		return dbos.RetrieveWorkflow[ItemResult](c.flow, plan.Join)
	}

	handle, err := dbos.Enqueue[ItemResult](c.flow, itemsQueueName, itemName, in,
		dbos.WithEnqueueWorkflowID(runID(in.Item, plan.Attempt)), dbos.WithEnqueueApplicationVersion(c.version))
	if err != nil {
		return nil, fmt.Errorf("queue the run of item %s: %w", in.Item, err)
	}

	return handle, nil
}

// await waits for the result of the run. A run in the queue waits for ever when no engine takes it, so each poll reads the status of the run and checks for an engine. The check holds probe.lock for microseconds and never meets an engine that is starting, so a poll each second is cheap and safe.
// The loop runs in the caller, so it ends with ctx and leaves no poller behind.
func (c *Client) await(ctx context.Context, handle dbos.WorkflowHandle[ItemResult], item string) (ItemResult, error) {
	poll := cmp.Or(c.give.Clock.Poll, joinPoll)
	watch := engineWatch{dir: c.dir, grace: cmp.Or(c.give.Clock.Gone, engineGone), item: item}
	ticker := time.NewTicker(poll)

	defer ticker.Stop()

	for {
		result, done, err := watch.poll(handle)
		if done || err != nil {
			return result, err
		}

		select {
		case <-ctx.Done():
			return ItemResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// poll reads the run once. It returns done with the result when the run ended, and an error when the status cannot be read or no engine holds the state folder for the grace time.
func (w *engineWatch) poll(handle dbos.WorkflowHandle[ItemResult]) (ItemResult, bool, error) {
	status, err := handle.GetStatus()
	if err != nil {
		return ItemResult{}, false, fmt.Errorf("item %s: read the status of the run: %w", w.item, err)
	}

	if (run{Status: status}).terminal() {
		result, resultErr := handle.GetResult()
		out, outErr := outcome{result, resultErr}.unwrap(w.item)

		return out, true, outErr
	}

	return ItemResult{}, false, w.check()
}

// outcome is what a run ended with.
type outcome struct {
	result ItemResult
	err    error
}

// unwrap returns the result of the run, or its error named after the item.
func (o outcome) unwrap(item string) (ItemResult, error) {
	if o.err != nil {
		return ItemResult{}, fmt.Errorf("item %s: %w", item, o.err)
	}

	return o.result, nil
}

// engineWatch counts how long no engine holds the state folder.
type engineWatch struct {
	dir   string
	item  string
	grace time.Duration
	since time.Time
}

// check returns ErrEngineGone, with the next step for the item, when no engine has held the state folder for the whole grace time.
func (w *engineWatch) check() error {
	running, err := engineRuns(w.dir)
	if err != nil {
		return err
	}

	if running {
		w.since = time.Time{}

		return nil
	}

	if w.since.IsZero() {
		w.since = time.Now()
	}

	if time.Since(w.since) >= w.grace {
		return fmt.Errorf("%w: start beehave serve or run beehave run %s again", ErrEngineGone, w.item)
	}

	return nil
}
