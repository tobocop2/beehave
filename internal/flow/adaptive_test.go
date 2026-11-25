package flow

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
)

// traceStart is the time of the tick before the first one of a trace.
var traceStart = time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)

// queueFake is the agents queue of a controller test: it records each limit that the controller writes.
type queueFake struct {
	mu     sync.Mutex
	limit  int
	writes []int
	fail   error
}

func (q *queueFake) set(limit int) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.fail != nil {
		return q.fail
	}

	q.limit = limit
	q.writes = append(q.writes, limit)

	return nil
}

func (q *queueFake) written() []int {
	q.mu.Lock()
	defer q.mu.Unlock()

	return append([]int(nil), q.writes...)
}

// signals is the channel of a controller test, with room for every signal of a tick.
func signals() chan limitSignal { return make(chan limitSignal, limitSignals) }

// controllerOf builds a controller over fakes: room and waiting are what the usage read and the store say at each step.
func controllerOf(floor, ceiling int, q *queueFake, room *bool, waiting *int, limited <-chan limitSignal) *controller {
	return &controller{
		floor: floor, ceiling: ceiling, tick: adaptiveTick, set: q.set, limited: limited, logger: slog.New(slog.DiscardHandler),
		room:    func(context.Context) bool { return *room },
		waiting: func(context.Context) (int, error) { return *waiting, nil },
	}
}

// tickAt is the time of tick number n of a test, counted from 1.
func tickAt(n int) time.Time { return traceStart.Add(time.Duration(n) * adaptiveTick) }

// traceScenario drives a controller through the events of one scenario and returns the limit that the queue holds after each tick.
// A static scenario starts no controller, so the queue keeps the ceiling.
func traceScenario(t *testing.T, line string) (string, string) {
	t.Helper()

	fields := strings.Split(line, "|")
	require.Len(t, fields, 5, line)

	ceiling, err := strconv.Atoi(fields[2])
	require.NoError(t, err)

	floor, err := strconv.Atoi(fields[3])
	require.NoError(t, err)

	events := strings.Fields(fields[4])
	numbers := make([]string, 0, len(events))

	if fields[1] == string(config.ConcurrencyStatic) {
		engine := &Engine{concurrency: config.ConcurrencyStatic}
		require.False(t, engine.adaptive())
		require.NoError(t, engine.startController(), "a static engine starts no controller")

		for range events {
			numbers = append(numbers, strconv.Itoa(ceiling))
		}

		return fields[0], strings.Join(numbers, " ")
	}

	queue := &queueFake{}
	room, waiting := true, 1
	control := controllerOf(floor, ceiling, queue, &room, &waiting, nil)
	require.NoError(t, control.launch())

	for index, event := range events {
		now := tickAt(index + 1)
		room, waiting = true, 1

		switch kind, after := event[0], mustNumber(t, event[1:]); kind {
		case 'N', 'U':
			room = false
		case 'I':
			waiting = 0
		case 'L':
			control.halve(t.Context(), limitSignal{At: now, ResetsAt: now.Add(time.Duration(after) * adaptiveTick)})
		case 'B':
			for range 3 {
				control.halve(t.Context(), limitSignal{At: now, ResetsAt: now.Add(time.Duration(after) * adaptiveTick)})
			}
		case 'O':
			control.halve(t.Context(), limitSignal{At: now})
		}

		control.step(t.Context(), now)

		numbers = append(numbers, strconv.Itoa(queue.limit))
	}

	return fields[0], strings.Join(numbers, " ")
}

// mustNumber reads the digits after an event letter. No digits read as 0.
func mustNumber(t *testing.T, digits string) int {
	t.Helper()

	if digits == "" {
		return 0
	}

	number, err := strconv.Atoi(digits)
	require.NoError(t, err)

	return number
}

// TestAdaptiveConcurrency_Trace prints one ROW line for each scenario of the file in ADAPTIVE_TRACE: the limit of the queue after each tick.
// The acceptance script of the change compares the rows with its own oracle. The test skips when the variable is empty.
func TestAdaptiveConcurrency_Trace(t *testing.T) { //nolint:paralleltest // reads a variable of the process
	path := os.Getenv("ADAPTIVE_TRACE") //nolint:forbidigo // the acceptance script names the scenario file in this variable
	if path == "" {
		t.Skip("ADAPTIVE_TRACE names no scenario file")
	}

	file, err := os.Open(path) //nolint:gosec // the acceptance script names the file
	require.NoError(t, err)

	defer func() { require.NoError(t, file.Close()) }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if scanner.Text() == "" {
			continue
		}

		id, numbers := traceScenario(t, scanner.Text())
		_, _ = fmt.Fprintf(os.Stdout, "ROW %s|%s\n", id, numbers) //nolint:forbidigo // the acceptance script reads the rows from the output of the test
	}

	require.NoError(t, scanner.Err())
}

func TestController_LaunchWritesTheFloorAndAFailedWriteIsAnError(t *testing.T) {
	t.Parallel()

	queue := &queueFake{limit: 9}
	room, waiting := false, 0
	control := controllerOf(2, 6, queue, &room, &waiting, signals())

	require.NoError(t, control.launch())
	assert.Equal(t, []int{2}, queue.written(), "a restart writes the floor whatever the queue held")
	assert.Equal(t, 2, control.current)

	broken := &queueFake{fail: errors.New("the store is gone")}
	control = controllerOf(2, 6, broken, &room, &waiting, signals())

	err := control.launch()
	require.ErrorContains(t, err, "set the limit of the agents queue to 2")
	require.ErrorContains(t, err, "the store is gone")
}

func TestController_StepAddsOneAgentOnlyWhenEveryLoginHasRoomAndAPhaseWaits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		room    bool
		waiting int
		want    []int
	}{
		{"room_and_demand_adds_one", true, 2, []int{1, 2}},
		{"no_room_adds_none", false, 2, []int{1}},
		{"no_demand_adds_none", true, 0, []int{1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			queue := &queueFake{}
			room, waiting := test.room, test.waiting
			control := controllerOf(1, 4, queue, &room, &waiting, signals())
			require.NoError(t, control.launch())

			control.step(t.Context(), tickAt(1))

			assert.Equal(t, test.want, queue.written())
		})
	}
}

func TestController_StepNeverPassesTheCeilingAndTheFloorStaysWhenALimitComes(t *testing.T) {
	t.Parallel()

	queue := &queueFake{}
	room, waiting := true, 5
	control := controllerOf(1, 2, queue, &room, &waiting, nil)
	require.NoError(t, control.launch())

	for index := 1; index <= 4; index++ {
		control.step(t.Context(), tickAt(index))
	}

	assert.Equal(t, []int{1, 2}, queue.written(), "the number stops at the ceiling and the controller writes nothing more")

	control.halve(t.Context(), limitSignal{At: tickAt(5)})
	assert.Equal(t, []int{1, 2, 1}, queue.written(), "a limit halves 2 to the floor 1")

	control.halve(t.Context(), limitSignal{At: tickAt(7)})
	assert.Equal(t, []int{1, 2, 1}, queue.written(), "a limit at the floor writes nothing")
}

func TestController_AHoldBlocksTheRiseUntilTheResetTimeAndTakesNoSecondHalving(t *testing.T) {
	t.Parallel()

	queue := &queueFake{}
	room, waiting := true, 5
	control := controllerOf(1, 8, queue, &room, &waiting, nil)
	require.NoError(t, control.launch())

	for index := 1; index <= 7; index++ {
		control.step(t.Context(), tickAt(index))
	}

	require.Equal(t, 8, queue.limit)

	control.halve(t.Context(), limitSignal{At: tickAt(8), ResetsAt: tickAt(10)})
	control.halve(t.Context(), limitSignal{At: tickAt(8), ResetsAt: tickAt(20)})
	assert.Equal(t, 4, queue.limit, "the first limit halves 8, the second one inside the hold does not")

	control.halve(t.Context(), limitSignal{At: tickAt(9), ResetsAt: tickAt(30)})
	control.step(t.Context(), tickAt(9))
	assert.Equal(t, 4, queue.limit, "a limit while the hold is open changes nothing, and the hold keeps its end")

	control.step(t.Context(), tickAt(10))
	assert.Equal(t, 5, queue.limit, "the tick at the reset time adds one agent again")
}

func TestController_APhaseThatEndedInsideTheHoldNeverHalvesAgainWhenALaterTickReadsIt(t *testing.T) {
	t.Parallel()

	queue := &queueFake{}
	room, waiting := true, 0
	control := controllerOf(1, 16, queue, &room, &waiting, nil)
	require.NoError(t, control.launch())
	require.NoError(t, control.set(16))
	control.current = 16

	control.halve(t.Context(), limitSignal{At: tickAt(1)})
	control.halve(t.Context(), limitSignal{At: tickAt(1).Add(10 * time.Second)})
	control.step(t.Context(), tickAt(2))
	control.halve(t.Context(), limitSignal{At: tickAt(2)})

	assert.Equal(t, []int{1, 16, 8, 4}, queue.written(), "the phase that ended 10 s after the first one is inside the hold and adds no halving; a phase that ends when the hold ends halves again")
}

func TestController_ALimitWithoutAResetTimeOrWithOneInThePastHoldsOneTick(t *testing.T) {
	t.Parallel()

	for name, signal := range map[string]limitSignal{"an_overload_has_no_reset_time": {At: tickAt(4)}, "a_reset_time_in_the_past": {At: tickAt(4), ResetsAt: tickAt(-5)}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			queue := &queueFake{}
			room, waiting := true, 5
			control := controllerOf(1, 8, queue, &room, &waiting, nil)
			require.NoError(t, control.launch())

			for index := 1; index <= 3; index++ {
				control.step(t.Context(), tickAt(index))
			}

			require.Equal(t, 4, queue.limit)

			control.halve(t.Context(), signal)
			control.step(t.Context(), tickAt(4))
			assert.Equal(t, 2, queue.limit, "the limit halves 4, and the tick of the same time adds none")

			control.step(t.Context(), tickAt(5))
			assert.Equal(t, 3, queue.limit, "the hold ended one tick after the limit")
		})
	}
}

func TestController_RunHalvesAtOnceWhenALimitedPhaseArrivesWithNoTick(t *testing.T) {
	t.Parallel()

	queue := &queueFake{}
	room, waiting, limited := true, 0, signals()
	control := controllerOf(1, 8, queue, &room, &waiting, limited)
	require.NoError(t, control.launch())
	control.current = 8

	stop, done := make(chan struct{}), make(chan struct{})

	go func() {
		control.run(t.Context(), make(chan time.Time), stop)
		close(done)
	}()

	limited <- limitSignal{At: tickAt(1)}
	eventually(t, func() bool { return slices.Equal(queue.written(), []int{1, 4}) })
	close(stop)
	promptly(t, done)
}

func TestController_AFailedWriteKeepsTheNumberAndTheNextTickTriesAgain(t *testing.T) {
	t.Parallel()

	queue := &queueFake{}
	room, waiting := true, 3
	control := controllerOf(1, 4, queue, &room, &waiting, signals())
	require.NoError(t, control.launch())

	queue.fail = errors.New("the store is busy")
	control.step(t.Context(), tickAt(1))
	assert.Equal(t, 1, control.current, "the write failed, so the controller still reads 1")

	queue.fail = nil
	control.step(t.Context(), tickAt(2))
	assert.Equal(t, []int{1, 2}, queue.written())
}

func TestController_ACountOfWaitingPhasesThatFailsAddsNoAgent(t *testing.T) {
	t.Parallel()

	queue := &queueFake{}
	room, waiting := true, 3
	control := controllerOf(1, 4, queue, &room, &waiting, signals())
	control.waiting = func(context.Context) (int, error) { return 0, errors.New("the store is busy") }
	require.NoError(t, control.launch())

	control.step(t.Context(), tickAt(1))

	assert.Equal(t, []int{1}, queue.written())
}

func TestController_RunStepsOnEachTickAndStopsWhenStopCloses(t *testing.T) {
	t.Parallel()

	queue := &queueFake{}
	room, waiting := true, 3
	control := controllerOf(1, 4, queue, &room, &waiting, signals())
	require.NoError(t, control.launch())

	ticks, stop, done := make(chan time.Time), make(chan struct{}), make(chan struct{})

	go func() {
		control.run(t.Context(), ticks, stop)
		close(done)
	}()

	ticks <- tickAt(1)
	ticks <- tickAt(2)
	close(stop)
	promptly(t, done)

	assert.Equal(t, []int{1, 2, 3}, queue.written(), "each tick that the run received added one agent")
}

func TestController_RunStopsWhenItsContextEnds(t *testing.T) {
	t.Parallel()

	queue := &queueFake{}
	room, waiting := true, 0
	control := controllerOf(1, 4, queue, &room, &waiting, signals())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		control.run(ctx, make(chan time.Time), make(chan struct{}))
		close(done)
	}()

	cancel()
	promptly(t, done)
}

func TestSignalLimit_NeverBlocksAndSendsOnlyALimitedPhase(t *testing.T) {
	t.Parallel()

	engine := &Engine{limited: signals()}
	reset := tickAt(3)

	engine.signalLimit(PhaseOutput{Outcome: agent.OutcomeLimited, Limit: &agent.Limit{Kind: agent.LimitRate, ResetsAt: reset}})
	<-engine.limited

	before := time.Now()
	engine.signalLimit(PhaseOutput{Outcome: agent.OutcomeLimited, Limit: &agent.Limit{Kind: agent.LimitRate, ResetsAt: reset}})
	sent := <-engine.limited
	assert.Equal(t, reset, sent.ResetsAt)
	assert.False(t, sent.At.Before(before), "the signal carries the time when the phase ended")

	engine.signalLimit(PhaseOutput{Outcome: agent.OutcomeLimited})
	engine.signalLimit(PhaseOutput{Outcome: agent.OutcomeTurnCap, Limit: &agent.Limit{Kind: agent.LimitRate}})
	assert.Empty(t, engine.limited, "a phase with no limit, or one that ended at another outcome, sends nothing")

	for range limitSignals + 5 {
		engine.signalLimit(PhaseOutput{Outcome: agent.OutcomeLimited, Limit: &agent.Limit{Kind: agent.LimitOverload}})
	}

	assert.Len(t, engine.limited, limitSignals, "a full channel drops the signal and the send returns")

	assert.NotPanics(t, func() {
		(&Engine{}).signalLimit(PhaseOutput{Outcome: agent.OutcomeLimited, Limit: &agent.Limit{Kind: agent.LimitOverload}})
	}, "an engine with no channel drops every signal")
}

// adaptiveHarness is a harness whose agents queue is adaptive with a floor of 1 and a ceiling of 2, and whose runs wait on the fake agent until the test releases it.
func adaptiveHarness(t *testing.T, logins map[string]string) (*harness, func()) {
	t.Helper()

	h := newHarness(t)
	h.opts.Concurrency = config.ConcurrencyAdaptive
	h.opts.MaxConcurrent = 2
	h.opts.PerLogin = 1
	h.logins = fakeLogins{byItem: logins, room: true}
	h.adapt = make(chan time.Time, 1)
	h.agents.gate = make(chan struct{})

	release := sync.OnceFunc(func() { close(h.agents.gate) })
	t.Cleanup(release)

	return h, release
}

// agentsLimit reads the limit that the agents queue holds, as the status shows it.
func agentsLimit(t *testing.T, client *Client) int {
	t.Helper()

	shown, adaptive, err := client.AgentsCap()
	require.NoError(t, err)
	require.True(t, adaptive)

	return shown.Current
}

func TestAdaptive_TheQueueStartsAtTheFloorAndATickWithAWaitingPhaseStartsTheNextAgent(t *testing.T) {
	t.Parallel()

	h, release := adaptiveHarness(t, map[string]string{"1": "a", "2": "b"})
	engine := h.engine(t)
	client := h.client(t, testVersion)

	assert.Equal(t, 1, agentsLimit(t, client), "the engine writes the floor at launch")

	first, second := runInBackground(engine, "1"), runInBackground(engine, "2")

	eventually(t, func() bool { return h.agents.running() == 1 })
	eventually(t, func() bool { return enqueuedPhases(t, engine) == 1 })

	h.adapt <- tickAt(1)

	eventually(t, func() bool { return h.agents.running() == 2 })
	assert.Equal(t, 2, agentsLimit(t, client), "the tick wrote 2 to the queue row, and the waiting phase started")

	release()
	require.NoError(t, promptly(t, first).err)
	require.NoError(t, promptly(t, second).err)
}

func TestAdaptive_LoweringTheLimitCutsNoRunningPhaseAndStartsNoNewOne(t *testing.T) {
	t.Parallel()

	h, release := adaptiveHarness(t, map[string]string{"1": "a", "2": "b", "3": "c"})
	engine := h.engine(t)
	client := h.client(t, testVersion)

	first, second := runInBackground(engine, "1"), runInBackground(engine, "2")

	eventually(t, func() bool { return enqueuedPhases(t, engine) == 1 && h.agents.running() == 1 })
	h.adapt <- tickAt(1)
	eventually(t, func() bool { return h.agents.running() == 2 })

	third := runInBackground(engine, "3")

	eventually(t, func() bool { return enqueuedPhases(t, engine) == 1 })
	require.Equal(t, 2, h.agents.running(), "the limit 2 holds the third phase")

	engine.limited <- limitSignal{At: time.Now()}

	eventually(t, func() bool { return agentsLimit(t, client) == 1 })
	require.Never(t, func() bool { return h.agents.running() != 2 || enqueuedPhases(t, engine) != 1 }, quietFor, 20*time.Millisecond,
		"no running phase is cut by the lower limit")

	// One running phase ends. Two run and one waits, so a start under the old limit of 2 would show here: the limit 1 holds the waiting phase while one still runs.
	h.agents.gate <- struct{}{}

	eventually(t, func() bool { return h.agents.running() == 1 })
	require.Never(t, func() bool { return h.agents.running() != 1 || enqueuedPhases(t, engine) != 1 }, quietFor, 20*time.Millisecond,
		"the waiting phase stays queued while one phase runs at the limit 1")

	release()
	require.NoError(t, promptly(t, first).err)
	require.NoError(t, promptly(t, second).err)
	require.NoError(t, promptly(t, third).err, "the third phase starts after the two end")
}

func TestAdaptive_APhaseThatEndsAtALimitSignalsTheControllerWithTheResetTime(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0), exited)

	engine := h.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	select {
	case signal := <-engine.limited:
		assert.Equal(t, limitReset, signal.ResetsAt, "the limited phase sent its reset time")
		assert.False(t, signal.At.IsZero(), "and the time when it ended")
	default:
		require.Fail(t, "the limited phase sent no signal")
	}

	assert.Empty(t, engine.limited, "the rerun after the reset ended with success, so it sent nothing")
}

func TestAdaptive_AStaticEngineKeepsTheCeilingAndReadsNoQueueRow(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.MaxConcurrent = 2
	engine := h.engine(t)
	client := h.client(t, testVersion)

	assert.Equal(t, 2, *engine.queue.GetGlobalConcurrency(), "a static engine registers the agents queue at max_concurrent")

	shown, adaptive, err := client.AgentsCap()
	require.NoError(t, err)
	assert.False(t, adaptive)
	assert.Equal(t, AgentsCap{}, shown)
}

func TestClientAgentsCap_AFolderWithNoEngineShowsTheFloorAndAClosedStoreIsAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Concurrency = config.ConcurrencyAdaptive
	h.opts.MaxConcurrent = 4
	h.opts.PerLogin = 2
	h.fresh(t)

	client := h.client(t, testVersion)

	shown, adaptive, err := client.AgentsCap()
	require.NoError(t, err)
	assert.True(t, adaptive)
	assert.Equal(t, AgentsCap{Mode: config.ConcurrencyAdaptive, Current: 2, Ceiling: 4}, shown, "no engine has written a queue row, so the limit is the floor")

	closed, err := OpenClient(t.Context(), h.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	_, adaptive, err = closed.AgentsCap()
	require.ErrorContains(t, err, "read the limit of the agents queue")
	assert.False(t, adaptive)
}

func TestAdaptive_ACountOfWaitingPhasesOnAStoppedEngineIsAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	require.NoError(t, engine.Shutdown())

	_, err := engine.waitingPhases(t.Context())
	require.ErrorContains(t, err, "list the phases that wait")
}

func TestAdaptive_StartControllerFailsWhenTheQueueRowCannotBeRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Concurrency = config.ConcurrencyAdaptive

	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	require.NoError(t, engine.Shutdown())

	err = engine.startController()
	require.ErrorContains(t, err, "read the agents queue for the controller")
}

func TestAdaptive_StartControllerFailsWhenTheQueueRefusesTheFloor(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Concurrency = config.ConcurrencyAdaptive
	h.opts.MaxConcurrent = 4
	h.opts.PerLogin = 2

	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	// The queue row holds a partition limit of 2, so a floor of 1 is below it and the row refuses it.
	engine.perLogin = 1

	err = engine.startController()
	require.ErrorContains(t, err, "set the limit of the agents queue to 1")
}

func TestDryLogins_HaveNoHeadroomBecauseADryRunStartsNoController(t *testing.T) {
	t.Parallel()

	assert.False(t, dryLogins{}.Headroom(t.Context()))
}

func TestAdaptive_TheAgentsQueueIsRegisteredAtTheFloorBeforeTheControllerWritesIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.opts.Concurrency = config.ConcurrencyAdaptive
	h.opts.MaxConcurrent = 4
	h.opts.PerLogin = 2

	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	assert.Equal(t, 2, *engine.queue.GetGlobalConcurrency(), "no phase starts above the floor before the first write")
}

func TestAdaptive_TheControllerTicksOnTheIntervalOfTheControllerAndThatIntervalIsOneMinute(t *testing.T) {
	t.Parallel()

	assert.Equal(t, time.Minute, adaptiveTick, "the real tick is one minute")

	h := newHarness(t)
	engine := h.engine(t)
	assert.Equal(t, time.Minute, engine.newController(engine.queue).tick, "the controller of an engine takes the constant")

	counted := make(chan struct{}, 1)
	control := controllerOf(1, 4, &queueFake{}, new(bool), new(int), nil)
	control.tick = 10 * time.Millisecond
	control.waiting = func(context.Context) (int, error) {
		select {
		case counted <- struct{}{}:
		default:
		}

		return 0, nil
	}

	done := make(chan struct{})

	go func() {
		engine.runController(control)
		close(done)
	}()

	promptly(t, counted)
	require.NoError(t, engine.Shutdown())
	promptly(t, done)
}

// quietFor is how long a test watches a queue that must not start a phase. The queue reads the store once each storePoll, so three polls show a start that a wrong limit allows.
const quietFor = 3 * storePoll

// patience bounds a wait of a controller test. A controller that never acts fails the test in this time and not at the bound of the whole run.
const patience = 10 * time.Second

// eventually waits until check is true, and fails the test after patience.
func eventually(t *testing.T, check func() bool) {
	t.Helper()

	require.Eventually(t, check, patience, 5*time.Millisecond)
}

// promptly receives one value from ch, and fails the test after patience.
func promptly[T any](t *testing.T, ch <-chan T) T {
	t.Helper()

	select {
	case value := <-ch:
		return value
	case <-time.After(patience):
		require.FailNow(t, "no value on the channel in time")

		var none T

		return none
	}
}
