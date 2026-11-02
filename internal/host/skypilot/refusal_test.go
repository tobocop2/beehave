package skypilot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
)

func TestStatus_NamesTheStateOfAClusterThatIsNotUp(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"INIT", "STOPPED"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			h, sky, ssh := newScripted(t)
			sky.Plan(`[{"name": "lane-host", "status": "`+state+`"}]`, "status")

			_, err := h.Status(context.Background())

			require.ErrorIs(t, err, errClusterUp)
			require.ErrorContains(t, err, state)
			assert.Empty(t, ssh.Calls, "no command runs on a cluster that is not up")
		})
	}
}

func TestWait_RefusesTwoStatesFromSqueueWithoutAskingSacct(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("RUNNING\nRUNNING\n", "squeue")

	state, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, errJobText)
	assert.Equal(t, model.JobUnknown, state)
	assert.False(t, ssh.Called("sacct"))
}

func TestWait_RefusesAnUnknownStateInsteadOfPollingOnIt(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("MYSTERY\n", "squeue")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	state, err := h.Wait(ctx, "5-n")

	require.ErrorIs(t, err, errJobText)
	require.ErrorContains(t, err, "MYSTERY")
	assert.Equal(t, model.JobUnknown, state)
}

func TestWait_AnSSHFailureIsNotAnOrdinaryFailure(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	h.now, h.sleep = clock.read, clock.sleep
	// The fake clock moves one poll per pause, and the wait ends after lostAfter on that clock.
	// A 1 ms poll makes 300 000 reads, which is more than the 5 s deadline on a busy host. A 10 s poll makes 30.
	h.poll = 10 * time.Second
	ssh.PlanResult(model.CommandResult{ExitCode: sshFailed, Stderr: "no route"}, "squeue")
	ssh.Plan("COMPLETED\n", "sacct")

	state, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, errLink)
	require.ErrorIs(t, err, host.ErrLost)
	require.ErrorContains(t, err, "ssh to lane-host: no route")
	assert.Equal(t, model.JobLost, state)
	assert.False(t, ssh.Called("sacct"), "sacct never answers for a host that ssh cannot reach")
}

func TestDrain_RefusesANodeNameThatCouldChangeACommandAndRunsNothing(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("-x\n", "sinfo")

	err := h.Drain(context.Background())

	require.ErrorIs(t, err, errNodeName)
	require.ErrorContains(t, err, "-x")
	assert.False(t, ssh.Called("scontrol"))
}

func TestWait_RevokedIsAFinalFailedState(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("REVOKED\n", "squeue")
	ssh.Plan("", "touch")

	state, err := h.Wait(deadline(t), "5-n")

	require.NoError(t, err)
	assert.Equal(t, model.JobFailed, state)
}

func TestStatus_ADownNodeRefusesAndNamesTheFix(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"down\n", "down*\n", "idle\ndown~\n"} {
		h := upHost(t, text)

		_, err := h.Status(context.Background())

		require.ErrorContains(t, err, "down")
		require.ErrorContains(t, err, "State=RESUME", text)
	}
}

// upHost returns a host whose cluster is up and whose sinfo prints text.
func upHost(t *testing.T, text string) *Host {
	t.Helper()

	h, sky, ssh := newScripted(t)
	sky.Plan(`[{"name": "lane-host", "status": "UP"}]`, "status")
	ssh.Plan(text, "sinfo")
	ssh.Plan(fakeDF, "df")

	return h
}
