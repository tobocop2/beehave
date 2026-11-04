package ssh_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/host/sshtest"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	failed  = 1
	dropped = 255
)

// machineWith returns a machine whose commands that start with a key of fail exit with the status, and whose commands that start with a key of say print the text.
func machineWith(fail map[string]int, say map[string]string) *sshtest.Machine {
	m := sshtest.New(hosttest.Working)
	m.Fail, m.Say = fail, say

	return m
}

func TestUp_EachStepThatFailsIsAnErrorThatKeepsItsCause(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		fail map[string]int
		say  map[string]string
		is   error
	}{
		"loginctl fails":      {fail: map[string]int{"loginctl": failed}, is: link.ErrCommand},
		"loginctl drops":      {fail: map[string]int{"loginctl": dropped}, is: link.ErrDropped},
		"loginctl says junk":  {say: map[string]string{"loginctl": "junk\n"}, is: nil},
		"the manager drops":   {fail: map[string]int{"systemctl --user is-system-running": dropped}, is: link.ErrDropped},
		"the manager stopped": {say: map[string]string{"systemctl --user is-system-running": "stopped\n"}, is: ssh.ErrSystemd},
		"the version fails":   {fail: map[string]int{"systemd-run --version": failed}, is: link.ErrCommand},
		"mkdir fails":         {fail: map[string]int{"mkdir": failed}, is: link.ErrCommand},
		"the root is missing": {fail: map[string]int{"test -d": failed}, is: link.ErrCommand},
	}

	for name, tc := range cases {
		h, _, _ := newHost(t, machineWith(tc.fail, tc.say), config())

		err := h.Up(t.Context())

		require.Error(t, err, name)

		if tc.is != nil {
			require.ErrorIs(t, err, tc.is, name)
		}
	}
}

func TestUp_ADegradedUserManagerStillRunsJobs(t *testing.T) {
	t.Parallel()

	h, _, _ := newHost(t, machineWith(nil, map[string]string{"systemctl --user is-system-running": "degraded\n"}), config())

	require.NoError(t, h.Up(t.Context()))
}

func TestStatus_ACommandThatFailsIsAnErrorAndADropIsAbsent(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		command string
		root    string
		status  int
	}{
		"the login folder": {command: "pwd", root: "", status: failed},
		"df":               {command: "df", root: sshtest.Root + "/work", status: failed},
		"the drain marker": {command: "test -e", root: sshtest.Root + "/work", status: 2},
	} {
		cfg := config()
		cfg.SSH.WorkRoot = tc.root

		h, _, _ := newHost(t, machineWith(map[string]int{tc.command: tc.status}, nil), cfg)
		_, err := h.Status(t.Context())
		require.Error(t, err, name)

		h, _, _ = newHost(t, machineWith(map[string]int{tc.command: dropped}, nil), cfg)
		got, err := h.Status(t.Context())
		require.NoError(t, err, name)
		assert.Equal(t, model.HostAbsent, got.State, name)
	}
}

func TestStatus_ADrainMarkerTestThatExitsWithAnotherStatusIsAnError(t *testing.T) {
	t.Parallel()

	h, _, _ := newHost(t, machineWith(map[string]int{"test -e": 2}, nil), config())

	_, err := h.Status(t.Context())

	require.ErrorIs(t, err, link.ErrCommand)
}

func TestSubmit_AStepThatFailsIsAnErrorAndStartsNoUnit(t *testing.T) {
	t.Parallel()

	for _, fail := range []map[string]int{{"test -d": failed}, {"test -e": 2}, {"mkdir": failed}} {
		m := machineWith(fail, nil)
		h, _, _ := newHost(t, m, config())

		_, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})

		require.Error(t, err, fail)
		assert.Empty(t, m.Units)
	}
}

func TestACallThatNeedsTheWorkRootFailsWhenTheRootCannotBeRead(t *testing.T) {
	t.Parallel()

	m := machineWith(map[string]int{"test -d": failed}, nil)
	h, _, _ := newHost(t, m, config())

	_, err := h.Log(t.Context(), "beehave-x")
	require.Error(t, err)

	state, err := h.Wait(t.Context(), "beehave-x")
	require.Error(t, err)
	assert.Equal(t, model.JobUnknown, state)

	require.Error(t, h.Cancel(t.Context(), "beehave-x"))
	require.Error(t, h.Drain(t.Context()))
	require.Error(t, h.Resume(t.Context()))

	bare, err := ssh.New("gates", config(), m.SSH())
	require.NoError(t, err)
	require.Error(t, bare.Teardown(t.Context()), "the default bank needs the work root")
}

func TestDrainAndResume_AFailedCommandIsAnError(t *testing.T) {
	t.Parallel()

	h, _, _ := newHost(t, machineWith(map[string]int{"touch": failed}, nil), config())
	require.ErrorIs(t, h.Drain(t.Context()), link.ErrCommand)

	h, _, _ = newHost(t, machineWith(map[string]int{"rm": failed}, nil), config())
	require.ErrorIs(t, h.Resume(t.Context()), link.ErrCommand)
}

func TestWait_APollThatFailsEndsAsUnknownWithTheError(t *testing.T) {
	t.Parallel()

	m := machineWith(nil, nil)
	h, _, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	m.Fail = map[string]int{"sh": 3}

	state, err := h.Wait(t.Context(), id)

	require.ErrorIs(t, err, link.ErrCommand)
	assert.Equal(t, model.JobUnknown, state)
}

func TestCancel_AStepThatFailsIsAnError(t *testing.T) {
	t.Parallel()

	for name, fail := range map[string]map[string]int{
		"the marker":        {"touch": failed},
		"the stop":          {"systemctl --user stop": failed},
		"a stop that drops": {"systemctl --user stop": dropped},
		"the unit end":      {"systemctl --user reset-failed": dropped},
	} {
		m := machineWith(nil, nil)
		h, _, _ := newHost(t, m, config())

		id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
		require.NoError(t, err)

		m.Fail = fail

		require.Error(t, h.Cancel(t.Context(), id), name)
	}
}

func TestWait_AUnitThatCannotBeEndedAfterTheBankIsAnErrorWithTheState(t *testing.T) {
	t.Parallel()

	for _, fail := range []map[string]int{{"systemctl --user reset-failed": dropped}, {"systemctl --user stop": failed}} {
		m := machineWith(nil, nil)
		h, _, _ := newHost(t, m, config())

		id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: hosttest.PassingCommand})
		require.NoError(t, err)

		m.Fail = fail

		state, err := h.Wait(t.Context(), id)

		require.Error(t, err, fail)
		assert.Equal(t, model.JobSucceeded, state)
	}
}

func TestCancel_AUnitThatKeepsRunningAfterTheStopEndsWithAdviceToStopItByHand(t *testing.T) {
	t.Parallel()

	m := machineWith(nil, nil)
	h, bank, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	m.Units[string(id)].Live = 1 << 20
	m.Units[string(id)].Lingers = 1 << 20

	err = h.Cancel(t.Context(), id)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "systemctl --user status "+string(id))
	assert.Empty(t, bank.calls, "work that a running job may still write is not banked")
}

func TestTeardown_WithTheDefaultBankScansTheMachineForRepositories(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)

	h, err := ssh.New("gates", config(), m.SSH())
	require.NoError(t, err)

	err = h.Teardown(t.Context())

	require.Error(t, err, "the fake machine has no find, so the scan fails and the work counts as unbanked")
	assert.NotZero(t, calledWith(m, "find"))
	assert.NotErrorIs(t, err, host.ErrNoRepos)
}

func TestWait_APollWithAValueTheTableDoesNotListIsAnError(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"LoadState=masked\nActiveState=inactive\nSubState=dead\nResult=success\nExecMainCode=0\nExecMainStatus=0\n",
		"beehave-size x\n" + sshtest.Running,
	} {
		m := machineWith(nil, nil)
		h, _, _ := newHost(t, m, config())

		id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
		require.NoError(t, err)

		m.Units[string(id)].Raw = []string{raw}

		_, err = h.Wait(t.Context(), id)

		require.Error(t, err, raw)
	}
}
