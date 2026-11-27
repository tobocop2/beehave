package flow

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// refusedText is the error that the refusal triggers of these tests raise.
const refusedText = "the store refused the record"

// paidRun is an agent run that ends well and costs 0.30, so a cost that counts twice shows.
var paidRun = agent.Result{Outcome: agent.OutcomeExited, Session: "sess-1", Transcript: "/t.jsonl", Text: "all done", Spend: agent.Spend{Cost: 0.3}}

// refuseOutcome makes the store refuse the record of the outcome of the step name: a record that holds no error.
func refuseOutcome(t *testing.T, db *sql.DB, name string) {
	t.Helper()

	_, err := db.ExecContext(t.Context(), "CREATE TRIGGER refuse_outcome BEFORE INSERT ON operation_outputs WHEN NEW.function_name = '"+name+ //nolint:gosec // the name is a step name of the test, and a trigger takes no parameter
		"' AND NEW.error IS NULL BEGIN SELECT RAISE(ABORT, '"+refusedText+"'); END")
	require.NoError(t, err)
}

// refuseLook makes the store refuse the record of look number n (from 1) at the caps, a step of a phase: a record that holds no error.
func refuseLook(t *testing.T, db *sql.DB, n int) {
	t.Helper()

	_, err := db.ExecContext(t.Context(), fmt.Sprintf("CREATE TRIGGER refuse_outcome BEFORE INSERT ON operation_outputs WHEN NEW.function_name = '%s' AND NEW.error IS NULL "+
		"AND (SELECT count(*) FROM operation_outputs WHERE function_name = '%s') = %d BEGIN SELECT RAISE(ABORT, '%s'); END", capLookStepName, capLookStepName, n-1, refusedText))
	require.NoError(t, err)
}

// refusedFlow is a flow of the resume table: its harness, its file (empty for the fix lane of the harness) and its control result.
type refusedFlow struct {
	results []agent.Result
	body    string
	agents  int
	cost    float64
}

// refusedOutcome is what a run of the resume table left: the error of the first run and of the resume, the agent runs and the cost of the run.
type refusedOutcome struct {
	first, resumed error
	agents         int
	cost           float64
}

// runRefusedFlow runs the flow once with the store refusing the outcome record that refuse names, and resumes it with the store healed.
func runRefusedFlow(t *testing.T, f refusedFlow, refuse func(*sql.DB)) refusedOutcome {
	t.Helper()

	var (
		h    *harness
		file string
	)

	if f.body == "" {
		h = limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	} else {
		h = newAgentRig(t).h
		file = capFile(t, f.body)
	}

	inTurn(h, f.results...)

	engine := h.engine(t)
	refuse(sqlOf(t, h))

	var out refusedOutcome

	if file == "" {
		_, out.first = engine.Run(testItem, testProject)
	} else {
		_, out.first = engine.RunFile(testItem, testProject, file, modeOff())
	}

	liftRefusal(t, h)

	_, out.resumed = resumeAndJoin(t, engine, h.client(t, testVersion))

	spend, err := runAndBelowSpend(viewOf(engine.ctx, engine.dir), testRun)
	require.NoError(t, err)

	out.agents, out.cost = len(h.agents.seen()), spend.Cost

	return out
}

func TestRunStep_AResumeAfterTheStoreRefusedTheRecordOfAnyStepEndsAsTheRunWithNoRefusal(t *testing.T) {
	t.Parallel()

	one := refusedFlow{results: []agent.Result{paidRun}, agents: 1, cost: 0.3}
	limitedOnce := refusedFlow{results: []agent.Result{limited(limitReset, 0.2), paidRun}, agents: 2, cost: 0.5}
	two := refusedFlow{
		results: []agent.Result{paidRun}, agents: 2, cost: 0.6,
		body: bodyFlow("agent(model = \"m\", prompt = \"prompts/a.md\", name = \"a\")\nagent(model = \"m\", prompt = \"prompts/a.md\", name = \"b\")\n"),
	}

	// A step names an item step; a look names the look at the caps of the nth phase of the run.
	cases := map[string]struct {
		flow refusedFlow
		step string
		look int
	}{
		"one_call_origin":         {one, originStepName, 0},
		"one_call_claim":          {one, "claim", 0},
		"one_call_item":           {one, "item", 0},
		"one_call_login":          {one, "login#1", 0},
		"one_call_render":         {one, "agent:fix#1/render", 0},
		"one_call_clear":          {one, "agent:fix#1/clear1", 0},
		"one_call_look":           {one, "", 1},
		"one_call_read":           {one, "agent:fix#1/read1", 0},
		"one_call_worktree":       {one, "worktree", 0},
		"one_call_release":        {one, "release", 0},
		"limited_limit":           {limitedOnce, "agent:fix#1/limit1.0", 0},
		"limited_second_look":     {limitedOnce, "", 2},
		"limited_read":            {limitedOnce, "agent:fix#1/read1", 0},
		"two_calls_first_render":  {two, "agent:a#1/render", 0},
		"two_calls_first_clear":   {two, "agent:a#1/clear1", 0},
		"two_calls_first_look":    {two, "", 1},
		"two_calls_first_read":    {two, "agent:a#1/read1", 0},
		"two_calls_second_render": {two, "agent:b#2/render", 0},
		"two_calls_second_clear":  {two, "agent:b#2/clear1", 0},
		"two_calls_second_look":   {two, "", 2},
		"two_calls_second_read":   {two, "agent:b#2/read1", 0},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			refuse := func(db *sql.DB) { refuseOutcome(t, db, tc.step) }
			if tc.look > 0 {
				refuse = func(db *sql.DB) { refuseLook(t, db, tc.look) }
			}

			got := runRefusedFlow(t, tc.flow, refuse)

			require.ErrorContains(t, got.first, refusedText, "the first run fails at the refused record")
			require.NoError(t, got.resumed, "the resume reruns the refused step and keeps no record after it")
			assert.Equal(t, tc.flow.agents, got.agents, "the agent runs of the run with no refusal")
			assert.InDelta(t, tc.flow.cost, got.cost, 1e-9, "each agent run counts once")
		})
	}
}

// refusalRig is a DBOS context on a fresh store with one workflow, the engine whose step wrapper the workflow uses, its log and the store.
type refusalRig struct {
	ctx    dbos.Context
	db     *sql.DB
	engine *Engine
	logged *bytes.Buffer
}

// newRefusalRig registers the workflow that body makes from the engine of the rig, and launches the context.
func newRefusalRig(t *testing.T, body func(e *Engine) dbos.Workflow[int, int]) *refusalRig {
	t.Helper()

	h := newHarness(t)

	state, err := openStore(h.opts)
	require.NoError(t, err)

	// The test is over at the cleanup, so an error of the close changes no result.
	t.Cleanup(func() { _ = state.log.Close() })

	rig := &refusalRig{db: sqlOf(t, h), logged: &bytes.Buffer{}}
	rig.engine = &Engine{logger: newLogger(rig.logged, runRedactor(nil)), redact: agent.Redactor()}

	rig.ctx, err = dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: applicationVersion(testVersion), ExecutorID: executorName,
	})
	require.NoError(t, err)

	dbos.RegisterWorkflow(rig.ctx, body(rig.engine), dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(rig.ctx))
	t.Cleanup(func() { require.NoError(t, dbos.Shutdown(rig.ctx, shutdownTimeout)) })

	return rig
}

// run starts the workflow as id and returns its error.
func (r *refusalRig) run(t *testing.T, flow dbos.Workflow[int, int], id string) error {
	t.Helper()

	handle, err := dbos.RunWorkflow(r.ctx, flow, 0, dbos.WithWorkflowID(id))
	require.NoError(t, err)

	_, err = resultWithin(t, handle)

	return err
}

// steps lists the records of the run id as id:name, with !E on a record that holds an error.
func (r *refusalRig) steps(t *testing.T, id string) string {
	t.Helper()

	records, err := dbos.GetWorkflowSteps(r.ctx, id)
	require.NoError(t, err)

	lines := make([]string, 0, len(records))

	for _, step := range records {
		mark := ""
		if step.Error != nil {
			mark = "!E"
		}

		lines = append(lines, fmt.Sprintf("%d:%s%s", step.StepID, step.StepName, mark))
	}

	return strings.Join(lines, " ")
}

// errStepFails is the error of a step function that always fails.
var errStepFails = errors.New("the step fails")

// twoSteps is a workflow of a step named first that fn runs, then a step named after.
func twoSteps(fn func(context.Context) (int, error), opts ...dbos.StepOption) func(e *Engine) dbos.Workflow[int, int] {
	return func(e *Engine) dbos.Workflow[int, int] {
		return func(ctx dbos.Context, _ int) (int, error) {
			if _, err := runStep(e, ctx, "first", fn, opts...); err != nil {
				return 0, err
			}

			return runStep(e, ctx, "after", func(context.Context) (int, error) { return 1, nil })
		}
	}
}

func TestRunStep_ARefusedRecordIsFollowedByTheFailureOfTheStepUnderTheSameName(t *testing.T) {
	t.Parallel()

	var flow dbos.Workflow[int, int]

	rig := newRefusalRig(t, func(e *Engine) dbos.Workflow[int, int] {
		flow = twoSteps(func(context.Context) (int, error) { return 1, nil })(e)

		return flow
	})
	refuseOutcome(t, rig.db, "first")

	err := rig.run(t, flow, "refused-once")

	require.ErrorContains(t, err, refusedText, "the caller gets the refusal of the record")
	assert.Equal(t, "1:first!E", rig.steps(t, "refused-once"), "the failure of the step is at the next id, and the run stops")
	assert.NotContains(t, rig.logged.String(), "cannot record the failure", "the failure record was written")
}

func TestRunStep_AStepThatRunsOutOfRetriesKeepsTheOneRecordThatDBOSWrites(t *testing.T) {
	t.Parallel()

	var flow dbos.Workflow[int, int]

	rig := newRefusalRig(t, func(e *Engine) dbos.Workflow[int, int] {
		// Each try fails with an error of its own, so the end of the retries wraps a value that differs from the first.
		flow = twoSteps(func(context.Context) (int, error) { return 0, fmt.Errorf("try: %w", errStepFails) },
			dbos.WithStepMaxRetries(1), dbos.WithStepBaseInterval(time.Millisecond), dbos.WithStepMaxInterval(time.Millisecond))(e)

		return flow
	})

	err := rig.run(t, flow, "out-of-retries")

	require.ErrorIs(t, err, dbos.ErrMaxStepRetriesExceeded)
	assert.Equal(t, "0:first!E", rig.steps(t, "out-of-retries"), "DBOS recorded the end of the retries, so the step writes no record of its own")
}

func TestRunStep_AStepThatFailsKeepsTheOneRecordThatDBOSWrites(t *testing.T) {
	t.Parallel()

	var flow dbos.Workflow[int, int]

	rig := newRefusalRig(t, func(e *Engine) dbos.Workflow[int, int] {
		flow = twoSteps(func(context.Context) (int, error) { return 0, errStepFails })(e)

		return flow
	})

	err := rig.run(t, flow, "fails")

	require.ErrorIs(t, err, errStepFails)
	assert.Equal(t, "0:first!E", rig.steps(t, "fails"), "DBOS recorded the error of the step, so the step writes no record of its own")
}

// uncomparableError is an error whose values cannot be compared with ==, and whose text holds a secret.
type uncomparableError struct{ lines []string }

func (u uncomparableError) Error() string { return strings.Join(u.lines, " ") }

func TestRunStep_AStepErrorWithASecretOfATypeThatCannotBeComparedKeepsTheOneRecordThatDBOSWrites(t *testing.T) {
	t.Parallel()

	var flow dbos.Workflow[int, int]

	rig := newRefusalRig(t, func(e *Engine) dbos.Workflow[int, int] {
		flow = twoSteps(func(context.Context) (int, error) { return 0, uncomparableError{lines: []string{"token", testSecret}} })(e)

		return flow
	})

	err := rig.run(t, flow, "uncomparable")

	require.ErrorContains(t, err, "token")
	assert.NotContains(t, err.Error(), testSecret, "the step error is redacted")
	assert.Equal(t, "0:first!E", rig.steps(t, "uncomparable"), "the check of the recorded error does not panic, and the step writes no record of its own")
}

func TestRunStep_ARunCanceledDuringAStepRecordsNoFailureOfTheStep(t *testing.T) {
	t.Parallel()

	var flow dbos.Workflow[int, int]

	started := make(chan struct{})

	rig := newRefusalRig(t, func(e *Engine) dbos.Workflow[int, int] {
		flow = twoSteps(func(c context.Context) (int, error) {
			close(started)
			hang(c.Done())

			return 1, nil
		})(e)

		return flow
	})
	refuseOutcome(t, rig.db, "first")

	runCtx, cancel := dbos.WithCancelCause(rig.ctx)

	handle, err := dbos.RunWorkflow(runCtx, flow, 0, dbos.WithWorkflowID("canceled"))
	require.NoError(t, err)

	receive(t, started)
	cancel(errStepFails)

	_, err = resultWithin(t, handle)

	require.ErrorIs(t, err, dbos.ErrWorkflowCancelled)
	assert.Empty(t, rig.steps(t, "canceled"), "a canceled run records no step, so a resume runs the step again")
	assert.NotContains(t, rig.logged.String(), "cannot record the failure", "the step tried no failure record")
}

func TestRunStep_AResumeThatReplaysAFailedStepTheRunToleratesRecordsNoSecondFailure(t *testing.T) {
	t.Parallel()

	var (
		flow    dbos.Workflow[int, int]
		runs    atomic.Int32
		started = make(chan struct{})
	)

	rig := newRefusalRig(t, func(e *Engine) dbos.Workflow[int, int] {
		flow = func(ctx dbos.Context, _ int) (int, error) {
			if _, err := runStep(e, ctx, "first", func(context.Context) (int, error) { return 0, errStepFails }); err != nil {
				e.logger.Info("the run goes on after a failed step", "error", err)
			}

			return runStep(e, ctx, "after", func(c context.Context) (int, error) {
				if runs.Add(1) == 1 {
					close(started)
					hang(c.Done())
				}

				return 1, nil
			})
		}

		return flow
	})

	runCtx, cancel := dbos.WithCancelCause(rig.ctx)

	handle, err := dbos.RunWorkflow(runCtx, flow, 0, dbos.WithWorkflowID("replayed"))
	require.NoError(t, err)

	receive(t, started)
	cancel(errStepFails)

	_, err = resultWithin(t, handle)
	require.ErrorIs(t, err, dbos.ErrWorkflowCancelled)
	require.Equal(t, "0:first!E", rig.steps(t, "replayed"), "the canceled run keeps the failure of the first step only")

	again, err := dbos.ResumeWorkflow[int](rig.ctx, "replayed")
	require.NoError(t, err)

	_, err = resultWithin(t, again)
	require.NoError(t, err)
	assert.Equal(t, "0:first!E 1:after", rig.steps(t, "replayed"), "the replay of the failed step writes no record, so the next step keeps its id")
}

func TestRunStep_AFailureRecordThatTheStoreRefusesTooIsLoggedAndTheRefusalReturns(t *testing.T) {
	t.Parallel()

	var flow dbos.Workflow[int, int]

	rig := newRefusalRig(t, func(e *Engine) dbos.Workflow[int, int] {
		flow = twoSteps(func(context.Context) (int, error) { return 1, nil })(e)

		return flow
	})

	_, err := rig.db.ExecContext(t.Context(), "CREATE TRIGGER refuse_every BEFORE INSERT ON operation_outputs WHEN NEW.function_name = 'first' "+
		"BEGIN SELECT RAISE(ABORT, '"+refusedText+"'); END")
	require.NoError(t, err)

	err = rig.run(t, flow, "refused-twice")

	require.ErrorContains(t, err, refusedText, "the caller gets the first refusal")
	assert.Empty(t, rig.steps(t, "refused-twice"), "the store kept neither record")
	assert.Contains(t, rig.logged.String(), "cannot record the failure of a step whose record the store refused", "the lost failure is reported")
	assert.Equal(t, 1, strings.Count(rig.logged.String(), "cannot record the failure"), "the warning is logged once")
}
