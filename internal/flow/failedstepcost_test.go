package flow

// A helper process runs the item and is killed with SIGKILL at one store write: a trigger on the table that the write goes to calls the
// function stopcost_kill, which kills its own process inside the transaction of that write. The write and everything after it are lost;
// every write before it is committed. The test process then recovers the run with a second engine over the same state folder, reads the
// four totals, resumes the run and reads the look at the run cap before step b.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"

	"github.com/tobocop2/beehave/internal/agent"
)

// The flags of the helper process. The state folder is empty in the test process, so a trigger left in a store never kills the test process.
var (
	stopCostState = flag.String("stopcost-state", "", "the state folder that the helper process of the stop-cost test runs the item in")
	stopCostFlow  = flag.String("stopcost-flow", "", "the flow file that the helper process of the stop-cost test runs")
	stopCostKind  = flag.String("stopcost-kind", "", "the end of the fake agent in the helper process of the stop-cost test")
	stopCostTree  = flag.String("stopcost-tree", "", "the item folder that the helper process of the stop-cost test uses")
)

// errStopCostKill is the registration of stopcost_kill. It runs when the package loads, before any connection opens.
var errStopCostKill = sqlite.RegisterScalarFunction("stopcost_kill", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
	if *stopCostState == "" {
		return int64(0), nil
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		return nil, err
	}

	time.Sleep(time.Hour)

	return int64(0), nil
})

// stopCostPoints holds the triggers of each stop point. Each one fires inside the transaction of the write it names.
var stopCostPoints = map[string][]string{
	// none: no stop. The control of every other row.
	"none": nil,
	// between: at the first write that the phase makes after the record of its failed agent step: an event of the phase, or the end of the phase.
	"between": {
		`CREATE TRIGGER stopcost_1 BEFORE INSERT ON workflow_events WHEN NEW.workflow_uuid <> '` + testRun + `' BEGIN SELECT stopcost_kill(); END`,
		`CREATE TRIGGER stopcost_2 BEFORE UPDATE ON workflow_status WHEN NEW.status = 'ERROR' AND NEW.workflow_uuid <> '` + testRun + `' BEGIN SELECT stopcost_kill(); END`,
	},
	// after-event: after the phase ended, inside the write of the record of the result of the phase in the item run.
	"after-event": {`CREATE TRIGGER stopcost_1 BEFORE INSERT ON operation_outputs WHEN NEW.function_name = 'DBOS.getResult' AND NEW.error IS NOT NULL BEGIN SELECT stopcost_kill(); END`},
	// read: a run that ended well, inside the write of its read step.
	"read": {`CREATE TRIGGER stopcost_1 BEFORE INSERT ON operation_outputs WHEN NEW.function_name LIKE '%/read%' BEGIN SELECT stopcost_kill(); END`},
}

// stopCostFile is two agent calls. A failed a ends the item run; a resume reruns a, and the look before b reads the run cap.
const stopCostFile = "def main(item):\n    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"a\")\n    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"b\")\n"

// stopCostCap is the run cap. Under it at the start of a; past it once a failed run of $0.70 counts.
const stopCostCap = 0.5

// stopCostResult is the end of a run of the fake agent for a kind of case.
func stopCostResult(kind string) agent.Result {
	switch kind {
	case "limit-no-reset":
		return limited(time.Time{}, 0.7)
	case "exit-nonzero":
		failed := costing(0.7)
		failed.ExitCode = 3
		failed.Note = "bad exit"

		return failed
	default: // ok
		return costing(0.3)
	}
}

// TestStopCostHelper is the helper process. It does nothing without the flag that names its state folder.
func TestStopCostHelper(t *testing.T) {
	t.Parallel()

	if *stopCostState == "" {
		t.Skip("helper of TestAgentStep_AStopAfterAFailedRunKeepsItsCost")
	}

	require.NoError(t, errStopCostKill)

	h := newHarness(t)
	h.opts.StateDir = *stopCostState
	h.opts.MaxRunBudget = stopCostCap
	// The item folder of the test process: its HEAD is the commit that the recorded start names. A tree of its own would have another commit.
	h.trees = &fakeTrees{dir: *stopCostTree}

	h.agents.result = func(AgentRequest) agent.Result { return stopCostResult(*stopCostKind) }

	_, err := h.engine(t).RunFile(testItem, testProject, *stopCostFlow, modeOff())
	t.Logf("the helper ended its run: %v", err)
}

// stopCostCase is one row group: the end of the fake agent and the stop point.
type stopCostCase struct{ kind, stop string }

// wantedCost is the cost that every total shows once the run ended: the failed run of $0.70, or the two runs of $0.30 of a run that ended well.
func (c stopCostCase) wantedCost() float64 {
	if c.kind == "ok" {
		return 0.6
	}

	return 0.7
}

func TestAgentStep_AStopAfterAFailedRunKeepsItsCost(t *testing.T) {
	t.Parallel()
	require.NoError(t, errStopCostKill)

	var cases []stopCostCase
	for _, kind := range []string{"limit-no-reset", "exit-nonzero"} {
		for _, stop := range []string{"none", "between", "after-event"} {
			cases = append(cases, stopCostCase{kind, stop})
		}
	}

	cases = append(cases, stopCostCase{"ok", "read"}, stopCostCase{"ok", "none"})

	for _, c := range cases {
		t.Run(c.kind+"/"+c.stop, func(t *testing.T) {
			t.Parallel()
			runStopCostCase(t, c)
		})
	}
}

func runStopCostCase(t *testing.T, c stopCostCase) {
	t.Helper()

	h := newHarness(t)
	h.opts.MaxRunBudget = stopCostCap
	stateDir := h.opts.StateDir

	for _, trigger := range stopCostPoints[c.stop] {
		execStopCost(t, stateDir, trigger)
	}

	runStopCostHelper(t, h, c)
	execStopCost(t, stateDir, "DROP TRIGGER IF EXISTS stopcost_1")
	execStopCost(t, stateDir, "DROP TRIGGER IF EXISTS stopcost_2")

	// The second process: $0.30 for each later run (a rerun of a, and b).
	inTurn(h, costing(0.3))

	engine := h.engine(t)
	client := h.client(t, testVersion)

	eventually(t, func() bool {
		last, found, err := latestRun(engine.store(), testItem)

		return err == nil && found && last.Status.Status != dbos.WorkflowStatusPending && last.Status.Status != dbos.WorkflowStatusEnqueued
	})

	checkStopCostTotals(t, c, engine, client)

	if c.kind != "ok" {
		checkStopCostResume(t, h, engine, client)
	}
}

// runStopCostHelper runs the item in a helper process with the triggers of the case in place, and requires that the helper stops where the case says.
func runStopCostHelper(t *testing.T, h *harness, c stopCostCase) {
	t.Helper()

	// The helper's temporary folders live in this test's folder, so the cleanup of this test removes what a killed helper leaves.
	tmp := filepath.Join(t.TempDir(), "helper-tmp")
	require.NoError(t, os.MkdirAll(tmp, 0o700))

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	helper := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStopCostHelper$", "-test.count=1", "-stopcost-state="+h.opts.StateDir, "-stopcost-flow="+capFile(t, stopCostFile), //nolint:gosec // the test starts its own binary
		"-stopcost-kind="+c.kind, "-stopcost-tree="+h.trees.dir)
	helper.Env = append(os.Environ(), "TMPDIR="+tmp) //nolint:forbidigo // the helper keeps the environment of the test and gets a folder of its own for temporary files

	out, runErr := helper.CombinedOutput()

	killed := false

	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		status, ok := exit.Sys().(syscall.WaitStatus)
		killed = ok && status.Signaled()
	}

	assert.Equal(t, c.stop != "none", killed, "the helper must be killed at the write of the stop point, and only there; its output:\n%s", out)
}

// checkStopCostTotals requires each total of the run to hold the cost of the failed run.
func checkStopCostTotals(t *testing.T, c stopCostCase, engine *Engine, client *Client) {
	t.Helper()

	want := c.wantedCost()

	steps, err := client.Steps(testRun)
	require.NoError(t, err)
	assert.InDelta(t, want, steps.Spent.Cost, 0.001, "status view")

	runs, err := client.Status()
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.InDelta(t, want, runs[0].Spent.Cost, 0.001, "run list")

	tree, err := runAndBelowSpend(engine.store(), testRun)
	require.NoError(t, err)
	assert.InDelta(t, want, tree.Cost, 0.001, "tree sum")

	days, err := client.Days(DaysSince(time.Now(), 1))
	require.NoError(t, err)
	require.Len(t, days, 1)
	assert.InDelta(t, want, days[0].Spent.Cost, 0.001, "day sum")
}

// checkStopCostResume resumes the failed run and requires the look at the run cap before step b to count the cost of the failed run and stop the run.
func checkStopCostResume(t *testing.T, h *harness, engine *Engine, client *Client) {
	t.Helper()

	_ = resumeRun(t, engine, client)

	seenBefore := len(h.agents.seen())
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if stoppedRun(t, client, testRun) != nil || len(h.agents.seen()) > seenBefore+1 {
			break
		}
	}

	stop := stoppedRun(t, client, testRun)
	require.NotNil(t, stop, "the cost of the failed run must stop the resume at the run cap")
	assert.Equal(t, "run", string(stop.Scope))
	assert.InDelta(t, 0.7, stop.Spent, 0.001)
	assert.LessOrEqual(t, len(h.agents.seen()), seenBefore+1, "step b must not start")
}

// execStopCost runs one statement on the store of the state folder.
func execStopCost(t *testing.T, stateDir, statement string) {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(stateDir, databaseFile)+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)

	defer func() { require.NoError(t, db.Close()) }()

	_, err = db.ExecContext(t.Context(), statement)
	require.NoError(t, err)
}
