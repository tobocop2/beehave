package slurm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

// testDeadline is the longest a test that can block may run. A parser that is wrong makes Wait poll for ever, so the test fails here instead.
const testDeadline = 5 * time.Second

// deadline returns a context that ends after testDeadline or when the test ends.
func deadline(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), testDeadline)
	t.Cleanup(cancel)

	return ctx
}

func config() model.HostConfig {
	return model.HostConfig{IdleMinutes: 45, MaxRun: 12 * time.Hour}
}

func newScripted(t *testing.T) (*Queue, *proctest.Scripted) {
	t.Helper()

	ssh := &proctest.Scripted{}
	h := New("lane-host", config(), ssh, stubSlot{})

	h.Poll = time.Millisecond
	h.Bank = func(context.Context) error { return nil }
	h.Nonce = func() string { return "n" }

	return h, ssh
}

func TestWait_RefusesTwoStatesFromSqueueWithoutAskingSacct(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)
	ssh.Plan("RUNNING\nRUNNING\n", "squeue")

	state, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, errJobText)
	assert.Equal(t, model.JobUnknown, state)
	assert.False(t, ssh.Called("sacct"))
}

func TestWait_RefusesAnUnknownStateInsteadOfPollingOnIt(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)
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

	h, ssh := newScripted(t)
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	h.Now, h.Sleep = clock.read, clock.sleep
	// The fake clock moves one poll per pause, and the wait ends after lostAfter on that clock.
	// A 1 ms poll makes 300 000 reads, which is more than the 5 s deadline on a busy host. A 10 s poll makes 30.
	h.Poll = 10 * time.Second
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

	h, ssh := newScripted(t)
	ssh.Plan("-x\n", "sinfo")

	err := h.Drain(context.Background())

	require.ErrorIs(t, err, errNodeName)
	require.ErrorContains(t, err, "-x")
	assert.False(t, ssh.Called("scontrol"))
}

func TestWait_RevokedIsAFinalFailedState(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)
	ssh.Plan("REVOKED\n", "squeue")

	state, err := h.Wait(deadline(t), "5-n")

	require.NoError(t, err)
	assert.Equal(t, model.JobFailed, state)
}

func TestSubmit_QuotesTheCommandAndRefusesAnAnswerItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)
	ssh.Plan("77;cluster1\n", "sbatch")

	id, err := h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: "make check; echo 'done'"})
	require.NoError(t, err)
	assert.Equal(t, model.JobID("77-n"), id)
	assert.Equal(t, []string{"-o", "BatchMode=yes", "lane-host", "sbatch", "--parsable", "--job-name=gate", `'--wrap=make check; echo '\''done'\'''`}, ssh.Calls[0])

	for _, answer := range []string{"", "Submitted batch job 77", "77;", "-1", "77\n78"} {
		ssh.Plan(answer, "sbatch")

		_, err = h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: "x"})
		require.ErrorIs(t, err, errJobID, answer)
	}

	_, err = h.Submit(context.Background(), model.JobSpec{Name: "", Command: "x"})
	require.ErrorIs(t, err, errJobFields)

	ssh.PlanFail("boom", "sbatch")

	_, err = h.Submit(context.Background(), model.JobSpec{Name: "g", Command: "x"})
	require.ErrorIs(t, err, ErrCommand)
}

func TestWait_ReadsTheStateFromSlurmAndMapsEachName(t *testing.T) {
	t.Parallel()

	tests := map[string]model.JobState{
		"COMPLETED": model.JobSucceeded, "FAILED": model.JobFailed, "TIMEOUT": model.JobTimedOut, "OUT_OF_MEMORY": model.JobFailed,
		"NODE_FAIL": model.JobFailed, "CANCELLED": model.JobCanceled, "CANCELLED by 0": model.JobCanceled, "CANCELLED+": model.JobCanceled, //nolint:misspell // Slurm's spelling
	}

	for text, want := range tests {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			h, ssh := newScripted(t)
			ssh.Plan(text+"\n", "squeue")

			got, err := h.Wait(deadline(t), "5-n")
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestWait_AJobThatEndedWithExit124IsTimedOutNotFailed(t *testing.T) {
	t.Parallel()

	tests := map[string]model.JobState{
		"FAILED|124:0\n": model.JobTimedOut, "FAILED|1:0\n": model.JobFailed, "FAILED|0:124\n": model.JobFailed,
	}

	for text, want := range tests {
		h, ssh := newScripted(t)
		ssh.PlanFail("slurm_load_jobs error: Invalid job id specified", "squeue")
		ssh.Plan(text, "sacct")

		got, err := h.Wait(deadline(t), "5-n")
		require.NoError(t, err, text)
		assert.Equal(t, want, got, text)
	}
}

func TestWait_KeepsPollingWhileTheJobIsPendingOrRunning(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)
	ssh.Plan("PENDING\n", "squeue")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := h.Wait(ctx, "5-n")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWait_ReadsSacctWhenSqueueForgotTheJob(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)
	ssh.PlanFail("slurm_load_jobs error: Invalid job id specified", "squeue")
	ssh.Plan("FAILED\n", "sacct")

	got, err := h.Wait(deadline(t), "5-n")
	require.NoError(t, err)
	assert.Equal(t, model.JobFailed, got)

	ssh.Plan("", "squeue")

	got, err = h.Wait(deadline(t), "5-n")
	require.NoError(t, err)
	assert.Equal(t, model.JobFailed, got)
}

func TestWait_RefusesWhatItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*proctest.Scripted){
		"squeue state unknown": func(ssh *proctest.Scripted) { ssh.Plan("MYSTERY\n", "squeue") },
		"squeue two lines":     func(ssh *proctest.Scripted) { ssh.Plan("RUNNING\nRUNNING\n", "squeue") },
		"squeue lower case":    func(ssh *proctest.Scripted) { ssh.Plan("completed\n", "squeue") },
		"sacct fails": func(ssh *proctest.Scripted) {
			ssh.PlanFail("x", "squeue")
			ssh.PlanFail("x", "sacct")
		},
		"sacct unknown state": func(ssh *proctest.Scripted) {
			ssh.PlanFail("x", "squeue")
			ssh.Plan("MYSTERY\n", "sacct")
		},
	}

	for name, plan := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, ssh := newScripted(t)
			plan(ssh)

			state, err := h.Wait(deadline(t), "5-n")
			require.Error(t, err)
			assert.Equal(t, model.JobUnknown, state)
		})
	}
}

func TestLog_ReadsTheTailOfTheSlurmLogOfTheJob(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)
	ssh.Plan("line\nobee-gate-done\n", "tail")

	log, err := h.Log(deadline(t), "5-n")

	require.NoError(t, err)
	assert.Equal(t, "line\nobee-gate-done\n", log)
	assert.Equal(t, []string{"-o", "BatchMode=yes", "lane-host", "tail", "-c", "16384", "slurm-5.out"}, ssh.Calls[0])

	_, err = h.Log(deadline(t), "5; rm -rf x")
	require.Error(t, err)
	assert.Len(t, ssh.Calls, 1)
}

func TestDrainAndResume_UpdateEveryNode(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)
	ssh.Plan("n1\nn2\nn1\n", "sinfo")
	ssh.Plan("", "scontrol")

	require.NoError(t, h.Drain(context.Background()))
	require.NoError(t, h.Resume(context.Background()))

	var updates []string

	for _, call := range ssh.Calls {
		if call[3] != "sinfo" {
			updates = append(updates, strings.Join(call[3:], " "))
		}
	}

	assert.Equal(t, []string{
		"scontrol update NodeName=n1 State=DRAIN Reason=obee", "scontrol update NodeName=n2 State=DRAIN Reason=obee",
		"scontrol update NodeName=n1 State=RESUME", "scontrol update NodeName=n2 State=RESUME",
	}, updates)
}

func TestDrain_RefusesWhatItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*proctest.Scripted){
		"no node":          func(ssh *proctest.Scripted) { ssh.Plan("\n", "sinfo") },
		"node with a flag": func(ssh *proctest.Scripted) { ssh.Plan("-x\n", "sinfo") },
		"node with space":  func(ssh *proctest.Scripted) { ssh.Plan("a b\n", "sinfo") },
		"sinfo fails":      func(ssh *proctest.Scripted) { ssh.PlanFail("x", "sinfo") },
		"scontrol fails": func(ssh *proctest.Scripted) {
			ssh.Plan("n1\n", "sinfo")
			ssh.PlanFail("x", "scontrol")
		},
	}

	for name, plan := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, ssh := newScripted(t)
			plan(ssh)

			require.Error(t, h.Drain(context.Background()))
			require.Error(t, h.Resume(context.Background()))
		})
	}
}

func TestWaitAndCancel_RefuseAnIdWithoutAValidNonce(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)

	for _, id := range []model.JobID{"5", "5-", "-n", "5-n;rm", "5-n-m", "5 n", "x-n", "5-n\n", "", "7; rm", "-1", "7;cluster", "5-n/m", "5/6-n", "5-'n'", `5-"n"`, "5-../n"} {
		state, err := h.Wait(deadline(t), id)
		require.ErrorIs(t, err, errBadJobID, string(id))
		assert.Equal(t, model.JobUnknown, state)
		require.ErrorIs(t, h.Cancel(context.Background(), id), errBadJobID, string(id))
	}

	assert.Empty(t, ssh.Calls)
}

func TestWorkRoot_RefusesAnythingButOneAbsolutePath(t *testing.T) {
	t.Parallel()

	for _, answer := range []string{"", "home/lane\n", "/home/a\n/home/b\n", "pwd: not found"} {
		h, ssh := newScripted(t)
		ssh.Plan(answer, "pwd")

		_, err := h.WorkRoot(context.Background())

		require.ErrorIs(t, err, errWorkRoot, answer)
	}

	h, ssh := newScripted(t)
	ssh.PlanFail("boom", "pwd")

	_, err := h.WorkRoot(context.Background())

	require.ErrorIs(t, err, ErrCommand)
}
