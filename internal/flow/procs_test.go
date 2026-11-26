package flow

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/ledger"
	"github.com/tobocop2/beehave/internal/proc"
)

const (
	groupPoll = 10 * time.Millisecond
	// groupStay is longer than the time a SIGKILL needs to land and be reaped, so a kill sent by the code under test is seen inside the window.
	groupStay = 500 * time.Millisecond
)

// startSleeper starts a real sleep in its own process group and reaps it when it dies.
func startSleeper(t *testing.T) int {
	t.Helper()

	cmd := proc.GroupCommand(context.Background(), "sleep", []string{"60"}, t.TempDir())
	require.NoError(t, cmd.Start())

	go func() { _ = cmd.Wait() }() //nolint:errcheck // the exit status of the killed sleeper is not read

	t.Cleanup(func() { _ = proc.KillGroupID(cmd.Process.Pid) }) //nolint:errcheck // ESRCH means the test killed it already

	return cmd.Process.Pid
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func requireDies(t *testing.T, pid int) {
	t.Helper()

	waitOn(t, waiting[struct{}]{what: "process " + strconv.Itoa(pid) + " to die", read: func() (struct{}, bool, error) { return struct{}{}, !alive(pid), nil }})
}

// requireStayAlive fails when any of the processes dies inside the window groupStay.
func requireStayAlive(t *testing.T, msg string, pids ...int) {
	t.Helper()

	require.Never(t, func() bool {
		for _, pid := range pids {
			if !alive(pid) {
				return true
			}
		}

		return false
	}, groupStay, groupPoll, msg)
}

func newGroups(t *testing.T) *groups {
	t.Helper()

	return &groups{path: groupsFile(t.TempDir())}
}

func writeRow(t *testing.T, g *groups, pid int, started string) {
	t.Helper()
	require.NoError(t, os.WriteFile(g.path, []byte(strconv.Itoa(pid)+rowSeparator+started+"\n"), pgidFileMode))
}

func TestGroups_ReapKillsTheGroupAnEarlierWorkerLeft(t *testing.T) {
	t.Parallel()

	g := newGroups(t)
	pid := startSleeper(t)
	started, err := proc.StartTime(t.Context(), pid)
	require.NoError(t, err)
	writeRow(t, g, pid, started)

	killed, err := g.reap(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 1, killed)
	requireDies(t, pid)

	data, err := os.ReadFile(g.path)
	require.NoError(t, err)
	assert.Empty(t, data, "the file is empty after the reap")
}

func TestGroups_ReapSparesAProcessThatReusedTheIdWithAnotherStartTime(t *testing.T) {
	t.Parallel()

	g := newGroups(t)
	pid := startSleeper(t)
	writeRow(t, g, pid, "Thu Jan  1 00:00:00 1970")

	killed, err := g.reap(t.Context())

	require.NoError(t, err)
	assert.Zero(t, killed)
	assert.True(t, alive(pid), "the unrelated process must live")
}

func TestGroups_ReapSkipsLinesThatDoNotParseAndAMissingFile(t *testing.T) {
	t.Parallel()

	g := newGroups(t)
	killed, err := g.reap(t.Context())
	require.NoError(t, err)
	assert.Zero(t, killed)

	require.NoError(t, os.WriteFile(g.path, []byte("junk\nx"+rowSeparator+"y\n0"+rowSeparator+"y\n"), pgidFileMode))

	killed, err = g.reap(t.Context())
	require.NoError(t, err)
	assert.Zero(t, killed)
}

func TestGroups_ReapReportsAnUnreadableFile(t *testing.T) {
	t.Parallel()

	g := &groups{path: t.TempDir()}

	_, err := g.reap(t.Context())

	require.Error(t, err)
}

func TestGroups_AddKeepsTheFilePrivateAndRecordsTheStartTime(t *testing.T) {
	t.Parallel()

	g := newGroups(t)
	pid := startSleeper(t)

	require.NoError(t, g.add(t.Context(), &attempt{id: "12/implement/0"}, pid))

	info, err := os.Stat(g.path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(pgidFileMode), info.Mode().Perm())

	started, err := proc.StartTime(t.Context(), pid)
	require.NoError(t, err)

	data, err := os.ReadFile(g.path)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(pid)+rowSeparator+started+"\n", string(data))
}

func TestGroups_AddRefusesAProcessThatIsGone(t *testing.T) {
	t.Parallel()

	g := newGroups(t)

	require.ErrorIs(t, g.add(t.Context(), &attempt{id: "12/implement/0"}, 1<<22), proc.ErrNoProcess)
}

func TestGroups_WriteReportsAFolderThatIsGone(t *testing.T) {
	t.Parallel()

	g := &groups{path: filepath.Join(t.TempDir(), "missing", pgidFileName)}

	require.Error(t, g.write())
}

func TestGroups_KillAllKillsEveryRecordedGroup(t *testing.T) {
	t.Parallel()

	g := newGroups(t)
	first, second := startSleeper(t), startSleeper(t)
	require.NoError(t, g.add(t.Context(), &attempt{id: "a"}, first))
	require.NoError(t, g.add(t.Context(), &attempt{id: "b"}, second))

	require.NoError(t, g.killAll())

	requireDies(t, first)
	requireDies(t, second)
}

// blockingAgents starts a real sleeper under the context of the request, as the runner of the agent does, reports it, and returns only when the test lets it.
type blockingAgents struct {
	pid     chan int
	proceed chan struct{}
}

func (b blockingAgents) Run(ctx context.Context, request AgentRequest) (agent.Result, error) {
	cmd := proc.GroupCommand(ctx, "sleep", []string{"60"}, "")
	if err := cmd.Start(); err != nil {
		return agent.Result{}, err
	}

	go func() { _ = cmd.Wait() }() //nolint:errcheck // the exit status of the killed sleeper is not read

	request.Started(cmd.Process.Pid)
	b.pid <- cmd.Process.Pid
	hang(b.proceed)

	return agent.Result{Outcome: agent.OutcomeExited}, nil
}

// procRun is the run that holds a trusted claim in an engine from newProcEngine: the phases of these tests belong to it.
const procRun = "run-proc"

func newProcEngine(t *testing.T, agents agents) *Engine {
	t.Helper()

	held, err := ledger.Open(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, held.Close()) })

	engine := &Engine{deps: Deps{Agents: agents}, procs: newGroups(t), logger: newLogger(io.Discard, runRedactor(nil)), ledger: held}
	holdClaim(engine, procRun, claimHold{renewed: time.Now()})

	return engine
}

// runAgentOf waits for the claim of the run of the phase, then runs the agent of the phase under it, as the phase does.
func runAgentOf(ctx context.Context, engine *Engine, in PhaseInput) (PhaseOutput, error) {
	claim, holder, err := engine.awaitClaim(ctx, in)
	if err != nil {
		return PhaseOutput{}, err
	}

	return engine.runAgent(ctx, claim, holder, in, nil)
}

func TestRunAgent_AGroupDiesWhenThePhaseIsCancelled(t *testing.T) {
	t.Parallel()

	agents := blockingAgents{pid: make(chan int, 1), proceed: make(chan struct{})}
	engine := newProcEngine(t, agents)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)

	go func() {
		_, err := runAgentOf(ctx, engine, PhaseInput{Run: procRun, Item: testItem})
		done <- err
	}()

	pid := receive(t, agents.pid)
	cancel()
	requireDies(t, pid)
	close(agents.proceed)
	require.NoError(t, receive(t, done))
}

func TestRunAgent_AGroupDiesWhenThePhaseTimesOut(t *testing.T) {
	t.Parallel()

	agents := blockingAgents{pid: make(chan int, 1), proceed: make(chan struct{})}
	engine := newProcEngine(t, agents)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)

	defer cancel()

	done := make(chan error, 1)

	go func() {
		_, err := runAgentOf(ctx, engine, PhaseInput{Run: procRun, Item: testItem})
		done <- err
	}()

	requireDies(t, receive(t, agents.pid))
	close(agents.proceed)
	require.NoError(t, receive(t, done))
}

func TestRunAgent_APhaseThatEndsEmptiesTheFileAndTheEndOfItsContextKillsTheGroup(t *testing.T) {
	t.Parallel()

	agents := blockingAgents{pid: make(chan int, 1), proceed: make(chan struct{})}
	close(agents.proceed)

	engine := newProcEngine(t, agents)

	_, err := runAgentOf(t.Context(), engine, PhaseInput{Run: procRun, Item: testItem})

	require.NoError(t, err)

	requireDies(t, receive(t, agents.pid))

	data, err := os.ReadFile(engine.procs.path)
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestRunAgent_LogsAnAgentThatCannotBeRecorded(t *testing.T) {
	t.Parallel()

	engine := newProcEngine(t, agentFunc(func(_ context.Context, request AgentRequest) (agent.Result, error) {
		request.Started(1 << 22)

		return agent.Result{Outcome: agent.OutcomeExited}, nil
	}))

	_, err := runAgentOf(t.Context(), engine, PhaseInput{Run: procRun, Item: testItem})

	require.NoError(t, err)
}

func TestEndGroups_LogsAFileThatCannotBeWritten(t *testing.T) {
	t.Parallel()

	engine := newProcEngine(t, nil)
	engine.procs.path = filepath.Join(t.TempDir(), "missing", pgidFileName)

	var logged bytes.Buffer

	engine.logger = newLogger(&logged, runRedactor(nil))

	engine.endGroups(&attempt{id: "x"})

	assert.Contains(t, logged.String(), "cannot update the agent process file")
}

type agentFunc func(context.Context, AgentRequest) (agent.Result, error)

func (f agentFunc) Run(ctx context.Context, request AgentRequest) (agent.Result, error) {
	return f(ctx, request)
}

func TestRunAgent_ALostClaimKillsTheGroupWhileTheRunStaysOpen(t *testing.T) {
	t.Parallel()

	agents := blockingAgents{pid: make(chan int, 1), proceed: make(chan struct{})}
	engine := newProcEngine(t, agents)

	done := make(chan error, 1)

	go func() {
		_, err := runAgentOf(t.Context(), engine, PhaseInput{Run: procRun, Item: testItem})
		done <- err
	}()

	pid := receive(t, agents.pid)

	// The run is not dropped and the phase does not end: only the loss ends the context of the claim.
	engine.holds.fail(claimAttempt(engine, procRun), "the claim is lost")
	requireDies(t, pid)

	close(agents.proceed)
	require.NoError(t, receive(t, done))
}

func TestHolds_AddEndsTheContextOfTheRowItReplaces(t *testing.T) {
	t.Parallel()

	var h holds

	earlier, later := &attempt{id: "run"}, &attempt{id: "run"}

	first := h.add(earlier, claimHold{})

	h.add(later, claimHold{})

	require.Error(t, first.Err(), "the first context is done")
	require.NoError(t, h.rows[later].ctx.Err(), "the new row keeps a live context")
}

// runsOf returns the run of each row.
func runsOf(rows []groupRow) []string {
	runs := make([]string, 0, len(rows))

	for _, row := range rows {
		runs = append(runs, row.attempt.id)
	}

	return runs
}

// withoutStart returns the rows with the start time of each leader cleared, so a test compares the group and the run.
func withoutStart(rows []groupRow) []groupRow {
	out := make([]groupRow, 0, len(rows))

	for _, row := range rows {
		row.started = ""
		out = append(out, row)
	}

	return out
}

func TestGroups_ReleaseForgetsEveryGroupOfTheRunAndKillsNone(t *testing.T) {
	t.Parallel()

	g := newGroups(t)
	first, second := startSleeper(t), startSleeper(t)
	longer, other := startSleeper(t), startSleeper(t)

	r1, r10, r13 := &attempt{id: "item-12-r1"}, &attempt{id: "item-12-r10"}, &attempt{id: "item-13-r1"}

	for a, pid := range map[*attempt]int{r1: first, r10: longer, r13: other} {
		require.NoError(t, g.add(t.Context(), a, pid))
	}

	require.NoError(t, g.add(t.Context(), r1, second))

	require.NoError(t, g.release(r1))

	requireStayAlive(t, "a release kills no group", first, second, longer, other)
	assert.ElementsMatch(t, []groupRow{{pgid: longer, attempt: r10}, {pgid: other, attempt: r13}}, withoutStart(g.rows), "run r10 is not run r1")
}

func TestGroups_ReleaseOfAnEarlierAttemptKeepsTheGroupsOfTheResumedOne(t *testing.T) {
	t.Parallel()

	g := newGroups(t)
	earlier, resumed := &attempt{id: "item-12-r1"}, &attempt{id: "item-12-r1"}
	mine, theirs := startSleeper(t), startSleeper(t)

	require.NoError(t, g.add(t.Context(), earlier, mine))
	require.NoError(t, g.add(t.Context(), resumed, theirs))

	require.NoError(t, g.release(earlier))

	require.Len(t, g.rows, 1, "the release of the earlier attempt forgot the groups of the resumed attempt of the same run id")
	assert.Same(t, resumed, g.rows[0].attempt)
	assert.Equal(t, theirs, g.rows[0].pgid)
}
