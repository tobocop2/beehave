package ssh_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/host/sshtest"
	"github.com/tobocop2/beehave/internal/model"
)

func TestHost_PassesTheConformanceSuiteAsAMachineTheOwnerProvides(t *testing.T) {
	t.Parallel()

	hosttest.TestHost(t, func(t *testing.T, mode hosttest.Mode) host.Host {
		t.Helper()

		h, _, _ := newHost(t, sshtest.New(mode), config())

		return h
	}, hosttest.NonOwning())
}

func TestSubmit_RunsTheCommandAsATransientUnitThatKeepsItsResult(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: `echo "$$ ${HOME} %h"; exit 3`})

	require.NoError(t, err)
	assert.Equal(t, model.JobID("beehave-n1"), id)

	u := m.Units["beehave-n1"]
	require.NotNil(t, u)
	assert.Equal(t, `echo "$$ ${HOME} %h"; exit 3`, u.Command, "the command reaches the unit byte for byte")

	root := sshtest.Root + "/work"
	assert.Equal(t, []string{
		"systemd-run", "--user", "--quiet", "--unit=beehave-n1", "--remain-after-exit",
		"-p", "RuntimeMaxSec=7200", "-p", "TimeoutStopSec=30", "-p", "WorkingDirectory=" + root,
		"-p", "StandardOutput=append:" + root + "/.beehave/beehave-n1.log", "-p", "StandardError=inherit",
		"--", "sh", "-c", `eval "$(printf %s ` + base64.StdEncoding.EncodeToString([]byte(u.Command)) + ` | base64 -d)"`,
	}, u.Args)
	assert.NotContains(t, strings.Join(u.Args, " "), "CollectMode")
	assert.NotContains(t, strings.Join(u.Args, " "), "TasksMax")
}

func TestSubmit_RefusesWhatItCannotRun(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	_, err := h.Submit(t.Context(), model.JobSpec{Name: "", Command: "x"})
	require.Error(t, err)
	_, err = h.Submit(t.Context(), model.JobSpec{Name: "x", Command: ""})
	require.Error(t, err)

	assert.Zero(t, calledWith(m, "systemd-run"))
}

func TestSubmit_ADrainingHostTakesNoJob(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	require.NoError(t, h.Drain(t.Context()))

	_, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})

	require.ErrorIs(t, err, ssh.ErrDraining)
	assert.Zero(t, calledWith(m, "systemd-run"))
}

func TestSubmit_AFailedSystemdRunHoldsNothingAndReturnsTheError(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	m.NoRun = true
	h, _, _ := newHost(t, m, config())

	_, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})

	require.ErrorIs(t, err, link.ErrCommand)
	assert.Empty(t, m.Units)
}

func TestWait_BanksTheWorkThenEndsTheUnit(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: hosttest.PassingCommand})
	require.NoError(t, err)

	state, err := h.Wait(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	require.Len(t, bank.calls, 1)

	after := words(m)[bank.calls[0]:]
	assert.Equal(t, []string{"systemctl --user reset-failed beehave-n1", "systemctl --user stop beehave-n1"}, after, "reset-failed and stop run after the bank")
}

func TestWait_AFailedBankKeepsTheUnitAndReturnsTheStateWithTheError(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())
	bank.err = assert.AnError

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: hosttest.FailingCommand})
	require.NoError(t, err)

	state, err := h.Wait(t.Context(), id)

	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, model.JobFailed, state)
	assert.Zero(t, calledWith(m, "systemctl --user stop"))
	assert.Zero(t, calledWith(m, "systemctl --user reset-failed"))
}

func TestWait_RefusesAnIdThatSubmitDidNotReturn(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	for _, id := range []model.JobID{"", "7", "beehave-", "beehave-x; rm -rf /", "-beehave-x", "beehave-x y"} {
		_, err := h.Wait(t.Context(), id)
		require.Error(t, err, id)
		require.Error(t, h.Cancel(t.Context(), id), id)
		_, err = h.Log(t.Context(), id)
		require.Error(t, err, id)
	}

	assert.Zero(t, calledWith(m, "sh"), "no refused id reaches the machine")
}

// A unit that is gone after the marker is canceled whatever systemd says; a stopped unit reads not-found, success or timeout.
func TestWait_TheStateTableOfASystemdUnit(t *testing.T) {
	t.Parallel()

	prop := func(load, active, sub, result, code, status string) string {
		return "LoadState=" + load + "\nActiveState=" + active + "\nSubState=" + sub + "\nResult=" + result + "\nExecMainCode=" + code + "\nExecMainStatus=" + status + "\n"
	}

	cases := []struct {
		name   string
		raw    string
		marker bool
		want   model.JobState
	}{
		{"exited with status 0", prop("loaded", "active", "exited", "success", "1", "0"), false, model.JobSucceeded},
		{"exited by SIGTERM on the main process", prop("loaded", "active", "exited", "success", "2", "15"), false, model.JobFailed},
		{"exit code", prop("loaded", "failed", "failed", "exit-code", "1", "3"), false, model.JobFailed},
		{"exit 255", prop("loaded", "failed", "failed", "exit-code", "1", "255"), false, model.JobFailed},
		{"killed by a signal", prop("loaded", "failed", "failed", "signal", "2", "9"), false, model.JobFailed},
		{"killed by the oom killer", prop("loaded", "failed", "failed", "oom-kill", "2", "9"), false, model.JobFailed},
		{"run limit", prop("loaded", "failed", "failed", "timeout", "2", "15"), false, model.JobTimedOut},
		{"run limit after the marker", prop("loaded", "failed", "failed", "timeout", "2", "9"), true, model.JobCanceled},
		{"not found after the marker", prop("not-found", "inactive", "dead", "success", "0", "0"), true, model.JobCanceled},
		{"success after the marker", prop("loaded", "active", "exited", "success", "2", "15"), true, model.JobCanceled},
		{"not found", prop("not-found", "inactive", "dead", "success", "0", "0"), false, model.JobDied},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := sshtest.New(hosttest.Working)
			h, _, _ := newHost(t, m, config())

			id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
			require.NoError(t, err)

			m.Units[string(id)].Raw = []string{tc.raw}
			m.Files[sshtest.Root+"/work/.beehave/"+string(id)+".canceled"] = tc.marker

			state, err := h.Wait(t.Context(), id)

			require.NoError(t, err)
			assert.Equal(t, tc.want, state)
		})
	}
}

// Every Result that systemd documents for a service that ended with a fault is a failed job. Only the timeout is a run limit.
func TestWait_EveryDocumentedFaultOfAServiceIsAFailedJob(t *testing.T) {
	t.Parallel()

	for _, result := range []string{"exit-code", "signal", "oom-kill", "core-dump", "watchdog", "resources", "start-limit-hit", "protocol", "exec-condition"} {
		t.Run(result, func(t *testing.T) {
			t.Parallel()

			m := sshtest.New(hosttest.Working)
			h, bank, _ := newHost(t, m, config())

			id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
			require.NoError(t, err)

			m.Units[string(id)].Raw = []string{"LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=" + result + "\nExecMainCode=2\nExecMainStatus=11\n"}

			state, err := h.Wait(t.Context(), id)

			require.NoError(t, err)
			assert.Equal(t, model.JobFailed, state)
			assert.Len(t, bank.calls, 1, "a failed job banks its work")
		})
	}
}

// A unit that systemd is still stopping or reloading is not final. The poll must keep waiting, with or without the marker of a cancel.
func TestWait_AUnitInTheMiddleOfAChangeIsStillRunning(t *testing.T) {
	t.Parallel()

	rec := func(active, sub string) string {
		return "LoadState=loaded\nActiveState=" + active + "\nSubState=" + sub + "\nResult=success\nExecMainCode=0\nExecMainStatus=0\n"
	}

	for _, tc := range []struct{ active, sub string }{
		{"deactivating", "stop-sigterm"},
		{"deactivating", "stop-sigkill"},
		{"deactivating", "stop-post"},
		{"deactivating", "final-sigterm"},
		{"deactivating", "final-sigkill"},
		{"deactivating", "stop"},
		{"reloading", "reload"},
		{"refreshing", "reload"},
	} {
		for _, marker := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/marker=%t", tc.active, tc.sub, marker), func(t *testing.T) {
				t.Parallel()

				m := sshtest.New(hosttest.Working)
				h, bank, _ := newHost(t, m, config())

				id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
				require.NoError(t, err)

				end := sshtest.Succeeded
				if marker {
					end = rec("inactive", "dead")
					end = strings.Replace(end, "LoadState=loaded", "LoadState=not-found", 1)
				}

				m.Units[string(id)].Raw = []string{rec(tc.active, tc.sub), rec(tc.active, tc.sub), end}
				m.Files[sshtest.Root+"/work/.beehave/"+string(id)+".canceled"] = marker

				state, err := h.Wait(t.Context(), id)

				require.NoError(t, err, "a unit in %s/%s is not an error", tc.active, tc.sub)
				assert.Contains(t, []model.JobState{model.JobSucceeded, model.JobCanceled}, state)
				assert.Equal(t, 3, calledWith(m, "sh -c"), "the two reads in the change were polls, not final states")
				assert.Len(t, bank.calls, 1, "the work is banked once, at the end")
			})
		}
	}
}

// The result of a job stays readable after its unit is released, as the record of a Slurm job stays in the accounting.
func TestWait_ASecondWaitAfterTheReleaseReadsTheSameResult(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
		want model.JobState
	}{
		{"succeeded", sshtest.Succeeded, model.JobSucceeded},
		{"failed", sshtest.Failed, model.JobFailed},
		{"timed out", "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=timeout\nExecMainCode=2\nExecMainStatus=15\n", model.JobTimedOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := sshtest.New(hosttest.Working)
			h, bank, clock := newHost(t, m, config())

			id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
			require.NoError(t, err)

			m.Units[string(id)].Raw = []string{tc.raw}

			first, err := h.Wait(t.Context(), id)
			require.NoError(t, err)
			require.Equal(t, tc.want, first)
			require.True(t, m.Units[string(id)].Reset, "the unit was released")

			start := clock.now
			second, err := h.Wait(t.Context(), id)

			require.NoError(t, err)
			assert.Equal(t, tc.want, second, "the unit is gone, the record of the result is not")
			assert.Less(t, clock.now.Sub(start), 30*time.Second, "no wait for the died grace")
			assert.Len(t, bank.calls, 2, "a replayed wait banks again: the bank is safe to repeat")
		})
	}
}

// A record of a state that beehave never writes (died, or any word) is an error, never a guess.
func TestWait_ARecordOfAStateBeehaveNeverWritesIsAnError(t *testing.T) {
	t.Parallel()

	for _, word := range []string{"died", "running", "nonsense"} {
		m := sshtest.New(hosttest.Working)
		h, _, _ := newHost(t, m, config())

		id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
		require.NoError(t, err)

		m.Say = map[string]string{"sh -c": "beehave-result " + word + "\n" + sshtest.Succeeded}

		state, err := h.Wait(t.Context(), id)

		require.Error(t, err, word)
		assert.Equal(t, model.JobUnknown, state)
	}
}

// A job that was never read as final leaves no record: a unit that is gone with no marker and no record is died.
func TestWait_AGoneUnitWithNoRecordIsDied(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	m.Units[string(id)].Vanished = true

	state, err := h.Wait(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobDied, state)
}

func TestWait_ValuesTheTableDoesNotListAreErrors(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=mystery\nExecMainCode=1\nExecMainStatus=0\n",
		"LoadState=loaded\nActiveState=inactive\nSubState=dead\nResult=success\nExecMainCode=0\nExecMainStatus=0\n",
		"LoadState=loaded\nActiveState=maintenance\nSubState=dead\nResult=success\nExecMainCode=0\nExecMainStatus=0\n",
		"LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainStatus=0\n",
		"LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainCode=x\nExecMainStatus=0\n",
		"garbage\n",
		"",
	} {
		m := sshtest.New(hosttest.Working)
		h, _, _ := newHost(t, m, config())

		id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
		require.NoError(t, err)

		m.Units[string(id)].Raw = []string{raw}

		state, err := h.Wait(t.Context(), id)

		require.Error(t, err, raw)
		assert.Equal(t, model.JobUnknown, state)
	}
}

func TestWait_AnActivatingUnitIsPendingAndARunningUnitIsRunning(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	m.Units[string(id)].Raw = []string{
		"LoadState=loaded\nActiveState=activating\nSubState=start\nResult=success\nExecMainCode=0\nExecMainStatus=0\n",
		sshtest.Running, sshtest.Succeeded,
	}

	state, err := h.Wait(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.Equal(t, 3, calledWith(m, "sh -c"), "the two live reads were polls, not final states")
	assert.Len(t, bank.calls, 1)
}

func TestWait_ALinkThatStaysDownEndsAsLostAndNeverSubmitsAgain(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	m.Mu.Lock()
	m.Drops = -1
	m.Mu.Unlock()

	state, err := h.Wait(t.Context(), id)

	require.ErrorIs(t, err, host.ErrLost)
	assert.Equal(t, model.JobLost, state)
	assert.Contains(t, err.Error(), "systemctl --user")
	assert.Empty(t, bank.calls)
	assert.Equal(t, 1, calledWith(m, "systemd-run"))
}

func TestWait_AStaleLogCancelsTheUnitWithAMarkerThenStopsIt(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	u := m.Units[string(id)]
	u.Live = 1 << 20
	u.LogSizes = []int{10}

	state, err := h.Wait(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, model.JobStale, state)
	assert.True(t, m.Files[sshtest.Root+"/work/.beehave/beehave-n1.canceled"])
	assert.True(t, u.Stopped)
	assert.Len(t, bank.calls, 1)
}

func TestCancel_WritesTheMarkerThenStopsThenWaitsThenBanks(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	m.Units[string(id)].Lingers = 3
	m.Units[string(id)].Live = 1 << 20
	before := len(words(m))

	require.NoError(t, h.Cancel(t.Context(), id))

	got := words(m)[before:]
	require.GreaterOrEqual(t, len(got), 4)
	assert.Equal(t, "touch "+sshtest.Root+"/work/.beehave/beehave-n1.canceled", got[0])
	assert.Equal(t, "systemctl --user stop beehave-n1", got[1])
	assert.Len(t, bank.calls, 1)
	assert.GreaterOrEqual(t, bank.calls[0]-before, 2+4, "the bank waits until a unit that still runs after the stop is gone")

	state, err := h.Wait(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, model.JobCanceled, state, "the marker, not the unit, tells a canceled job")
}

func TestCancel_AStopThatFailsReturnsTheErrorAndBanksNothing(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	m.NoStop = true

	require.ErrorIs(t, h.Cancel(t.Context(), id), link.ErrCommand)
	assert.Empty(t, bank.calls)
}

func TestCancel_AUnitThatIsNotLoadedCountsAsStopped(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	m.Units[string(id)].Vanished = true

	require.NoError(t, h.Cancel(t.Context(), id))
}

func TestLog_ReadsTheEndOfTheLogFile(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	out, err := h.Log(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, "log text\n", out)
	assert.Equal(t, "tail -c 16384 "+sshtest.Root+"/work/.beehave/beehave-n1.log", words(m)[len(words(m))-1])
}
