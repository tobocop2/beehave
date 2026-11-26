package flow

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/tobocop2/beehave/internal/agent"
)

// LimitWait is a provider limit that holds an agent step until it resets. The step sleeps until WaitUntil, then runs the agent call again with the same login.
// The view shows it on the run and on the line of the held step.
type LimitWait struct {
	// Limit is the limit that the agent reported: its kind, its window and its reset time.
	Limit agent.Limit `json:"limit"`
	// Login is the login whose limit stopped the run. The rerun keeps it, because the session of the run lives in its config folder.
	Login string `json:"login"`
	// WaitUntil is the end of the sleep, fixed when the limit was recorded, so a replay sleeps until the same time.
	WaitUntil time.Time `json:"wait_until"`
}

// text is the note of the wait for the human view of the status.
func (w LimitWait) text() string {
	return "waits for the " + w.Limit.Name() + " of login " + w.Login + " until " + w.WaitUntil.Format(time.RFC3339)
}

// note is the paragraph that a fresh rerun after the limit gets, with what the view lets the engine say about the changes since the step started.
func (w LimitWait) note(view lostView, stat string) string {
	return "This step ran before, and the " + w.Limit.Name() + " of login " + w.Login + " stopped it. " + treeNote(view, stat)
}

// limitRecord is what the limit step of an agent call records after a run that a provider limit stopped: the wait, the session and the cost of the run.
// Passed is true when the reset time had passed when the step ran, so the wait is the least wait.
type limitRecord struct {
	LimitWait

	// Session is the session of the step: the one that the limited run named, else the one that it was given to resume (ResumeOf).
	Session string `json:"session,omitempty"`
	// Note is the note of a fresh rerun after the reset.
	Note string `json:"note,omitempty"`
	agent.Spend

	Passed bool `json:"reset_passed,omitempty"`
}

// continued is the prompt of the resumed session of the rerun. The brief and the output file are in the session already.
func (r limitRecord) continued() string {
	return "The " + r.Limit.Name() + " of login " + r.Login + " stopped you while you worked on this step, and it has reset. " + continueStep
}

// rerun returns the limited run for the rerun after the reset, or nil when the step has no session: the note then goes into the prompt.
func (r limitRecord) rerun() *LostRun {
	if r.Session == "" {
		return nil
	}

	return &LostRun{Session: r.Session, Note: r.Note, Continue: r.continued()}
}

// afterLimit returns the input of the rerun after the limit that record holds. in is the input of the try, so the notes of two limits never stack.
func (in PhaseInput) afterLimit(record limitRecord) PhaseInput {
	in.ResetPassed, in.Rerun = record.Passed, true

	if in.Limited = record.rerun(); in.Limited == nil {
		in.Prompt += "\n\n" + record.Note
	}

	return in
}

const (
	// overloadFirstWait is the wait after the first overload of a try. Each overload in a row doubles the wait.
	overloadFirstWait = time.Minute
	// overloadWaits is the ceiling of the backoff: the number of waits that an overload gets before the step fails. The waits are 1, 2, 4, 8 and 16 minutes.
	overloadWaits = 5
)

// overloadWait is the wait that overload number n of a row gets, counted from 0.
func (c clock) overloadWait(n int) time.Duration {
	return cmp.Or(c.Overload, overloadFirstWait) << n
}

// limitCounts counts the limits that stopped the runs of one try.
type limitCounts struct {
	// runs is the number of limits so far, which numbers the limit records. overloads is the number of overloads in a row.
	runs, overloads int
}

// add counts the limit that stopped the next run. It returns the number of that limit among all, and, for an overload, the number of overloads in a row before it.
func (c *limitCounts) add(limit agent.Limit) (run, overloads int) {
	run = c.runs
	c.runs++

	if limit.Kind != agent.LimitOverload {
		c.overloads = 0

		return run, 0
	}

	c.overloads++

	return run, c.overloads - 1
}

// waitOutLimit records the limit that stopped a run of a try, with the cost of that run that the caps read, and sleeps until the limit resets.
// An overload has no reset time: it waits the backoff, and past the ceiling of the backoff it fails the step.
// It returns the input of the rerun, built from base, the input of the try. Its ResetPassed is true when the reset time had passed, so the rerun fails its phase when its limit has a past reset time too.
func (s *script) waitOutLimit(name string, base, in PhaseInput, ended PhaseOutput, try int, counts *limitCounts) (PhaseInput, error) {
	run, overloads := counts.add(*ended.Limit)

	record, err := runStep(s.e, s.ctx, name+"/limit"+strconv.Itoa(try)+"."+strconv.Itoa(run), func(ctx context.Context) (limitRecord, error) {
		if overloads >= overloadWaits {
			return limitRecord{}, undated(in, *ended.Limit)
		}

		return s.limitRecordOf(ctx, in, ended, overloads), nil
	})
	if err != nil {
		if s.running() {
			s.countFailedRun(name, try, ended.Spend)
		}

		return PhaseInput{}, err
	}

	return base.afterLimit(record), s.waitForReset(record.WaitUntil)
}

// limitRecordOf is the record of the run that ended at a limit. Only the note uses the diff summary, so a diff that cannot be read changes the note and keeps the record.
// An overload ends its wait at the backoff for overload number overloads of its row, counted from 0, and its wait is never a past reset.
func (s *script) limitRecordOf(ctx context.Context, in PhaseInput, ended PhaseOutput, overloads int) limitRecord {
	limit, now := *ended.Limit, s.e.deps.Clock.wall()
	wait := LimitWait{Limit: limit, Login: in.Login, WaitUntil: limit.ResetsAt}

	if limit.Kind == agent.LimitOverload {
		wait.WaitUntil = now.Add(s.e.deps.Clock.overloadWait(overloads))
	}

	stat, view, unread := s.e.lostDiff(ctx, in)
	if unread != nil {
		s.e.logger.WarnContext(ctx, "cannot read the diff summary of the limited agent run", "item", in.Item, "error", unread)
	}

	return limitRecord{
		LimitWait: wait, Session: cmp.Or(ended.Session, ended.ResumeOf), Note: wait.note(view, stat), Spend: ended.Spend,
		Passed: limit.Kind != agent.LimitOverload && !limit.ResetsAt.After(now),
	}
}

// dateLimit gives a limit that carries no reset time the reset time that the usage read of the pool gives for the login of the run, so the wait goes on as if the agent had given it.
// An overload is not dated: the usage windows of the login say nothing about when the provider is less busy. The step records the dated limit, so a replay never reads the usage again. A read that knows no time leaves the limit undated, and the end of the phase fails it (see limitEnd).
func (e *Engine) dateLimit(ctx context.Context, in PhaseInput, out *PhaseOutput) error {
	if out.Outcome != agent.OutcomeLimited || out.Limit == nil || !out.Limit.ResetsAt.IsZero() || out.Limit.Kind == agent.LimitOverload {
		return nil
	}

	reset, known, err := e.deps.Logins.Reset(ctx, in.Project, in.Login)
	if err != nil {
		return fmt.Errorf("read the usage of login %s for the reset of the %s: %w. Run beehave resume %s when the usage read works", in.Login, out.Limit.Name(), err, in.Item)
	}

	if known {
		limit := *out.Limit
		limit.ResetsAt = reset
		out.Limit = &limit
	}

	return nil
}

// undated is the error of a limit that no reset time ends: the agent gives none, and the usage read of the pool gives none or is not asked. An overload past the ceiling of its backoff ends here too.
func undated(in PhaseInput, limit agent.Limit) error {
	return fmt.Errorf("the agent step of item %s stopped at the %s of login %s, and neither the agent nor the usage read gives a reset time. Run beehave resume %s when the limit is gone",
		in.Item, limit.Name(), in.Login, in.Item)
}

// limitEnd fails the phase of a run that a provider limit stopped when no wait has an end: the limit gives no reset time, or it is the second limit
// in a row whose reset time has passed. An overload has no reset time, and its wait is the backoff of the limit step. The failed phase is the failed step, so beehave resume reopens the phase and runs the agent again.
func limitEnd(in PhaseInput, limit agent.Limit, now time.Time) error {
	switch {
	case limit.Kind == agent.LimitOverload:
		return nil
	case limit.ResetsAt.IsZero():
		return undated(in, limit)
	case in.ResetPassed && !limit.ResetsAt.After(now):
		return fmt.Errorf("the agent step of item %s stopped at the %s of login %s twice in a row with a reset time that has passed (%s), so the reset time is wrong. "+
			"Run beehave resume %s when the limit is gone", in.Item, limit.Name(), in.Login, limit.ResetsAt.Format(time.RFC3339), in.Item)
	}

	return nil
}
