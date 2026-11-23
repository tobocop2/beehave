package flow

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

// eventGate holds the gate of each project until the test releases that project, and sends "start <project>" and "end <project>" as each gate starts and ends.
// The first gate of the project lost returns a lost job instead.
type eventGate struct {
	events  chan string
	release map[string]chan struct{}
	lost    string
	losses  *atomic.Int32
}

func newEventGate(lost string, projects ...string) eventGate {
	release := map[string]chan struct{}{}
	for _, project := range projects {
		release[project] = make(chan struct{})
	}

	return eventGate{events: make(chan string, 4*len(projects)), release: release, lost: lost, losses: &atomic.Int32{}}
}

func (eventGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (eventGate) Submit(_ context.Context, project, _, _, _, _, _ string, _ model.Slot) (GateJob, error) {
	return GateJob{ID: "job-" + project, Head: gatedHead}, nil
}

func (g eventGate) Run(ctx context.Context, project, _, _ string, _ GateJob) (GateResult, error) {
	if project == g.lost && g.losses.Add(1) == 1 {
		return GateResult{}, lostError()
	}

	g.events <- "start " + project

	select {
	case <-g.release[project]:
	case <-ctx.Done():
		return GateResult{}, ctx.Err()
	}

	g.events <- "end " + project

	return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
}

// gateCounts gives Deps.Slots a count for each host of the class gate, read when each gate queues.
func gateCounts(counts map[string]*atomic.Int32) func(project, cluster, class string) (SlotGrant, error) {
	return func(_, cluster, class string) (SlotGrant, error) {
		if class != model.GateClass {
			return SlotGrant{}, fmt.Errorf("%w: class %q", errNoGrant, class)
		}

		return SlotGrant{Host: cluster, Count: int(counts[cluster].Load())}, nil
	}
}

func slotCount(n int32) *atomic.Int32 {
	count := &atomic.Int32{}
	count.Store(n)

	return count
}

// slotEngine launches an engine whose gate is gated and whose Deps.Slots is slots. A non-nil hosts answers the reads of a lost job, read again every 50ms.
func slotEngine(t *testing.T, gated gate, slots func(project, cluster, class string) (SlotGrant, error), hosts *upHosts) *Engine {
	t.Helper()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	h.slots = slots

	if hosts != nil {
		h.hosts, h.lostPoll = hosts, 50*time.Millisecond
	}

	return launchHostEngine(t, h, gated)
}

// startsOf reads want events and requires each to be the start of a gate. It returns the projects in the order they started.
func startsOf(t *testing.T, gated eventGate, want int) []string {
	t.Helper()

	var started []string

	for range want {
		event := receive(t, gated.events)
		project, ok := strings.CutPrefix(event, "start ")
		require.True(t, ok, "a gate started before any gate ended, got %q", event)

		started = append(started, project)
	}

	return started
}

// staysQueued requires that want build jobs of gates wait on the slot queue, and that no gate starts or ends for three polls of the queue.
// A free slot would start a waiting job within one poll.
func staysQueued(t *testing.T, engine *Engine, gated eventGate, want int) {
	t.Helper()

	waitOn(t, condition(waitsOnHost(engine, want)))

	select {
	case event := <-gated.events:
		t.Fatalf("a gate moved while every slot was full: %q", event)
	case <-time.After(3 * storePoll):
	}

	require.True(t, waitsOnHost(engine, want)(), "the build jobs still wait on the slot queue")
}

// endThenStart releases the gate of ended and requires that it ends and that the gate of started then starts, in that order.
func endThenStart(t *testing.T, gated eventGate, ended, started string) {
	t.Helper()

	close(gated.release[ended])
	assert.Equal(t, "end "+ended, receive(t, gated.events))
	assert.Equal(t, "start "+started, receive(t, gated.events), "the waiting gate starts in the slot that the ended gate freed")
}

func releaseAll(t *testing.T, gated eventGate, ends ...<-chan gateEnd) {
	t.Helper()

	for _, release := range gated.release {
		select {
		case <-release:
		default:
			close(release)
		}
	}

	for _, ended := range ends {
		require.NoError(t, receive(t, ended).err)
	}
}

func TestGateSlots_TwoSlotsRunTwoGatesOfOneHostAtOnce(t *testing.T) {
	t.Parallel()

	gated := newEventGate("", "project-one", "project-two")
	engine := slotEngine(t, gated, gateCounts(map[string]*atomic.Int32{"box": slotCount(2)}), nil)

	first := queueHostGate(engine, "project-one", "1", "A", "box")
	second := queueHostGate(engine, "project-two", "2", "A", "box")

	// No gate ends before the test releases it, so two start events prove that both run at once.
	assert.ElementsMatch(t, []string{"project-one", "project-two"}, startsOf(t, gated, 2))

	releaseAll(t, gated, first, second)
}

func TestGateSlots_AThirdGateWaitsForAFreeSlotAndStartsWhenOneEnds(t *testing.T) {
	t.Parallel()

	gated := newEventGate("", "project-one", "project-two", "project-three")
	engine := slotEngine(t, gated, gateCounts(map[string]*atomic.Int32{"box": slotCount(2)}), nil)

	first := queueHostGate(engine, "project-one", "1", "A", "box")
	second := queueHostGate(engine, "project-two", "2", "A", "box")
	running := startsOf(t, gated, 2)

	third := queueHostGate(engine, "project-three", "3", "A", "box")

	staysQueued(t, engine, gated, 1)
	endThenStart(t, gated, running[0], "project-three")

	releaseAll(t, gated, first, second, third)
}

func TestGateSlots_OneSlotRunsOneGateAtATime(t *testing.T) {
	t.Parallel()

	for name, slots := range map[string]func(project, cluster, class string) (SlotGrant, error){
		"gate_slots_of_one": gateCounts(map[string]*atomic.Int32{"box": slotCount(1)}),
		"no_slot_resolver":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gated := newEventGate("", "project-one", "project-two")
			engine := slotEngine(t, gated, slots, nil)

			first := queueHostGate(engine, "project-one", "1", "A", "box")
			second := queueHostGate(engine, "project-two", "2", "A", "box")
			running := startsOf(t, gated, 1)

			staysQueued(t, engine, gated, 1)

			waiting := map[string]string{"project-one": "project-two", "project-two": "project-one"}[running[0]]
			endThenStart(t, gated, running[0], waiting)

			releaseAll(t, gated, first, second)
		})
	}
}

// A count that changes between two queue times takes effect for the next gate. The gate on the old queue keeps its slot, so the host runs at most the old count plus the new one.
func TestGateSlots_ACountChangedBetweenQueueTimesRunsAtMostTheOldCountPlusTheNew(t *testing.T) {
	t.Parallel()

	count := slotCount(1)
	gated := newEventGate("", "project-one", "project-two", "project-three", "project-four")
	engine := slotEngine(t, gated, gateCounts(map[string]*atomic.Int32{"box": count}), nil)

	first := queueHostGate(engine, "project-one", "1", "A", "box")
	require.Equal(t, []string{"project-one"}, startsOf(t, gated, 1))

	count.Store(2)

	second := queueHostGate(engine, "project-two", "2", "A", "box")
	third := queueHostGate(engine, "project-three", "3", "A", "box")
	assert.ElementsMatch(t, []string{"project-two", "project-three"}, startsOf(t, gated, 2), "the new count gives two more slots while the old gate runs")

	fourth := queueHostGate(engine, "project-four", "4", "A", "box")

	staysQueued(t, engine, gated, 1)
	endThenStart(t, gated, "project-two", "project-four")

	releaseAll(t, gated, first, second, third, fourth)
}

func TestGateSlots_TwoGatesOfOneProjectNeverOverlapWithTwoSlots(t *testing.T) {
	t.Parallel()

	gated := newEventGate("", "project-one")
	engine := slotEngine(t, gated, gateCounts(map[string]*atomic.Int32{"box": slotCount(2)}), nil)

	first := queueHostGate(engine, "project-one", "1", "A", "box")
	require.Equal(t, []string{"project-one"}, startsOf(t, gated, 1))

	second := queueHostGate(engine, "project-one", "2", "A", "box")

	// The second gate waits on the queue of its project, so no build job of it waits on the slot queue.
	staysQueued(t, engine, gated, 0)

	close(gated.release["project-one"])
	assert.Equal(t, "end project-one", receive(t, gated.events))
	assert.Equal(t, "start project-one", receive(t, gated.events), "the second gate of the project starts after the first ends")
	assert.Equal(t, "end project-one", receive(t, gated.events))

	releaseAll(t, gated, first, second)
}

func TestGateSlots_EachHostKeepsItsOwnCount(t *testing.T) {
	t.Parallel()

	gated := newEventGate("", "project-one", "project-two", "project-three", "project-four")
	engine := slotEngine(t, gated, gateCounts(map[string]*atomic.Int32{"big": slotCount(2), "small": slotCount(1)}), nil)

	first := queueHostGate(engine, "project-one", "1", "A", "big")
	second := queueHostGate(engine, "project-two", "2", "A", "big")
	assert.ElementsMatch(t, []string{"project-one", "project-two"}, startsOf(t, gated, 2))

	third := queueHostGate(engine, "project-three", "3", "A", "small")
	require.Equal(t, []string{"project-three"}, startsOf(t, gated, 1))

	fourth := queueHostGate(engine, "project-four", "4", "A", "small")

	staysQueued(t, engine, gated, 1)
	endThenStart(t, gated, "project-three", "project-four")

	releaseAll(t, gated, first, second, third, fourth)
}

func TestGateSlots_ALostJobKeepsItsSlotUntilTheProofAndThenFreesIt(t *testing.T) {
	t.Parallel()

	gated := newEventGate("project-one", "project-two", "project-three")
	hosts := &upHosts{started: true}
	engine := slotEngine(t, gated, gateCounts(map[string]*atomic.Int32{"box": slotCount(2)}), hosts)

	lost := queueHostGate(engine, "project-one", "1", "A", "box")

	waitOn(t, condition(func() bool { return hosts.proofReads() >= 1 }))

	second := queueHostGate(engine, "project-two", "2", "A", "box")
	require.Equal(t, []string{"project-two"}, startsOf(t, gated, 1), "the free slot runs a second gate while the first job is lost")

	third := queueHostGate(engine, "project-three", "3", "A", "box")

	staysQueued(t, engine, gated, 1)

	hosts.prove(model.LostProof{Kind: model.LostStopped})

	end := receive(t, lost)
	require.NoError(t, end.err)
	require.NotNil(t, end.out.Lost)
	assert.Equal(t, model.LostStopped, end.out.Lost.Proof.Kind)
	assert.Equal(t, "start project-three", receive(t, gated.events), "the proof frees the slot of the lost job")

	releaseAll(t, gated, second, third)
}
