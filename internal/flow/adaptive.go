package flow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
)

// adaptiveTick is the time between two looks of the controller. A usage read starts the agent CLI once for each login and may take 30 seconds, so the tick is longer.
const adaptiveTick = time.Minute

// limitSignals is the room of the channel that carries the limited phases to the controller. A send that finds it full is dropped: the hold that the first limit opened covers it.
const limitSignals = 16

// halving is the divisor of the limit when a phase ends at a provider limit.
const halving = 2

// limitSignal tells the controller that a phase ended at a provider limit. At is the time when the phase ended. ResetsAt is the zero time when the limit has no reset time.
type limitSignal struct{ At, ResetsAt time.Time }

// controller moves the global limit of the agents queue between a floor and a ceiling while the engine runs.
// It adds one agent at a tick when every login has room and a phase waits. It halves the limit at once at the first limited phase and holds it until the reset time.
// One goroutine runs it: it is the only caller of set, and it owns every field.
type controller struct {
	floor, ceiling int
	// current is the limit that the queue holds, as far as the controller knows.
	current int
	// holdUntil is the time before which the limit does not rise. A phase that ended at a limit before this time halves nothing more.
	holdUntil time.Time
	tick      time.Duration
	// set writes the limit to the queue.
	set func(limit int) error
	// room reads the usage of every login and says whether each one has room.
	room func(ctx context.Context) bool
	// waiting counts the phases that wait for a slot of the agents queue.
	waiting func(ctx context.Context) (int, error)
	limited <-chan limitSignal
	logger  *slog.Logger
}

// launch writes the floor to the queue, so the number does not depend on what an earlier engine left in the queue row.
func (c *controller) launch() error {
	if err := c.set(c.floor); err != nil {
		return fmt.Errorf("set the limit of the agents queue to %d: %w", c.floor, err)
	}

	c.current = c.floor

	return nil
}

// step runs one tick at time now: it adds one agent when it may.
func (c *controller) step(ctx context.Context, now time.Time) {
	if now.Before(c.holdUntil) || c.current >= c.ceiling {
		return
	}

	waiting, err := c.waiting(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "cannot count the phases that wait for the agents queue", "error", err)

		return
	}

	if waiting == 0 || !c.room(ctx) {
		return
	}

	c.move(ctx, c.current+1)
}

// halve takes a phase that ended at a limit. The first one of a hold halves the limit and opens the hold. A phase that ended before the hold ends changes nothing.
// A limit with no reset time, or one whose reset time has passed, holds for one tick.
func (c *controller) halve(ctx context.Context, signal limitSignal) {
	if signal.At.Before(c.holdUntil) {
		return
	}

	c.holdUntil = signal.At.Add(c.tick)
	if signal.ResetsAt.After(c.holdUntil) {
		c.holdUntil = signal.ResetsAt
	}

	c.move(ctx, max(c.floor, c.current/halving))
}

// move writes a new limit when it differs from the current one. A failed write keeps the old number, and the next tick tries again.
func (c *controller) move(ctx context.Context, limit int) {
	if limit == c.current {
		return
	}

	if err := c.set(limit); err != nil {
		c.logger.WarnContext(ctx, "cannot set the limit of the agents queue", "limit", limit, "error", err)

		return
	}

	c.current = limit
}

// run takes one step for each value of ticks, and takes each limited phase when it arrives, until stop closes. The context of a step ends when stop closes, so a slow usage read does not hold the shutdown.
func (c *controller) run(ctx context.Context, ticks <-chan time.Time, stop <-chan struct{}) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go cancelOnStop(ctx, cancel, stop)

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			c.step(ctx, now)
		case signal := <-c.limited:
			c.halve(ctx, signal)
		}
	}
}

// cancelOnStop cancels ctx when stop closes, and returns when ctx ends.
func cancelOnStop(ctx context.Context, cancel context.CancelFunc, stop <-chan struct{}) {
	select {
	case <-stop:
		cancel()
	case <-ctx.Done():
	}
}

// adaptive reports whether the engine moves the limit of the agents queue.
func (e *Engine) adaptive() bool { return e.concurrency == config.ConcurrencyAdaptive }

// startController writes the floor to the agents queue and starts the controller next to the status tick. A static engine starts nothing.
func (e *Engine) startController() error {
	if !e.adaptive() {
		return nil
	}

	c := e.newController()

	if err := c.launch(); err != nil {
		return err
	}

	e.tickRun.Go(func() { e.runController(c) })

	return nil
}

// runController runs the controller on the real tick of a minute, or on the tick that a test gives.
func (e *Engine) runController(c *controller) {
	ticks := e.deps.Clock.Adapt
	if ticks == nil {
		ticker := time.NewTicker(c.tick)
		defer ticker.Stop()

		ticks = ticker.C
	}

	c.run(e.ctx, ticks, e.tickEnd)
}

// newController builds the controller of this engine: the floor is agents.per_login, the ceiling is agents.max_concurrent.
func (e *Engine) newController() *controller {
	return &controller{
		floor: e.perLogin, ceiling: e.maxConcurrent, tick: adaptiveTick,
		set:     func(limit int) error { return e.queue.SetGlobalConcurrency(e.ctx, &limit) },
		room:    e.deps.Logins.Headroom,
		waiting: e.waitingPhases, limited: e.limited, logger: e.logger,
	}
}

// waitingPhases counts the phases that wait for a slot of the agents queue.
func (e *Engine) waitingPhases(context.Context) (int, error) {
	rows, err := dbos.ListWorkflows(e.ctx, dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued), dbos.WithFilterName(phaseName), dbos.WithFilterQueueName(queueName))
	if err != nil {
		return 0, fmt.Errorf("list the phases that wait: %w", err)
	}

	return len(rows), nil
}

// signalLimit tells the controller that a phase ended at a limit, with the time when it ended. It never blocks: a full channel drops the signal. A static engine has the channel and no reader, so it keeps the first signals and drops the rest.
func (e *Engine) signalLimit(out PhaseOutput) {
	if out.Outcome != agent.OutcomeLimited || out.Limit == nil {
		return
	}

	select {
	case e.limited <- limitSignal{At: e.deps.Clock.wall(), ResetsAt: out.Limit.ResetsAt}:
	default:
	}
}

// AgentsCap is the limit of the agents queue that beehave status shows for an adaptive engine.
type AgentsCap struct {
	Mode    config.Concurrency `json:"mode"`
	Current int                `json:"current"`
	Ceiling int                `json:"ceiling"`
}

// AgentsCap reads the limit that the agents queue holds now. It reports false for a static config, which holds the ceiling and has nothing to show.
// A state folder that no engine has opened yet has no queue row: the limit is then the floor, where an engine starts.
func (c *Client) AgentsCap() (AgentsCap, bool, error) {
	if c.concurrency != config.ConcurrencyAdaptive {
		return AgentsCap{}, false, nil
	}

	shown := AgentsCap{Mode: c.concurrency, Current: c.perLogin, Ceiling: c.maxConcurrent}

	queue, err := dbos.RetrieveQueue(c.flow, queueName)

	switch {
	case errors.Is(err, dbos.ErrQueueNotFound):
		return shown, true, nil
	case err != nil:
		return AgentsCap{}, false, fmt.Errorf("read the limit of the agents queue: %w", err)
	}

	if limit := queue.GetGlobalConcurrency(); limit != nil {
		shown.Current = *limit
	}

	return shown, true, nil
}
