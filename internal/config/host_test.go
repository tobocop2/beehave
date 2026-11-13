package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/slurm"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/model"
)

const clusterProject = hostProject + `,build_host = "gpu"`

// hostCall returns a valid host call with extra arguments added after the two required ones. Each extra starts with ", ".
func hostCall(extra string) string {
	return `host(provider = "skypilot", disk_floor_gb = 50` + extra + ")"
}

// sshCall returns a valid ssh host call with extra arguments added. Each extra starts with ", ".
func sshCall(extra string) string {
	return `host(provider = "ssh", disk_floor_gb = 50` + extra + ")"
}

func TestLoadFolder_SSHAndSlurmHosts(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+sshCall("")))
	require.Empty(t, problems)
	require.Equal(t, &model.HostConfig{Provider: ssh.Name, StaleMinutes: 20, MaxRun: 24 * time.Hour, BankBudget: 20 * time.Second, DiskFloorGB: 50}, project.Host)

	project, problems = loadProjectText(t, call(clusterProject+", host = "+sshCall(`, ssh = ssh(user = "ci")`)))
	require.Empty(t, problems)

	user, given := project.Host.Block.Text(ssh.BlockUser)
	require.True(t, given)
	require.Equal(t, "ci", user)

	project, problems = loadProjectText(t, call(clusterProject+`, host = host(provider = "slurm", disk_floor_gb = 50,
  slurm = slurm(address = "10.0.0.5", user = "ci", port = 2222, identity = "~/.ssh/ci", work_root = "/home/ci/work", known_hosts = "accept-new"))`))
	require.Empty(t, problems)
	require.Equal(t, slurm.Name, project.Host.Provider)

	port, given := project.Host.Block.Whole(slurm.BlockPort)
	require.True(t, given)
	require.Equal(t, 2222, port)

	address, _ := project.Host.Block.Text(slurm.BlockAddress)
	require.Equal(t, "10.0.0.5", address)
}

func TestLoadFolder_HostTableDefaultsAndKeys(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall("")))

	require.Empty(t, problems)
	require.Equal(t, &model.HostConfig{Provider: skypilot.Name, IdleMinutes: 60, StaleMinutes: 20, MaxRun: 24 * time.Hour, BankBudget: 20 * time.Second, DiskFloorGB: 50}, project.Host)

	project, problems = loadProjectText(t, call(clusterProject+`, host = host(provider = "skypilot", idle_minutes = 30, stale_minutes = 5, max_run = "12h", bank_budget = "45s", disk_floor_gb = 20, disk_wait_minutes = 15,
  private_remote = "git@example.com:org/repo.git",
  skypilot = skypilot(cloud = "gcp", region = "us-central1", instance_type = "n2-standard-8"))`))

	require.Empty(t, problems)

	block := project.Host.Block
	project.Host.Block = nil
	require.Equal(t, &model.HostConfig{
		Provider: skypilot.Name, IdleMinutes: 30, StaleMinutes: 5, MaxRun: 12 * time.Hour, BankBudget: 45 * time.Second, DiskFloorGB: 20, DiskWaitMinutes: 15, PrivateRemote: "git@example.com:org/repo.git",
	}, project.Host)

	for key, want := range map[string]string{skypilot.BlockCloud: "gcp", skypilot.BlockRegion: "us-central1", skypilot.BlockInstanceType: "n2-standard-8"} {
		got, given := block.Text(key)
		require.True(t, given, key)
		require.Equal(t, want, got, key)
	}
}

func TestLoadFolder_ALocalProjectHasNoHostTable(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject))

	require.Empty(t, problems)
	require.Nil(t, project.Host)
}

func TestLoadFolder_HostTableRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		words string
	}{
		{"table_with_local_build", call(goodProject + `, build_host = "local", host = ` + hostCall("")), "p.star:1:"},
		{"table_with_local_build_names_the_block", call(goodProject + `, build_host = "local", host = ` + hostCall("")), "the `host` block is not allowed when `build_host` is \"local\""},
		{"table_with_default_build", call(goodProject + ", host = " + hostCall("")), "the `host` block is not allowed when `build_host` is \"local\""},
		{"cluster_without_table", call(clusterProject), "p.star:1:8: the `host` block is required when `build_host` is a cluster name"},
		{"missing_provider", call(clusterProject + ", host = host(disk_floor_gb = 50)"), "host: missing argument for provider"},
		{"blank_provider", call(clusterProject + `, host = host(provider = " ", disk_floor_gb = 50)`), "`host.provider` must be a non-empty string"},
		{"unknown_provider", call(clusterProject + `, host = host(provider = "nomad", disk_floor_gb = 50)`), "`host.provider`: unknown host provider \"nomad\": use one of skypilot, slurm, ssh"},
		{"ssh_block_on_skypilot", call(clusterProject + ", host = " + hostCall(`, ssh = ssh(user = "ci")`)), "`host.provider` \"skypilot\" takes no `ssh` block. Remove the `ssh` block."},
		{"skypilot_block_on_ssh", call(clusterProject + `, host = ` + sshCall(`, skypilot = skypilot(cloud = "gcp")`)), "`host.provider` \"ssh\" takes no `skypilot` block. Remove the `skypilot` block."},
		{"skypilot_block_on_slurm", call(clusterProject + `, host = host(provider = "slurm", disk_floor_gb = 50, skypilot = skypilot())`), "`host.provider` \"slurm\" takes no `skypilot` block. Remove the `skypilot` block."},
		{"idle_on_ssh", call(clusterProject + ", host = " + sshCall(", idle_minutes = 30")), "`host.idle_minutes` is not allowed for provider \"ssh\""},
		{"idle_on_slurm", call(clusterProject + `, host = host(provider = "slurm", disk_floor_gb = 50, idle_minutes = 30)`), "`host.idle_minutes` is not allowed for provider \"slurm\""},
		{"unknown_ssh_argument", call(clusterProject + ", host = " + sshCall(`, ssh = ssh(zone = "a")`)), `ssh: unexpected keyword argument "zone"`},
		{"repeated_ssh_call", "ssh()\nssh()\n" + call(clusterProject), "p.star:2:4: block `ssh` appears twice"},
		{"missing_disk_floor", call(clusterProject + `, host = host(provider = "skypilot")`), "host: missing argument for disk_floor_gb"},
		{"zero_disk_floor", call(clusterProject + `, host = host(provider = "skypilot", disk_floor_gb = 0)`), "`host.disk_floor_gb` must be above zero"},
		{"huge_disk_floor", call(clusterProject + `, host = host(provider = "skypilot", disk_floor_gb = 17179869184)`), "`host.disk_floor_gb` must be above zero and at most 1048576; got 17179869184"},
		{"dash_private_remote", call(clusterProject + ", host = " + hostCall(`, private_remote = "--upload-pack=x:y"`)), "`host.private_remote` must name a machine"},
		{"negative_idle", call(clusterProject + ", host = " + hostCall(", idle_minutes = -1")), "`host.idle_minutes` must be above zero"},
		{"zero_stale", call(clusterProject + ", host = " + hostCall(", stale_minutes = 0")), "`host.stale_minutes` must be above zero"},
		{"negative_stale", call(clusterProject + ", host = " + hostCall(", stale_minutes = -5")), "`host.stale_minutes` must be above zero"},
		{"negative_disk_wait", call(clusterProject + ", host = " + hostCall(", disk_wait_minutes = -1")), "`host.disk_wait_minutes` must be zero or above and at most 1048576; got -1"},
		{"bad_max_run", call(clusterProject + ", host = " + hostCall(`, max_run = "soon"`)), "`host.max_run` must be a duration above zero"},
		{"zero_max_run", call(clusterProject + ", host = " + hostCall(`, max_run = "0s"`)), "`host.max_run` must be a duration above zero"},
		{"bad_bank_budget", call(clusterProject + ", host = " + hostCall(`, bank_budget = "soon"`)), "`host.bank_budget` must be a duration above zero, such as \"20s\""},
		{"zero_bank_budget", call(clusterProject + ", host = " + hostCall(`, bank_budget = "0s"`)), "`host.bank_budget` must be a duration above zero"},
		{"path_private_remote", call(clusterProject + ", host = " + hostCall(`, private_remote = "/srv/repo.git"`)), "`host.private_remote` must name a machine"},
		{"unknown_host_argument", call(clusterProject + ", host = " + hostCall(", shade = 1")), `host: unexpected keyword argument "shade"`},
		{"unknown_skypilot_argument", call(clusterProject + ", host = " + hostCall(`, skypilot = skypilot(zone = "a")`)), `skypilot: unexpected keyword argument "zone"`},
		{"host_argument_of_the_wrong_type", call(clusterProject + ", host = " + hostCall(`, idle_minutes = "soon"`)), "p.star:1:"},
		{"host_that_is_not_a_block", call(clusterProject + `, host = "gpu"`), `for parameter "host": got string, want host(...)`},
		{"skypilot_that_is_not_a_block", call(clusterProject + ", host = " + hostCall(`, skypilot = "gcp"`)), `for parameter "skypilot": got string, want skypilot(...)`},
		{"repeated_host_call", call(clusterProject+", host = "+hostCall("")) + "host(provider = \"skypilot\", disk_floor_gb = 1)\n", "p.star:2:5: block `host` appears twice"},
		{"repeated_skypilot_call", "skypilot()\nskypilot()\n" + call(clusterProject), "p.star:2:9: block `skypilot` appears twice"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, tc.text)

			require.Len(t, problems, 1)
			require.ErrorIs(t, problems[0], config.ErrRefused)
			require.ErrorContains(t, problems[0], tc.words)
		})
	}
}

func TestLoadFolder_ABlockValueIsAnOpaqueValue(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "x = {host(provider = \"skypilot\", disk_floor_gb = 1): 1}\n"+call(goodProject))
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "p.star:1:")

	_, problems = loadProjectText(t, "_h = host(provider = \"skypilot\", disk_floor_gb = 1)\n"+call(clusterProject+", host = _h"))
	require.Empty(t, problems, "a global block value is frozen at the end of the file")

	project, problems := loadProjectText(t,
		"def f():\n  h = host(provider = \"skypilot\", disk_floor_gb = 1)\n  _ = [bool(h), str(h), type(h)]\n  return h\n"+call(clusterProject+", host = f()"))
	require.Empty(t, problems)
	require.NotNil(t, project.Host)
}
