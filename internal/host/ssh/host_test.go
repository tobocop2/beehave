package ssh_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/hosttest"
	"github.com/tobocop2/obee/internal/host/link"
	"github.com/tobocop2/obee/internal/host/ssh"
	"github.com/tobocop2/obee/internal/host/sshtest"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

func TestUp_ChecksTheMachineAndMakesOnlyItsOwnFolder(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	require.NoError(t, h.Up(t.Context()))
	require.NoError(t, h.Up(t.Context()), "Up is idempotent")

	assert.True(t, m.Files[sshtest.Root+"/work/.obee/"])
	assert.Zero(t, calledWith(m, "systemd-run --user"), "Up starts no unit")
	assert.Zero(t, calledWith(m, "loginctl enable-linger"), "obee never turns linger on")
}

func TestUp_RefusesAMachineWhoseLingerIsOffAndNamesTheFix(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	m.LingerOff = true
	h, _, _ := newHost(t, m, config())

	err := h.Up(t.Context())

	require.ErrorIs(t, err, ssh.ErrLinger)
	assert.Contains(t, err.Error(), "loginctl enable-linger lane")
	assert.Zero(t, calledWith(m, "loginctl enable-linger"), "obee never turns linger on")
	assert.False(t, m.Files[sshtest.Root+"/work/.obee/"], "a refused Up changes nothing")
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
		cfg := config()
		cfg.SSH.WorkRoot = tc.root
		h, _, _ := newHost(t, sshtest.New(hosttest.Working), cfg)

		got, err := h.WorkRoot(t.Context())

		require.NoError(t, err, name)
		assert.Equal(t, tc.want, got, name)
	}
}

func TestWorkRoot_ALoginFolderThatIsNotOneAbsolutePathIsAnError(t *testing.T) {
	t.Parallel()

	cfg := config()
	cfg.SSH.WorkRoot = ""
	runner := proctest.Always(model.CommandResult{Stdout: "relative\n"})

	h, err := ssh.New("gates", cfg, runner)
	require.NoError(t, err)

	_, err = h.WorkRoot(t.Context())

	require.Error(t, err)
}

func TestNew_RefusesASettingThatCouldChangeACommandLine(t *testing.T) {
	t.Parallel()

	for name, edit := range map[string]func(*model.HostConfig){
		"a percent in the work root": func(c *model.HostConfig) { c.SSH.WorkRoot = "/home/a%h" },
		"a dollar in the work root":  func(c *model.HostConfig) { c.SSH.WorkRoot = "/home/$HOME" },
		"a relative work root":       func(c *model.HostConfig) { c.SSH.WorkRoot = "work" },
		"a max run under a second":   func(c *model.HostConfig) { c.MaxRun = 0 },
		"a bad address":              func(c *model.HostConfig) { c.SSH.Address = "-x" },
	} {
		cfg := config()
		edit(&cfg)

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
	assert.Empty(t, runner.Calls, "New runs no ssh, so `obee check config` needs no network")
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
