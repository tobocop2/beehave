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

// blockingAgents starts a real sleeper, reports it, and returns only when the test lets it.
type blockingAgents struct {
	pid     chan int
	proceed chan struct{}
}

func (b blockingAgents) Run(ctx context.Context, request AgentRequest) (agent.Result, error) {
	cmd := proc.GroupCommand(context.WithoutCancel(ctx), "sleep", []string{"60"}, "")
	if err := cmd.Start(); err != nil {
		return agent.Result{}, err
	}

	go func() { _ = cmd.Wait() }() //nolint:errcheck // the exit status of the killed sleeper is not read

	request.Started(cmd.Process.Pid)
	b.pid <- cmd.Process.Pid
	<-b.proceed

	return agent.Result{Outcome: agent.OutcomeExited}, nil
}

func newProcEngine(t *testing.T, agents Agents) *Engine {
	t.Helper()

	return &Engine{deps: Deps{Agents: agents}, procs: newGroups(t), logger: newLogger(io.Discard)}
}

func TestRunAgent_AGroupDiesWhenThePhaseIsCancelled(t *testing.T) {
	t.Parallel()

	agents := blockingAgents{pid: make(chan int, 1), proceed: make(chan struct{})}
	engine := newProcEngine(t, agents)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)

	go func() {
		_, err := engine.runAgent(ctx, PhaseInput{Item: testItem, Step: "implement"}, "")
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
		_, err := engine.runAgent(ctx, PhaseInput{Item: testItem, Step: "implement"}, "")
		done <- err
	}()

	requireDies(t, <-agents.pid)
	close(agents.proceed)
	require.NoError(t, <-done)
}

func TestRunAgent_AGroupDiesWhenThePhaseEndsAndTheFileIsEmpty(t *testing.T) {
	t.Parallel()

	agents := blockingAgents{pid: make(chan int, 1), proceed: make(chan struct{})}
	close(agents.proceed)

	engine := newProcEngine(t, agents)

	_, err := engine.runAgent(t.Context(), PhaseInput{Item: testItem, Step: "implement"}, "")

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

	_, err := engine.runAgent(t.Context(), PhaseInput{Item: testItem, Step: "implement"}, "")

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

func TestGroups_ReleaseKillsOnlyTheGroupsOfItsOwner(t *testing.T) {
	t.Parallel()

	g := newGroups(t)
	mine, other := startSleeper(t), startSleeper(t)
	require.NoError(t, g.add(t.Context(), "mine", mine))
	require.NoError(t, g.add(t.Context(), "other", other))

	require.NoError(t, g.release("mine"))

	requireDies(t, mine)
	assert.True(t, alive(other))
	assert.Len(t, g.rows, 1)
}
