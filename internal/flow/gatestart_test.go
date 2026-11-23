package flow

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sizedHosts is the host keeper of a provider with a size table. The host is absent until a start. A start reads the count of waiting gates, and the
// read comes between the check of the state and the change of it, so two starts that run at once both launch.
type sizedHosts struct {
	*upHosts

	smu      sync.Mutex
	absent   bool
	launches []int

	// sizedErr, deferErr and launchErr fail the call of the same name when a test sets them.
	sizedErr, deferErr, launchErr error
}

func (s *sizedHosts) Sized(string, string) (bool, error) { return true, s.sizedErr }

func (s *sizedHosts) Defer(_ context.Context, _, cluster string) (bool, string, error) {
	s.smu.Lock()
	defer s.smu.Unlock()

	return s.absent, cmp.Or(cluster, defaultCluster), s.deferErr
}

func (s *sizedHosts) Up(_ context.Context, _, cluster string, waiting func(string) (int, error)) (bool, string, error) {
	host := cmp.Or(cluster, defaultCluster)

	s.smu.Lock()
	wasAbsent := s.absent
	s.smu.Unlock()

	if !wasAbsent {
		return false, host, nil
	}

	queued, err := waiting(host)
	if err != nil || s.launchErr != nil {
		return false, host, cmp.Or(err, s.launchErr)
	}

	<-time.After(50 * time.Millisecond)

	s.smu.Lock()
	defer s.smu.Unlock()

	s.absent = false
	s.launches = append(s.launches, queued)

	return true, host, nil
}

func (s *sizedHosts) setAbsent() {
	s.smu.Lock()
	defer s.smu.Unlock()

	s.absent = true
}

func (s *sizedHosts) launched() []int {
	s.smu.Lock()
	defer s.smu.Unlock()

	return append([]int(nil), s.launches...)
}

func sizedHarness(t *testing.T, absent bool) (*harness, *sizedHosts) {
	t.Helper()

	h, _ := placeHarness(t, []string{"A"})
	hosts := &sizedHosts{upHosts: &upHosts{}, absent: absent}
	h.hosts = hosts

	return h, hosts
}

func TestGateStart_TwelveGatesWaitOnAHostThatIsGoneAndTheNextGateStartsItWithTheCountOfTwelve(t *testing.T) {
	t.Parallel()

	h, hosts := sizedHarness(t, false)
	gated := newPlaceGate("A")
	engine := launchHostEngine(t, h, gated)

	var ended []<-chan gateEnd

	for n := 1; n <= 12; n++ {
		ended = append(ended, queueHostGate(engine, fmt.Sprintf("project-%d", n), strconv.Itoa(n), "A", "A"))

		if n == 1 {
			waitOn(t, startsReach(t, engine, gated.supersedeGate, 1))
		}
	}

	waitOn(t, condition(waitsOnHost(engine, 11)))
	assert.Empty(t, hosts.launched(), "the host was up: no gate started it")

	hosts.setAbsent()

	ended = append(ended, queueHostGate(engine, "project-13", "13", "A", "A"))

	waitOn(t, condition(func() bool { return len(hosts.launched()) == 1 }))
	assert.Equal(t, []int{12}, hosts.launched(), "the gate counts the twelve gates that wait or run on the host")

	close(gated.release)

	for _, one := range ended {
		require.NoError(t, receive(t, one).err)
	}
}

func TestGateStart_ARestartedGateReadsTheRecordOfItsStartAndStartsNoSecondHost(t *testing.T) {
	t.Parallel()

	h, first := sizedHarness(t, true)
	gated := newPlaceGate("A")
	deps := h.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, gateQueuer{engine}.queue, dbos.WithWorkflowName("test-queue-gate"))
	require.NoError(t, engine.Launch())

	ended := queueHostGate(engine, "project-one", "1", "A", "A")

	waitOn(t, startsReach(t, engine, gated.supersedeGate, 1))
	require.Equal(t, []int{0}, first.launched())
	require.NoError(t, engine.Shutdown())

	second := &sizedHosts{upHosts: &upHosts{}, absent: true}
	h.hosts = second
	regated := newPlaceGate("A")
	close(regated.release)

	launchHostEngine(t, h, regated)

	require.Error(t, receive(t, ended).err, "the stop of the first engine ends its wait with an error")
	require.NoError(t, receive(t, ended).err, "the second engine resumes the gate")
	assert.Empty(t, second.launched(), "the replay reads the record of the start and starts nothing")
}

func TestGateStart_TwoGatesPlacedAtOnceStartOneHost(t *testing.T) {
	t.Parallel()

	h, hosts := sizedHarness(t, true)
	gated := newPlaceGate("A")
	engine := launchHostEngine(t, h, gated)

	close(gated.release)

	first := queueHostGate(engine, "project-one", "1", "A", "A")
	second := queueHostGate(engine, "project-two", "2", "A", "A")

	require.NoError(t, receive(t, first).err)
	require.NoError(t, receive(t, second).err)
	assert.Len(t, hosts.launched(), 1, "the second gate finds the host that the first one started")
}

func TestGateStart_TheStopOfTheRunStopsAHostThatAGateStarted(t *testing.T) {
	t.Parallel()

	h, hosts := sizedHarness(t, true)
	h.flowIs(t, "def main(item):\n    up = host_up(name = \"start\", host = \"gpu-b\")\n    if up.started:\n        fail(\"host_up started the host\")\n    gate(name = \"check\")\n    host_teardown(name = \"stop\")\n")

	_, err := h.engine(t).Run(testItem, testProject)

	require.NoError(t, err)
	assert.Equal(t, []int{0}, hosts.launched(), "the first gate started the host that host_up left")
	assert.Equal(t, []string{"gpu-b"}, hosts.stopped, "the stop of the run covers the host that the gate started")
}

func TestHostUp_ALeftHostIsRecordedAsDeferredAndStartsNothing(t *testing.T) {
	t.Parallel()

	h, hosts := sizedHarness(t, true)
	s := &script{e: h.engine(t), in: ItemInput{Project: testProject}}

	got, err := s.startHost(t.Context(), "gpu-b")

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"started": false, "host": "gpu-b", "deferred": true}, got.Outputs)
	assert.Empty(t, hosts.launched())
}

func TestHostUp_AHostThatCannotBeReadFailsTheCallAndAHostThatIsUpStartsNormally(t *testing.T) {
	t.Parallel()

	h, hosts := sizedHarness(t, false)
	s := &script{e: h.engine(t), in: ItemInput{Project: testProject}}

	got, err := s.startHost(t.Context(), "gpu-b")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"started": false, "host": "gpu-b"}, got.Outputs, "a host that is up is not deferred")

	hosts.deferErr = errReadHost
	_, err = s.startHost(t.Context(), "gpu-b")
	require.ErrorIs(t, err, errReadHost)
}

func TestGateStart_AGateEndsWithTheErrorOfAHostThatCannotBeReadOrStarted(t *testing.T) {
	t.Parallel()

	for name, set := range map[string]func(*sizedHosts){
		"a size table that cannot be read": func(s *sizedHosts) { s.sizedErr = errReadHost },
		"a start that fails":               func(s *sizedHosts) { s.launchErr = errReadHost },
	} {
		h, hosts := sizedHarness(t, true)
		set(hosts)

		engine := launchHostEngine(t, h, newPlaceGate("A"))

		require.Error(t, receive(t, queueHostGate(engine, "project-one", "1", "A", "A")).err, name)
		assert.Empty(t, hosts.launched(), name)
	}
}
