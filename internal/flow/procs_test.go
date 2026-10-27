package flow

import (
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

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/proc"
)

const (
	groupWait = 5 * time.Second
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

	require.Eventually(t, func() bool { return !alive(pid) }, groupWait, groupPoll, "process %d is still alive", pid)
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

	require.NoError(t, g.add(t.Context(), "12/implement/0", pid))

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

	require.ErrorIs(t, g.add(t.Context(), "12/implement/0", 1<<22), proc.ErrNoProcess)
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
	require.NoError(t, g.add(t.Context(), "a", first))
	require.NoError(t, g.add(t.Context(), "b", second))

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
	<-b.proceed

	return agent.Result{Outcome: agent.OutcomeExited}, nil
}

// procRun is the run that holds a trusted claim in an engine from newProcEngine: the phases of these tests belong to it.
const procRun = "run-proc"

func newProcEngine(t *testing.T, agents agents) *Engine {
	t.Helper()

	engine := &Engine{deps: Deps{Agents: agents}, procs: newGroups(t), logger: newLogger(io.Discard)}
	engine.holds.add(procRun, claimHold{renewed: time.Now()})

	return engine
}

// runAgentOf waits for the claim of the run of the phase, then runs the agent of the phase under it, as the phase does.
func runAgentOf(ctx context.Context, engine *Engine, in PhaseInput) (PhaseOutput, error) {
	claim, err := engine.awaitClaim(ctx, in)
	if err != nil {
		return PhaseOutput{}, err
	}

	return engine.runAgent(ctx, claim, in, "")
}

func TestRunAgent_AGroupDiesWhenThePhaseIsCancelled(t *testing.T) {
	t.Parallel()

	agents := blockingAgents{pid: make(chan int, 1), proceed: make(chan struct{})}
	engine := newProcEngine(t, agents)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)

	go func() {
		_, err := runAgentOf(ctx, engine, PhaseInput{Run: procRun, Item: testItem, Step: "implement"})
		done <- err
	}()

	pid := <-agents.pid
	cancel()
	requireDies(t, pid)
	close(agents.proceed)
	require.NoError(t, <-done)
}

func TestRunAgent_AGroupDiesWhenThePhaseTimesOut(t *testing.T) {
	t.Parallel()

	agents := blockingAgents{pid: make(chan int, 1), proceed: make(chan struct{})}
	engine := newProcEngine(t, agents)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)

	defer cancel()

	done := make(chan error, 1)

	go func() {
		_, err := runAgentOf(ctx, engine, PhaseInput{Run: procRun, Item: testItem, Step: "implement"})
		done <- err
	}()

	requireDies(t, <-agents.pid)
	close(agents.proceed)
	require.NoError(t, <-done)
}

func TestRunAgent_APhaseThatEndsEmptiesTheFileAndTheEndOfItsContextKillsTheGroup(t *testing.T) {
	t.Parallel()

	agents := blockingAgents{pid: make(chan int, 1), proceed: make(chan struct{})}
	close(agents.proceed)

	engine := newProcEngine(t, agents)

	_, err := runAgentOf(t.Context(), engine, PhaseInput{Run: procRun, Item: testItem, Step: "implement"})

	require.NoError(t, err)

	requireDies(t, <-agents.pid)

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

	_, err := runAgentOf(t.Context(), engine, PhaseInput{Run: procRun, Item: testItem, Step: "implement"})

	require.NoError(t, err)
}

func TestEndGroups_LogsAFileThatCannotBeWritten(t *testing.T) {
	t.Parallel()

	engine := newProcEngine(t, nil)
	engine.procs.path = filepath.Join(t.TempDir(), "missing", pgidFileName)

	engine.endGroups("x")
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
		_, err := runAgentOf(t.Context(), engine, PhaseInput{Run: procRun, Item: testItem, Step: "implement"})
		done <- err
	}()

	pid := <-agents.pid

	// The run is not dropped and the phase does not end: only the loss ends the context of the claim.
	engine.holds.fail(procRun, "the claim is lost")
	requireDies(t, pid)

	close(agents.proceed)
	require.NoError(t, <-done)
}

func TestHolds_AddEndsTheContextOfTheRowItReplaces(t *testing.T) {
	t.Parallel()

	var h holds

	h.add("run", claimHold{})

	first := h.rows["run"].ctx

	h.add("run", claimHold{})

	require.Error(t, first.Err(), "the first context is done")
	require.NoError(t, h.rows["run"].ctx.Err(), "the new row keeps a live context")
}

// runsOf returns the run of each row.
func runsOf(rows []groupRow) []string {
	runs := make([]string, 0, len(rows))

	for _, row := range rows {
		runs = append(runs, row.run)
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

	for run, pid := range map[string]int{"item-12-r1": first, "item-12-r10": longer, "item-13-r1": other} {
		require.NoError(t, g.add(t.Context(), run, pid))
	}

	require.NoError(t, g.add(t.Context(), "item-12-r1", second))

	require.NoError(t, g.release("item-12-r1"))

	requireStayAlive(t, "a release kills no group", first, second, longer, other)
	assert.ElementsMatch(t, []groupRow{{pgid: longer, run: "item-12-r10"}, {pgid: other, run: "item-13-r1"}}, withoutStart(g.rows), "run r10 is not run r1")
}
