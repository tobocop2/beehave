package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

// These tests run the placement of a gate on a project that declares more than one build host. A gate whose run recorded no host goes to a host that is ready
// and has the most free gate slots. Every gate here has one slot for each host (the default), so "full" means one gate runs there.

var errReadHost = errors.New("the host does not answer")

// placeHosts is the host keeper of these tests. A host in down is not ready, and a host in readErr cannot be read.
type placeHosts struct {
	*upHosts

	mu      sync.Mutex
	down    map[string]bool
	readErr map[string]error
	reads   int
}

func (p *placeHosts) Ready(_ context.Context, _, cluster string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.reads++

	return !p.down[cluster], p.readErr[cluster]
}

func (p *placeHosts) readCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.reads
}

// placeGate records the host that each gate was given, as "project/host". The default host (first declared) stands for an empty name.
type placeGate struct {
	supersedeGate

	mu      *sync.Mutex
	ran     *[]string
	defHost string
}

func (g placeGate) Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error) {
	g.mu.Lock()
	*g.ran = append(*g.ran, project+"/"+cmp.Or(host, g.defHost))
	g.mu.Unlock()

	return g.supersedeGate.Run(ctx, project, item, host, job)
}

func (g placeGate) where() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), *g.ran...)
}

func newPlaceGate(defHost string) placeGate {
	return placeGate{supersedeGate: newSupersedeGate(""), mu: &sync.Mutex{}, ran: &[]string{}, defHost: defHost}
}

// placeHarness is a harness whose project declares declared as its build hosts, with the host keeper that it returns.
func placeHarness(t *testing.T, declared []string) (*harness, *placeHosts) {
	t.Helper()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	hosts := &placeHosts{upHosts: &upHosts{}, down: map[string]bool{}, readErr: map[string]error{}}
	h.hosts = hosts
	h.facts = declaredFacts(declared)

	return h, hosts
}

func declaredFacts(declared []string) func(string) model.Project {
	return func(project string) model.Project {
		p := model.Project{Name: project, BuildHost: declared[0]}
		if len(declared) > 1 {
			p.BuildHosts = declared
		}

		return p
	}
}

func placeEngine(t *testing.T, declared []string) (*Engine, placeGate, *placeHosts) {
	t.Helper()

	h, hosts := placeHarness(t, declared)
	gated := newPlaceGate(declared[0])

	return launchHostEngine(t, h, gated), gated, hosts
}

func TestPlaceOnHost_AGateWaitsForThePlacementLockBeforeItReadsTheHostsAndQueuesItsJob(t *testing.T) {
	t.Parallel()

	engine, gated, hosts := placeEngine(t, []string{"A", "B"})
	close(gated.release)

	engine.placement.Lock()

	done := queueHostGate(engine, "project-one", "1", "A", "")

	require.Never(t, func() bool { return gated.started.Load() > 0 || hosts.readCount() > 0 }, 3*storePoll, 100*time.Millisecond, "the lock is held: no host is read and no job is queued")

	engine.placement.Unlock()

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"project-one/A"}, gated.where())
}

func TestPlaceOnHost_TwoGatesPlacedAtOnceTakeTwoDifferentHosts(t *testing.T) {
	t.Parallel()

	for round := range 12 {
		engine, gated, _ := placeEngine(t, []string{"A", "B"})

		first := queueHostGate(engine, fmt.Sprintf("project-one-%d", round), "1", "A", "")
		second := queueHostGate(engine, fmt.Sprintf("project-two-%d", round), "2", "A", "")

		require.Eventually(t, func() bool { return len(gated.where()) == 2 }, 8*time.Second, 50*time.Millisecond, "round %d: both gates run only when they took different hosts; ran %v", round, gated.where())

		ran := gated.where()
		assert.NotEqual(t, ran[0][len(ran[0])-1:], ran[1][len(ran[1])-1:], "round %d: both gates took one host: %v", round, ran)

		close(gated.release)
		require.NoError(t, receive(t, first).err)
		require.NoError(t, receive(t, second).err)
	}
}

func TestPlaceOnHost_AFullHostSendsTheNextGateToTheFreeHost(t *testing.T) {
	t.Parallel()

	engine, gated, _ := placeEngine(t, []string{"A", "B"})

	first := queueHostGate(engine, "project-one", "1", "A", "")

	waitOn(t, startsReach(t, engine, gated.supersedeGate, 1))

	second := queueHostGate(engine, "project-two", "2", "A", "")

	waitOn(t, startsReach(t, engine, gated.supersedeGate, 2))

	close(gated.release)
	require.NoError(t, receive(t, first).err)
	require.NoError(t, receive(t, second).err)
	assert.Equal(t, []string{"project-one/A", "project-two/B"}, gated.where())
}

func TestPlaceOnHost_EqualHostsGoToTheFirstDeclaredHostWhateverTheOrder(t *testing.T) {
	t.Parallel()

	for _, declared := range [][]string{{"A", "B"}, {"B", "A"}} {
		engine, gated, _ := placeEngine(t, declared)
		close(gated.release)

		for _, project := range []string{"project-one", "project-two", "project-three"} {
			require.NoError(t, receive(t, queueHostGate(engine, project, "1", "A", "")).err, project)
		}

		want := declared[0]
		assert.Equal(t, []string{"project-one/" + want, "project-two/" + want, "project-three/" + want}, gated.where(), "equal hosts %v: the first declared host wins every time", declared)
	}
}

func TestPlaceOnHost_ADeadDefaultHostIsSkippedAndTheGateRunsOnAHostThatIsUp(t *testing.T) {
	t.Parallel()

	engine, gated, hosts := placeEngine(t, []string{"A", "B"})
	hosts.down["A"] = true

	close(gated.release)

	require.NoError(t, receive(t, queueHostGate(engine, "project-one", "1", "A", "")).err)
	assert.Equal(t, []string{"project-one/B"}, gated.where())
}

func TestPlaceOnHost_WhenNoHostIsReadyTheGateGoesToTheDefaultHost(t *testing.T) {
	t.Parallel()

	engine, gated, hosts := placeEngine(t, []string{"A", "B"})
	hosts.down["A"], hosts.down["B"] = true, true

	close(gated.release)

	require.NoError(t, receive(t, queueHostGate(engine, "project-one", "1", "A", "")).err)
	assert.Equal(t, []string{"project-one/A"}, gated.where())
}

func TestPlaceOnHost_AHostThatCannotBeReadIsSkipped(t *testing.T) {
	t.Parallel()

	h, _ := placeHarness(t, []string{"A", "B"})
	hosts := &placeHosts{upHosts: &upHosts{}, down: map[string]bool{}, readErr: map[string]error{"A": errReadHost}}
	h.hosts = hosts
	gated := newPlaceGate("A")
	engine := launchHostEngine(t, h, gated)

	close(gated.release)

	require.NoError(t, receive(t, queueHostGate(engine, "project-one", "1", "A", "")).err)
	assert.Equal(t, []string{"project-one/B"}, gated.where(), "the host that does not answer is not used, and the gate does not fail")
}

func TestPlaceOnHost_OneDeclaredHostReadsNoHostAndKeepsTheGateSerial(t *testing.T) {
	t.Parallel()

	engine, gated, hosts := placeEngine(t, []string{"A"})

	first := queueHostGate(engine, "project-one", "1", "A", "")

	waitOn(t, startsReach(t, engine, gated.supersedeGate, 1))

	second := queueHostGate(engine, "project-two", "2", "A", "")

	waitOn(t, condition(waitsOnHost(engine, 1)))

	close(gated.release)
	require.NoError(t, receive(t, first).err)
	require.NoError(t, receive(t, second).err)
	assert.Equal(t, []string{"project-one/A", "project-two/A"}, gated.where())
	assert.Zero(t, hosts.readCount(), "one host: no readiness read")
}

func TestPlaceOnHost_AHostThatHostUpRecordedStaysFixedWhileAnotherHostIsFree(t *testing.T) {
	t.Parallel()

	engine, gated, hosts := placeEngine(t, []string{"A", "B"})

	first := queueHostGate(engine, "project-one", "1", "A", "A")

	waitOn(t, startsReach(t, engine, gated.supersedeGate, 1))

	second := queueHostGate(engine, "project-two", "2", "A", "A")

	waitOn(t, condition(waitsOnHost(engine, 1)))

	close(gated.release)
	require.NoError(t, receive(t, first).err)
	require.NoError(t, receive(t, second).err)
	assert.Equal(t, []string{"project-one/A", "project-two/A"}, gated.where(), "a recorded host is not replaced")
	assert.Zero(t, hosts.readCount(), "a recorded host needs no readiness read")
}

// hostsOfWaitingJobs is the partition of each build job that waits in the slot queue, in no order.
func hostsOfWaitingJobs(t *testing.T, engine *Engine) []string {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued))
	require.NoError(t, err)

	var keys []string

	for _, row := range rows {
		keys = append(keys, row.QueuePartitionKey)
	}

	return keys
}

func TestPlaceOnHost_TheShortestBacklogWinsWhenEveryHostIsFull(t *testing.T) {
	t.Parallel()

	engine, gated, _ := placeEngine(t, []string{"A", "B"})

	// Host A: one gate runs and one waits, so it has -1 free slots. Host B: one gate runs, so it has 0 free slots.
	runsOnA := queueHostGate(engine, "project-one", "1", "A", "A")

	waitOn(t, startsReach(t, engine, gated.supersedeGate, 1))

	waitsOnA := queueHostGate(engine, "project-two", "2", "A", "A")

	waitOn(t, condition(waitsOnHost(engine, 1)))

	runsOnB := queueHostGate(engine, "project-three", "3", "A", "B")

	waitOn(t, startsReach(t, engine, gated.supersedeGate, 2))

	placed := queueHostGate(engine, "project-four", "4", "A", "")

	waitOn(t, condition(waitsOnHost(engine, 2)))
	assert.ElementsMatch(t, []string{hostKeyPrefix + "A", hostKeyPrefix + "B"}, hostsOfWaitingJobs(t, engine), "the placed gate waits on B, the host with the shorter backlog")

	close(gated.release)

	for _, ended := range []<-chan gateEnd{runsOnA, waitsOnA, runsOnB, placed} {
		require.NoError(t, receive(t, ended).err)
	}
}

func TestPlaceOnHost_ARecordedChoiceSurvivesARestartAndAChangeOfTheProjectFile(t *testing.T) {
	t.Parallel()

	h, first := placeHarness(t, []string{"A", "B"})
	first.down["A"] = true

	gated := newPlaceGate("A")
	deps := h.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, gateQueuer{engine}.queue, dbos.WithWorkflowName("test-queue-gate"))
	require.NoError(t, engine.Launch())

	ended := queueHostGate(engine, "project-one", "1", "A", "")

	waitOn(t, startsReach(t, engine, gated.supersedeGate, 1))
	require.NoError(t, engine.Shutdown())

	// The project file changed and the hosts changed: B is down and A is up now.
	h.facts = declaredFacts([]string{"C", "A", "B"})
	second := &placeHosts{upHosts: &upHosts{}, down: map[string]bool{"B": true}, readErr: map[string]error{}}
	h.hosts = second
	regated := newPlaceGate("C")
	close(regated.release)

	launchHostEngine(t, h, regated)

	require.Error(t, receive(t, ended).err, "the stop of the first engine ends its wait with an error")
	require.NoError(t, receive(t, ended).err, "the second engine resumes the gate")
	assert.Equal(t, []string{"project-one/B"}, regated.where(), "the gate resumes on the host that the first run recorded")
	assert.Zero(t, second.readCount(), "the replay reads the record and asks no host")
}

func TestPlaceOnHost_AGateStepRecordsTheHostItChose(t *testing.T) {
	t.Parallel()

	engine, gated, hosts := placeEngine(t, []string{"A", "B"})
	hosts.down["A"] = true

	close(gated.release)
	require.NoError(t, receive(t, queueHostGate(engine, "project-one", "1", "A", "")).err)

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, rows[0].ID)
	require.NoError(t, err)

	var placed []any

	for _, step := range steps {
		if step.StepName == gatePlaceName {
			placed = append(placed, step.Output)
		}
	}

	require.Len(t, placed, 1, "the placement is one recorded step of the gate")
	assert.Contains(t, fmt.Sprint(placed[0]), "B", "the record holds the host that the gate runs on")
}

func TestPlaceOnHost_AProjectGateIsPlacedAndAProgramGateIsNot(t *testing.T) {
	t.Parallel()

	h, _ := placeHarness(t, []string{"A", "B"})
	engine := h.opened(t)

	project, err := engine.placeOnHost(ExclusiveInput{Kind: kindGate, Project: "demo", Item: "1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "B"}, project.Hosts, "a project gate of a run that recorded no host is placed")
	assert.Equal(t, hostKeyPrefix+"A", project.HostKey, "until it is placed, the gate holds the default host")

	chosen := project.withHost("B")
	assert.Equal(t, "B", chosen.Request.BuildHost, "a project gate carries its host in its request")
	assert.Equal(t, hostKeyPrefix+"B", chosen.HostKey)

	program, err := engine.placeOnHost(ExclusiveInput{Kind: kindGate, Project: "demo", Item: "1", Request: CommandRequest{Run: []string{"make"}, Host: "A"}})
	require.NoError(t, err)
	assert.Empty(t, program.Hosts, "a program gate keeps its host")
	assert.Equal(t, hostKeyPrefix+"A", program.HostKey)
}

func TestPlaceOnHost_ABuildWithNoHostKeeperPlacesNothing(t *testing.T) {
	t.Parallel()

	h, _ := placeHarness(t, []string{"A", "B"})
	h.hosts = nil
	engine := h.opened(t)

	gate, err := engine.placeOnHost(ExclusiveInput{Kind: kindGate, Project: "demo", Item: "1"})
	require.NoError(t, err)
	assert.Empty(t, gate.Hosts)
	assert.Equal(t, hostKeyPrefix+"A", gate.HostKey)
}

// jobsOn is the rows of the build jobs that wait or run, with one partition each.
func jobsOn(hosts ...string) lister {
	return func(...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
		var rows []dbos.WorkflowStatus

		for _, host := range hosts {
			rows = append(rows, dbos.WorkflowStatus{QueuePartitionKey: hostKeyPrefix + host})
		}

		return rows, nil
	}
}

func TestPlaceOnHost_TheRankCountsTheJobsOfEachHostAndNoOthers(t *testing.T) {
	t.Parallel()

	h, _ := placeHarness(t, []string{"A", "B", "C"})
	engine := h.opened(t)
	in := ExclusiveInput{Kind: kindGate, Project: "demo", Hosts: []string{"A", "B", "C"}, HostKey: hostKeyPrefix + "A", SlotCount: 1}

	tests := []struct {
		name string
		jobs lister
		want string
	}{
		{"the_host_with_no_job_wins", jobsOn("A", "B"), "C"},
		{"the_first_declared_wins_a_tie", jobsOn(), "A"},
		{"two_jobs_on_a_host_do_not_hide_a_free_one", jobsOn("A", "A", "C"), "B"},
		{"the_shortest_backlog_wins_when_all_are_full", jobsOn("A", "A", "B", "B", "B", "C", "C"), "A"},
		{"a_job_of_another_host_is_not_counted", jobsOn("other"), "A"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			chosen, err := engine.rankHosts(t.Context(), in, tc.jobs)
			require.NoError(t, err)
			assert.Equal(t, tc.want, chosen.Host)
			assert.Equal(t, 1, chosen.Count)
		})
	}
}

func TestPlaceOnHost_TheRankUsesTheCountOfTheHostAndTheDefaultWhenNoHostIsReady(t *testing.T) {
	t.Parallel()

	h, hosts := placeHarness(t, []string{"A", "B"})
	h.slots = func(_, host, _ string) (SlotGrant, error) {
		return SlotGrant{Host: host, Count: map[string]int{"A": 2, "B": 3}[host]}, nil
	}
	engine := h.opened(t)
	in := ExclusiveInput{Kind: kindGate, Project: "demo", Hosts: []string{"A", "B"}, HostKey: hostKeyPrefix + "A", SlotCount: 2}

	chosen, err := engine.rankHosts(t.Context(), in, jobsOn("A", "B"))
	require.NoError(t, err)
	assert.Equal(t, placement{Host: "B", Count: 3}, chosen, "B has 3 slots and one job, A has 2 slots and one job")

	hosts.down["A"], hosts.down["B"] = true, true

	chosen, err = engine.rankHosts(t.Context(), in, jobsOn())
	require.NoError(t, err)
	assert.Equal(t, placement{Host: "A", Count: 2}, chosen, "no ready host: the host and the count of the queued gate")
}

func TestPlaceOnHost_AHostWhoseCountIsRefusedEndsTheGateWithTheRefusalAndRunsNoJob(t *testing.T) {
	t.Parallel()

	refused := errors.New("the host block names no gate slots")
	h, _ := placeHarness(t, []string{"A", "B"})
	h.slots = func(_, host, _ string) (SlotGrant, error) {
		if host == "B" {
			return SlotGrant{}, refused
		}

		return SlotGrant{Host: host, Count: 1}, nil
	}
	gated := newPlaceGate("A")
	engine := launchHostEngine(t, h, gated)

	ended := receive(t, queueHostGate(engine, "project-one", "1", "A", ""))

	require.ErrorContains(t, ended.err, refused.Error(), "the placement reads the count of every ready host")
	assert.Zero(t, gated.started.Load(), "no build job started")
}

func TestPlaceOnHost_ARefusalOfTheCountOrTheListEndsTheRankWithTheError(t *testing.T) {
	t.Parallel()

	refused := errors.New("the host block names no gate slots")
	h, _ := placeHarness(t, []string{"A", "B"})
	h.slots = func(string, string, string) (SlotGrant, error) { return SlotGrant{}, refused }
	in := ExclusiveInput{Kind: kindGate, Project: "demo", Hosts: []string{"A", "B"}, HostKey: hostKeyPrefix + "A", SlotCount: 1}

	_, err := h.opened(t).rankHosts(t.Context(), in, jobsOn())
	require.ErrorIs(t, err, refused)

	h, _ = placeHarness(t, []string{"A", "B"})
	failed := errors.New("the store does not answer")

	_, err = h.opened(t).rankHosts(t.Context(), in, func(...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) { return nil, failed })
	require.ErrorIs(t, err, failed)
	require.ErrorContains(t, err, "count the gates that wait or run on host A")
}
