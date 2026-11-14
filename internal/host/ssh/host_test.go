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
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func TestUp_ChecksTheMachineAndMakesOnlyItsOwnFolder(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	require.NoError(t, h.Up(t.Context()))
	require.NoError(t, h.Up(t.Context()), "Up is idempotent")

	assert.True(t, m.Files[sshtest.Root+"/work/.beehave/"])
	assert.Zero(t, calledWith(m, "systemd-run --user"), "Up starts no unit")
	assert.Zero(t, calledWith(m, "loginctl enable-linger"), "beehave never turns linger on")
}

func TestUp_RefusesAMachineWhoseLingerIsOffAndNamesTheFix(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	m.LingerOff = true
	h, _, _ := newHost(t, m, config())

	err := h.Up(t.Context())

	require.ErrorIs(t, err, ssh.ErrLinger)
	assert.Contains(t, err.Error(), "loginctl enable-linger lane")
	assert.Zero(t, calledWith(m, "loginctl enable-linger"), "beehave never turns linger on")
	assert.False(t, m.Files[sshtest.Root+"/work/.beehave/"], "a refused Up changes nothing")
}

func TestUp_RefusesAMachineWithoutAUserManagerOrWithAnOldSystemd(t *testing.T) {
	t.Parallel()

	for name, set := range map[string]func(*sshtest.Machine){
		"no user manager":   func(m *sshtest.Machine) { m.NoSystemd = true },
		"an old systemd":    func(m *sshtest.Machine) { m.SystemdText = "systemd 239 (239-58.el8)" },
		"text not systemd":  func(m *sshtest.Machine) { m.SystemdText = "busybox" },
		"a version too big": func(m *sshtest.Machine) { m.SystemdText = "systemd 99999999999999999999" },
	} {
		m := sshtest.New(hosttest.Working)
		set(m)
		h, _, _ := newHost(t, m, config())

		require.ErrorIs(t, h.Up(t.Context()), ssh.ErrSystemd, name)
	}
}

func TestUp_ALinkThatDropsIsACannotReachError(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Unreachable)
	h, _, _ := newHost(t, m, config())

	err := h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrDropped)
	assert.Contains(t, err.Error(), "gates")
}

func TestStatus_IsAbsentWhenSSHCannotConnectAndUpWhenItAnswers(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Unreachable)
	h, _, _ := newHost(t, m, config())

	got, err := h.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, model.HostAbsent, got.State)

	m.Mu.Lock()
	m.Offline = false
	m.Mu.Unlock()

	got, err = h.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, model.HostUp, got.State)
	assert.True(t, got.Disk.Readable)
	assert.Equal(t, uint64(94371840)*1024, got.Disk.FreeBytes)
}

func TestStatus_ADiskThatDoesNotParseIsNotReadable(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.UnreadableDisk)
	h, _, _ := newHost(t, m, config())

	got, err := h.Status(t.Context())

	require.NoError(t, err)
	assert.Equal(t, model.HostUp, got.State)
	assert.False(t, got.Disk.Readable)
}

func TestStatus_ADrainMarkerReadsAsDrainingUntilResume(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	require.NoError(t, h.Drain(t.Context()))
	got, err := h.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, model.HostDraining, got.State)

	require.NoError(t, h.Resume(t.Context()))
	got, err = h.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, model.HostUp, got.State)
}

func TestDown_SendsNothingAndStopsNoJob(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	require.NoError(t, h.Down(t.Context()))

	assert.Empty(t, m.Calls)
}

func TestTeardown_BanksTheWorkAndLeavesTheMachineUp(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())

	require.NoError(t, h.Teardown(t.Context()))

	assert.Len(t, bank.calls, 1)

	got, err := h.Status(t.Context())
	require.NoError(t, err)
	assert.Equal(t, model.HostUp, got.State)
}

func TestTeardown_AFailedBankStaysAnErrorThatSaysTheMachineKeepsItsWork(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())
	bank.err = host.ErrUnbanked

	err := h.Teardown(t.Context())

	require.ErrorIs(t, err, host.ErrUnbanked)
}

func TestWorkRoot_IsTheConfiguredFolderOrTheLoginFolder(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ root, want string }{
		"absolute":      {sshtest.Root + "/work", sshtest.Root + "/work"},
		"home relative": {"~/work", sshtest.Root + "/work"},
		"login folder":  {"", sshtest.Root},
	} {
		h, _, _ := newHost(t, sshtest.New(hosttest.Working), configRoot(tc.root))

		got, err := h.WorkRoot(t.Context())

		require.NoError(t, err, name)
		assert.Equal(t, tc.want, got, name)
	}
}

func TestWorkRoot_ALoginFolderThatIsNotOneAbsolutePathIsAnError(t *testing.T) {
	t.Parallel()

	runner := proctest.Always(model.CommandResult{Stdout: "relative\n"})

	h, err := ssh.New("gates", configRoot(""), runner)
	require.NoError(t, err)

	_, err = h.WorkRoot(t.Context())

	require.Error(t, err)
}

func TestNew_RefusesASettingThatCouldChangeACommandLine(t *testing.T) {
	t.Parallel()

	for name, cfg := range map[string]model.HostConfig{
		"a percent in the work root": configRoot("/home/a%h"),
		"a dollar in the work root":  configRoot("/home/$HOME"),
		"a relative work root":       configRoot("work"),
		"a max run under a second":   func() model.HostConfig { c := config(); c.MaxRun = 0; return c }(),
		"a bad address":              configWith(model.TextEntry(ssh.BlockAddress, "-x")),
		"an address with a space":    configWith(model.TextEntry(ssh.BlockAddress, "a b")),
		"a user that is an option":   configWith(model.TextEntry(ssh.BlockUser, "-x")),
		"a known_hosts that is off":  configWith(model.TextEntry(ssh.BlockKnownHosts, "no")),
	} {
		_, err := ssh.New("gates", cfg, proctest.Always(model.CommandResult{}))

		require.ErrorIs(t, err, link.ErrConfig, name)
	}

	_, err := ssh.New("-oProxyCommand=x", config(), proctest.Always(model.CommandResult{}))
	require.ErrorIs(t, err, link.ErrConfig)
}

func TestNew_BuildsNoConnection(t *testing.T) {
	t.Parallel()

	runner := proctest.Always(model.CommandResult{})

	_, err := ssh.New("gates", config(), runner)

	require.NoError(t, err)
	assert.Empty(t, runner.Calls, "New runs no ssh, so `beehave check config` needs no network")
}

func TestEveryCallPutsTheTargetAfterTheGuardAndAllowsNoWeakHostKeyPolicy(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	require.NoError(t, h.Up(t.Context()))

	for _, options := range m.Options {
		assert.Contains(t, options, "StrictHostKeyChecking=yes")
		assert.NotContains(t, options, "StrictHostKeyChecking=no")
		assert.Contains(t, options, "BatchMode=yes")
	}
}
