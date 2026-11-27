package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host"
)

// slotJobs holds each host job of a test until the test lets it go. The jobs report their start on started, and the log keeps the order of the starts and the ends.
type slotJobs struct {
	mu      sync.Mutex
	log     []string
	started chan string
	release map[string]chan struct{}
}

func newSlotJobs(programs ...string) *slotJobs {
	jobs := &slotJobs{started: make(chan string, len(programs)), release: map[string]chan struct{}{}}

	for _, program := range programs {
		jobs.release[program] = make(chan struct{})
	}

	return jobs
}

// hold is the before hook of the fake commands: the job records its start, then waits for its release or for its context.
func (j *slotJobs) hold(ctx context.Context, request CommandRequest) error {
	program := request.Run[0]

	j.mu.Lock()
	j.log = append(j.log, "start "+program)
	j.mu.Unlock()

	j.started <- program

	select {
	case <-j.release[program]:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// let records the end of a job, then releases it. The record comes first, so no job that waited for the slot of this one can log its start before the end.
func (j *slotJobs) let(program string) {
	j.mu.Lock()
	j.log = append(j.log, "end "+program)
	j.mu.Unlock()

	close(j.release[program])
}

func (j *slotJobs) order() []string {
	j.mu.Lock()
	defer j.mu.Unlock()

	return slices.Clone(j.log)
}

// slotFlow is the workflow file of one item: one host command of the program, in the slot class when the class is not empty.
func slotFlow(program, class string) string {
	if class == "" {
		return bodyFlow(fmt.Sprintf("command(name = \"job\", run = [%q], host = \"box\")", program))
	}

	return bodyFlow(fmt.Sprintf("command(name = \"job\", run = [%q], host = \"box\", slots = %q)", program, class))
}

// runSlotItem runs the workflow file of an item in the background and returns the channel of its end.
func runSlotItem(t *testing.T, engine *Engine, item, text string) <-chan error {
	t.Helper()

	file := writeFlow(t, text)
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(item, testProject, file, modeOff())
		ended <- err
	}()

	return ended
}

// enqueuedJobs counts the host jobs that wait in a queue.
func enqueuedJobs(t *testing.T, engine *Engine) int {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued))
	require.NoError(t, err)

	return len(rows)
}

func TestReopen_ACanceledSlotJobGoesBackToTheQueueOfItsClassInThePartitionOfItsHost(t *testing.T) {
	t.Parallel()

	job := func(in ExclusiveInput) dbos.WorkflowStatus {
		text, err := json.Marshal(in)
		require.NoError(t, err)

		return dbos.WorkflowStatus{Name: hostJobName, Input: string(text)}
	}

	queue, partition, err := gateRewindTarget(job(ExclusiveInput{Kind: kindCommand, HostKey: hostKeyPrefix + "box", SlotCount: 3, Request: CommandRequest{SlotClass: "mutation"}}))
	require.NoError(t, err)
	assert.Equal(t, "slots:mutation:3", queue)
	assert.Equal(t, "host:box", partition)

	queue, partition, err = gateRewindTarget(job(ExclusiveInput{Kind: kindGate, HostKey: hostKeyPrefix + "box", SlotCount: 2}))
	require.NoError(t, err)
	assert.Equal(t, "slots:gate:2", queue, "a build job of a gate goes back to the queue of the gate class and its count")
	assert.Equal(t, "host:box", partition)
}

func TestCommand_ASlotCommandRunsOnTheHostOfItsGrantAndRecordsItsOutput(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{Stdout: "killed 3", RC: 2}, nil }

	var asked []string

	h.slots = func(project, cluster, class string) (SlotGrant, error) {
		asked = append(asked, project+"/"+cluster+"/"+class)

		return SlotGrant{Host: "box", Count: 1}, nil
	}

	_, err := walkBody(t, h, "r = command(name = \"mutate\", run = [\"x\"], host = \"build\", slots = \"mutation\")\nif r.rc == 2 and r.stdout == \"killed 3\":\n    command(name = \"saw\", run = [\"saw-the-output\"])\n")

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "//mutation"}, asked, "the call asks for the class of its project and the host that host_up chose, empty here")
	assert.Equal(t, []string{"x", "saw-the-output"}, h.commands.ran())
	assert.Equal(t, "mutation", h.commands.requests[0].SlotClass)
}

// lostSlotJob makes a harness whose slot job of the class mutation loses its host.
func lostSlotJob(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.SlotClass == "mutation" {
			return CommandOutput{}, fmt.Errorf("the box is gone: %w", host.ErrLost)
		}

		return CommandOutput{}, nil
	}
	h.slots = func(_, _, _ string) (SlotGrant, error) { return SlotGrant{Host: "box", Count: 1}, nil }

	return h
}

func TestCommand_ASlotJobThatCannotRunRaisesByDefault(t *testing.T) {
	t.Parallel()

	h := lostSlotJob(t)

	_, err := walkBody(t, h, "command(name = \"mutate\", run = [\"x\"], host = \"build\", slots = \"mutation\")\ncommand(name = \"later\", run = [\"later\"])\n")

	require.ErrorContains(t, err, `command "mutate" could not run`)
	assert.Equal(t, []string{"x"}, h.commands.ran(), "the run ends at the slot job")
}

func TestCommand_ASlotJobThatCannotRunIsTheResultWithFailureReturn(t *testing.T) {
	t.Parallel()

	h := lostSlotJob(t)

	_, err := walkBody(t, h, "r = command(name = \"mutate\", run = [\"x\"], host = \"build\", slots = \"mutation\", failure = \"return\")\ncommand(name = \"log\", run = [\"log\", str(r.rc), str(r.attempts), r.error])\n")

	require.NoError(t, err)
	require.Len(t, h.commands.ran(), 2, "the run goes on after the slot job")
	assert.Equal(t, []string{"log", "-1", "1"}, h.commands.requests[1].Run[:3])
	assert.Contains(t, h.commands.requests[1].Run[3], "the box is gone")
}

func TestCommand_ASlotClassTheBuildCannotGrantIsRefusedAtTheCall(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		slots func(project, cluster, class string) (SlotGrant, error)
		body  string
		words string
	}{
		"the build has no slot classes": {nil, `command(run = ["x"], host = "box", slots = "mutation")`, "the build has no slot classes"},
		"the host names no such class": {
			func(_, _, class string) (SlotGrant, error) {
				return SlotGrant{}, fmt.Errorf("host %q names no slot class %q", "box", class)
			},
			`command(run = ["x"], host = "box", slots = "mutation")`, `host "box" names no slot class "mutation"`,
		},
		"a grant of no slots": {
			func(_, _, _ string) (SlotGrant, error) { return SlotGrant{Host: "box", Count: 0}, nil },
			`command(run = ["x"], host = "box", slots = "mutation")`, `register the queue of the slot class "mutation"`,
		},
		"no host":    {nil, `command(run = ["x"], slots = "mutation")`, "slots cannot be used without host"},
		"blank name": {nil, `command(run = ["x"], host = "box", slots = " ")`, "slots must be the name of a slot class"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.slots = tc.slots

			_, err := walkBody(t, h, tc.body)

			require.ErrorContains(t, err, tc.words)
			assert.Empty(t, h.commands.ran(), "a refused call runs nothing")
		})
	}
}

func TestSlotQueue_ASetPollIntervalReachesTheQueueAndAZeroPollLeavesTheLibraryDefault(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	engine.deps.Clock.Poll = 0

	byDefault, err := engine.slotQueue("poll-default", 2)
	require.NoError(t, err)
	require.NotNil(t, byDefault.GetPartitionConcurrency())
	assert.Equal(t, 2, *byDefault.GetPartitionConcurrency())

	engine.deps.Clock.Poll = 3 * time.Second

	set, err := engine.slotQueue("poll-set", 2)
	require.NoError(t, err)

	assert.Equal(t, 3*time.Second, set.GetPollingInterval(), "a set poll is the interval of the queue")
	assert.Positive(t, byDefault.GetPollingInterval(), "a zero poll leaves the library default")
	assert.NotEqual(t, 3*time.Second, byDefault.GetPollingInterval())
}

func TestCommand_AHostJobWaitsForAFreeSlotOfItsClass(t *testing.T) {
	t.Parallel()

	jobs := newSlotJobs("m1", "m2", "p1", "p2", "p3", "b1", "n1")
	h := newHarness(t)
	h.commands.before = jobs.hold

	var host grantHost

	host.Store("box")

	h.slots = func(_, _, class string) (SlotGrant, error) {
		return SlotGrant{Host: host.Load(), Count: map[string]int{"mutation": 1, "bench": 1, "default": 1}[class]}, nil
	}

	engine := h.engine(t)

	first := runSlotItem(t, engine, "11", slotFlow("m1", "mutation"))
	require.Equal(t, "m1", receive(t, jobs.started))

	second := runSlotItem(t, engine, "12", slotFlow("m2", "mutation"))
	waitOn(t, condition(func() bool { return enqueuedJobs(t, engine) == 1 }))

	// The queue of the class has looked at the waiting job three times, and the job still waits. The limit is the reason: with a higher limit one of the looks starts it.
	endPasses := passesOfClassQueue(t, engine, jobs, &host, "p1", "p2", "p3")
	host.Store("box")

	assert.Equal(t, []string{"start m1", "start p1", "start p2", "start p3"}, jobs.order(), "the second mutation job has not started")
	assert.Equal(t, 1, enqueuedJobs(t, engine), "the second mutation job still waits in its queue")

	// A job of another class and a job of no class start while the only mutation slot is taken.
	bench := runSlotItem(t, engine, "13", slotFlow("b1", "bench"))
	plain := runSlotItem(t, engine, "14", slotFlow("n1", ""))

	started := []string{receive(t, jobs.started), receive(t, jobs.started)}
	assert.ElementsMatch(t, []string{"b1", "n1"}, started, "a job of another class and a job of no class do not wait for the mutation slot")
	assert.Equal(t, 1, enqueuedJobs(t, engine), "the second mutation job still waits in its queue")

	jobs.let("m1")
	require.NoError(t, receive(t, first))

	assert.Equal(t, "m2", receive(t, jobs.started), "the second mutation job starts after the first ends")

	order := jobs.order()
	assert.Less(t, slices.Index(order, "end m1"), slices.Index(order, "start m2"), "the second job starts only after the first job ended")

	jobs.let("m2")
	jobs.let("b1")
	jobs.let("n1")
	endPasses()

	for _, ended := range []<-chan error{second, bench, plain} {
		require.NoError(t, receive(t, ended))
	}
}

// grantHost is the host that the fake grant of a test gives to the next call. A test changes it between its calls.
type grantHost struct{ name atomic.Pointer[string] }

func (g *grantHost) Store(name string) { g.name.Store(&name) }

func (g *grantHost) Load() string { return *g.name.Load() }

// passesOfClassQueue makes the queue of the class "mutation" look at its waiting jobs once for each program, and returns the function that ends the jobs.
// Each program is a job of the class on a host of its own. The queue lists the partitions of its hosts once for each look, and looks at all of them in that pass.
// A job on a new host is queued after the waiting job, so when it starts, a pass that saw the waiting job has ended. The grant reads the host from host.
func passesOfClassQueue(t *testing.T, engine *Engine, jobs *slotJobs, host *grantHost, programs ...string) func() {
	t.Helper()

	ended := make([]<-chan error, 0, len(programs))

	for i, program := range programs {
		host.Store(fmt.Sprintf("pass-%d", i))

		ended = append(ended, runSlotItem(t, engine, fmt.Sprintf("9%d", i), slotFlow(program, "mutation")))
		require.Equal(t, program, receive(t, jobs.started))
	}

	return func() {
		for _, program := range programs {
			jobs.let(program)
		}

		for _, end := range ended {
			require.NoError(t, receive(t, end))
		}
	}
}

func TestCommand_ACanceledSlotJobFreesItsSlotAndTheReopenWaitsOnTheQueueOfItsClass(t *testing.T) {
	t.Parallel()

	jobs := newSlotJobs("m1", "m2")
	h := newHarness(t)
	h.commands.before = jobs.hold
	h.slots = func(_, _, _ string) (SlotGrant, error) { return SlotGrant{Host: "box", Count: 1}, nil }

	engine := h.engine(t)
	client := h.client(t, testVersion)

	first := runSlotItem(t, engine, testItem, slotFlow("m1", "mutation"))
	require.Equal(t, "m1", receive(t, jobs.started))

	second := runSlotItem(t, engine, "13", slotFlow("m2", "mutation"))
	waitOn(t, condition(func() bool { return enqueuedJobs(t, engine) == 1 }))

	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	require.Error(t, receive(t, first))

	assert.Equal(t, "m2", receive(t, jobs.started), "the cancel freed the only slot, so the waiting job starts")

	handle := resumeRun(t, engine, client)

	waitOn(t, condition(func() bool { return enqueuedJobs(t, engine) == 1 }))

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, slotQueueName("mutation", 1), rows[0].QueueName, "the reopened job is back on the queue of its class")
	assert.Equal(t, 1, entriesOf(jobs.order(), "start m1"), "the reopened job waits: the slot is held by the other job")

	jobs.let("m2")
	require.NoError(t, receive(t, second))

	assert.Equal(t, "m1", receive(t, jobs.started))
	jobs.let("m1")

	_, err = resultOfHandle(t, handle)
	require.NoError(t, err)
}

func TestCommand_ARestartKeepsTheLimitOfAClassForTheJobThatWasRunning(t *testing.T) {
	t.Parallel()

	jobs := newSlotJobs("m1", "m2", "p1", "p2", "p3")
	h := newHarness(t)
	h.commands.before = jobs.hold

	var host grantHost

	host.Store("box")

	h.slots = func(_, _, _ string) (SlotGrant, error) { return SlotGrant{Host: host.Load(), Count: 1}, nil }
	deps := h.deps()

	first, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, first.Launch())
	t.Cleanup(func() { require.NoError(t, first.Shutdown()) })

	ended := runSlotItem(t, first, testItem, slotFlow("m1", "mutation"))
	require.Equal(t, "m1", receive(t, jobs.started))

	waiting := runSlotItem(t, first, "13", slotFlow("m2", "mutation"))
	waitOn(t, condition(func() bool { return enqueuedJobs(t, first) == 1 }))

	require.NoError(t, first.Shutdown())
	require.Error(t, receive(t, ended))
	require.Error(t, receive(t, waiting))

	second := engineOn(t, h, deps)

	require.Equal(t, "m1", receive(t, jobs.started), "the job that ran is recovered and runs again")

	endPasses := passesOfClassQueue(t, second, jobs, &host, "p1", "p2", "p3")
	host.Store("box")

	assert.Equal(t, 0, entriesOf(jobs.order(), "start m2"), "the waiting job does not start while the recovered job holds the slot")

	jobs.let("m1")

	require.Equal(t, "m2", receive(t, jobs.started))
	jobs.let("m2")
	endPasses()

	for _, item := range []string{testItem, "13"} {
		last, found, lerr := latestRun(viewOf(second.ctx, second.dir), item)
		require.NoError(t, lerr)
		require.True(t, found)

		handle, rerr := dbos.RetrieveWorkflow[ItemResult](second.ctx, last.Status.ID)
		require.NoError(t, rerr)

		_, rerr = resultOfHandle(t, handle)
		require.NoError(t, rerr)
	}

	assert.Equal(t, 2, entriesOf(jobs.order(), "start m1"), "the job ran before the stop and once after it")
	assert.Equal(t, 1, entriesOf(jobs.order(), "start m2"))
	assert.Equal(t, 1, entriesOf(jobs.order(), "end m1"))
}

// entriesOf is the number of entries of log that equal want.
func entriesOf(log []string, want string) int {
	return len(slices.DeleteFunc(slices.Clone(log), func(s string) bool { return s != want }))
}
