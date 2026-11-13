package ssh_test

import (
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
		h, _, _ := newHost(t, sshtest.New(hosttest.Working), configWith(map[string]string{ssh.BlockWorkRoot: tc.root}))

		got, err := h.WorkRoot(t.Context())

		require.NoError(t, err, name)
		assert.Equal(t, tc.want, got, name)
	}
}

func TestWorkRoot_ALoginFolderThatIsNotOneAbsolutePathIsAnError(t *testing.T) {
	t.Parallel()

	runner := proctest.Always(model.CommandResult{Stdout: "relative\n"})

	h, err := ssh.New("gates", configWith(map[string]string{ssh.BlockWorkRoot: ""}), runner)
	require.NoError(t, err)

	_, err = h.WorkRoot(t.Context())

	require.Error(t, err)
}

func TestNew_RefusesASettingThatCouldChangeACommandLine(t *testing.T) {
	t.Parallel()

	for name, set := range map[string]map[string]string{
		"a percent in the work root": {ssh.BlockWorkRoot: "/home/a%h"},
		"a dollar in the work root":  {ssh.BlockWorkRoot: "/home/$HOME"},
		"a relative work root":       {ssh.BlockWorkRoot: "work"},
		"a bad address":              {ssh.BlockAddress: "-x"},
		"an address with a space":    {ssh.BlockAddress: "a b"},
		"a bad user":                 {ssh.BlockUser: "-oProxyCommand=x"},
		"an unknown host key policy": {ssh.BlockKnownHosts: "off"},
	} {
		_, err := ssh.New("gates", configWith(set), proctest.Always(model.CommandResult{}))

		require.ErrorIs(t, err, link.ErrConfig, name)
	}

	cfg := config()
	cfg.MaxRun = 0

	_, err := ssh.New("gates", cfg, proctest.Always(model.CommandResult{}))
	require.ErrorIs(t, err, link.ErrConfig, "a max run under a second")

	_, err = ssh.New("-oProxyCommand=x", config(), proctest.Always(model.CommandResult{}))
	require.ErrorIs(t, err, link.ErrConfig)
}

func TestNew_ReadsTheKeysOfTheBlock(t *testing.T) {
	t.Parallel()

	whole := func(key string, value int) model.HostConfig {
		cfg := config()
		cfg.Block = model.NewBlock("t", []model.BlockEntry{model.WholeEntry(key, value)})

		return cfg
	}

	text := func(key, value string) model.HostConfig {
		cfg := config()
		cfg.Block = model.NewBlock("t", []model.BlockEntry{model.TextEntry(key, value)})

		return cfg
	}

	tests := map[string]struct {
		cfg     model.HostConfig
		refused string
	}{
		"port_zero":                {cfg: whole(ssh.BlockPort, 0), refused: "from 1 to 65535"},
		"port_above_the_range":     {cfg: whole(ssh.BlockPort, 65536), refused: "from 1 to 65535"},
		"port_negative":            {cfg: whole(ssh.BlockPort, -1), refused: "from 1 to 65535"},
		"port_at_the_top":          {cfg: whole(ssh.BlockPort, 65535)},
		"port_at_the_bottom":       {cfg: whole(ssh.BlockPort, 1)},
		"identity_relative":        {cfg: text(ssh.BlockIdentity, "id_ed25519"), refused: "absolute path"},
		"identity_control":         {cfg: text(ssh.BlockIdentity, "/home/a\nb"), refused: "no control character"},
		"identity_under_home":      {cfg: text(ssh.BlockIdentity, "~/.ssh/id")},
		"work_root_relative":       {cfg: text(ssh.BlockWorkRoot, "work"), refused: "absolute path"},
		"work_root_control":        {cfg: text(ssh.BlockWorkRoot, "/work\x00"), refused: "no control character"},
		"nil_block":                {cfg: model.HostConfig{MaxRun: time.Hour}},
		"block_with_no_entry":      {cfg: model.HostConfig{MaxRun: time.Hour, Block: model.NewBlock("t", nil)}},
		"known_hosts_accept_new":   {cfg: text(ssh.BlockKnownHosts, model.KnownHostsAcceptNew)},
		"known_hosts_not_a_policy": {cfg: text(ssh.BlockKnownHosts, "off"), refused: "known_hosts"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if tt.cfg.MaxRun == 0 {
				tt.cfg.MaxRun = time.Hour
			}

			_, err := ssh.New("gates", tt.cfg, proctest.Always(model.CommandResult{}))
			if tt.refused == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, link.ErrConfig)
			require.ErrorContains(t, err, tt.refused)
		})
	}
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

func TestBlock_DeclaresTheKeysOfTheLinkAndTheWorkRootAllOptional(t *testing.T) {
	t.Parallel()

	schema := ssh.Block()

	var keys []string

	for _, arg := range schema.Args {
		assert.True(t, arg.Optional, arg.Key)

		keys = append(keys, arg.Key)
	}

	assert.Equal(t, "ssh", schema.Name)
	assert.ElementsMatch(t, []string{ssh.BlockAddress, ssh.BlockUser, ssh.BlockPort, ssh.BlockIdentity, ssh.BlockWorkRoot, ssh.BlockKnownHosts}, keys)
}
