package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/model"
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

func TestLoadFolder_SSHAndSlurmHostsKeepTheirBlockUnread(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+sshCall("")))
	require.Empty(t, problems)
	require.Equal(t, &model.HostConfig{Provider: "ssh", StaleMinutes: 20, MaxRun: 24 * time.Hour, BankBudget: 20 * time.Second, LostJobWait: time.Hour, LostAfter: 5 * time.Minute, StopLimit: 5 * time.Minute, DiskFloorGB: 50, GateSlots: 1}, project.Host, "a host with no block holds none")

	project, problems = loadProjectText(t, call(clusterProject+", host = "+sshCall(`, ssh = ssh(user = "ci")`)))
	require.Empty(t, problems)

	user, given := project.Host.Block.Text("user")
	require.True(t, given)
	require.Equal(t, "ci", user)

	project, problems = loadProjectText(t, call(clusterProject+`, host = host(provider = "slurm", disk_floor_gb = 50,
  slurm = slurm(address = "10.0.0.5", user = "ci", port = 2222, identity = "~/.ssh/ci", known_hosts = "accept-new"))`))
	require.Empty(t, problems)
	require.Equal(t, model.HostProvider("slurm"), project.Host.Provider)
	require.Equal(t, `{address="10.0.0.5" identity="~/.ssh/ci" known_hosts="accept-new" port=2222 user="ci"}`, project.Host.Block.String())
}

func TestLoadFolder_HostTableDefaultsAndKeys(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall("")))

	require.Empty(t, problems)
	require.Equal(t, &model.HostConfig{Provider: "skypilot", IdleMinutes: 60, StaleMinutes: 20, MaxRun: 24 * time.Hour, BankBudget: 20 * time.Second, LostJobWait: time.Hour, LostAfter: 5 * time.Minute, StopLimit: 5 * time.Minute, DiskFloorGB: 50, GateSlots: 1}, project.Host)

	project, problems = loadProjectText(t, call(clusterProject+`, host = host(provider = "skypilot", idle_minutes = 30, stale_minutes = 5, max_run = "12h", bank_budget = "45s", lost_job_wait = "2h", lost_after = "3m", stop_limit = "4m", disk_floor_gb = 20, disk_wait_minutes = 15,
  private_remote = "git@example.com:org/repo.git",
  skypilot = skypilot(cloud = "gcp", region = "us-central1", instance_type = "n2-standard-8"))`))

	require.Empty(t, problems)
	require.Equal(t, &model.HostConfig{
		Provider: "skypilot", IdleMinutes: 30, StaleMinutes: 5, MaxRun: 12 * time.Hour, BankBudget: 45 * time.Second, LostJobWait: 2 * time.Hour, LostAfter: 3 * time.Minute, StopLimit: 4 * time.Minute, DiskFloorGB: 20, GateSlots: 1, DiskWaitMinutes: 15, PrivateRemote: "git@example.com:org/repo.git",
		Block: project.Host.Block,
	}, project.Host)
	require.Equal(t, `{cloud="gcp" instance_type="n2-standard-8" region="us-central1"}`, project.Host.Block.String())
}

func TestLoadFolder_AHostRunLimitOfExactlyOneSecondLoads(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(`, max_run = "1s"`)))

	require.Empty(t, problems)
	require.Equal(t, time.Second, project.Host.MaxRun)
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
		{"ssh_block_on_skypilot", call(clusterProject + ", host = " + hostCall(`, ssh = ssh(user = "ci")`)), "the `host` block holds the `ssh` block, and `host.provider` \"skypilot\" does not take it"},
		{"skypilot_block_on_ssh", call(clusterProject + `, host = ` + sshCall(`, skypilot = skypilot(cloud = "gcp")`)), "the `host` block holds the `skypilot` block, and `host.provider` \"ssh\" does not take it"},
		{"skypilot_block_on_slurm", call(clusterProject + `, host = host(provider = "slurm", disk_floor_gb = 50, skypilot = skypilot())`), "the `host` block holds the `skypilot` block, and `host.provider` \"slurm\" does not take it"},
		{"ssh_block_on_slurm", call(clusterProject + `, host = host(provider = "slurm", disk_floor_gb = 50, ssh = ssh())`), "the `host` block holds the `ssh` block, and `host.provider` \"slurm\" does not take it"},
		{"slurm_block_on_ssh", call(clusterProject + ", host = " + sshCall(`, slurm = slurm()`)), "the `host` block holds the `slurm` block, and `host.provider` \"ssh\" does not take it"},
		{"idle_on_ssh", call(clusterProject + ", host = " + sshCall(", idle_minutes = 30")), "`host.idle_minutes` is not allowed for provider \"ssh\""},
		{"setup_command_on_ssh", call(clusterProject + ", host = " + sshCall(`, setup_command = "make setup"`)), "`host.setup_command` is not allowed for provider \"ssh\": beehest does not create the machine"},
		{"setup_command_on_slurm", call(clusterProject + `, host = host(provider = "slurm", disk_floor_gb = 50, setup_command = "make setup")`), "`host.setup_command` is not allowed for provider \"slurm\": beehest does not create the machine"},
		{"setup_command_empty", call(clusterProject + ", host = " + hostCall(`, setup_command = "  "`)), "`host.setup_command` must not be empty"},
		{"setup_command_of_the_wrong_type", call(clusterProject + ", host = " + hostCall(", setup_command = 3")), `for parameter "setup_command": got int, want string`},
		{"idle_on_slurm", call(clusterProject + `, host = host(provider = "slurm", disk_floor_gb = 50, idle_minutes = 30)`), "`host.idle_minutes` is not allowed for provider \"slurm\""},
		{"ssh_port_of_the_wrong_type", call(clusterProject + ", host = " + sshCall(`, ssh = ssh(port = "22")`)), `ssh: for parameter "port": got string, want a whole number`},
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
		{"bad_max_run", call(clusterProject + ", host = " + hostCall(`, max_run = "soon"`)), "`host.max_run` must be a duration of at least 1s, such as \"24h\"; got `soon`"},
		{"zero_max_run", call(clusterProject + ", host = " + hostCall(`, max_run = "0s"`)), "`host.max_run` must be a duration of at least 1s, such as \"24h\"; got `0s`"},
		{"negative_max_run", call(clusterProject + ", host = " + hostCall(`, max_run = "-5m"`)), "`host.max_run` must be a duration of at least 1s"},
		{"sub_second_max_run", call(clusterProject + ", host = " + hostCall(`, max_run = "500ms"`)), "`host.max_run` must be a duration of at least 1s, such as \"24h\"; got `500ms`"},
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

func TestHostConfig_LostJobWaitDefaultsToSixtyMinutes(t *testing.T) {
	t.Parallel()

	hosts := map[string]string{
		"skypilot": hostCall(""),
		"ssh":      sshCall(""),
		"slurm":    `host(provider = "slurm", disk_floor_gb = 50)`,
	}

	for name, text := range hosts {
		project, problems := loadProjectText(t, call(clusterProject+", host = "+text))
		require.Empty(t, problems, name)
		require.Equal(t, time.Hour, project.Host.LostJobWait, "%s: a host with no lost_job_wait waits 60 minutes", name)
	}

	for _, tc := range []struct {
		text string
		want time.Duration
	}{
		{"90m", 90 * time.Minute},
		{"1m", time.Minute},
		{"24h", 24 * time.Hour},
	} {
		project, problems := loadProjectText(t, call(clusterProject+", host = "+sshCall(`, lost_job_wait = "`+tc.text+`"`)))
		require.Empty(t, problems, tc.text)
		require.Equal(t, tc.want, project.Host.LostJobWait, tc.text)
	}
}

func TestHostConfig_TheLostAndStopLimitsDefaultToFiveMinutesAndAreDistinctFromLostJobWait(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{"skypilot": hostCall(""), "ssh": sshCall(""), "slurm": `host(provider = "slurm", disk_floor_gb = 50)`} {
		project, problems := loadProjectText(t, call(clusterProject+", host = "+text))
		require.Empty(t, problems, name)
		require.Equal(t, 5*time.Minute, project.Host.LostAfter, name)
		require.Equal(t, 5*time.Minute, project.Host.StopLimit, name)
	}

	project, problems := loadProjectText(t, call(clusterProject+", host = "+sshCall(`, lost_after = "90s"`)))
	require.Empty(t, problems)
	require.Equal(t, 90*time.Second, project.Host.LostAfter)
	require.Equal(t, 5*time.Minute, project.Host.StopLimit, "the stop limit keeps its default when only lost_after is written")
	require.Equal(t, time.Hour, project.Host.LostJobWait, "lost_job_wait is a separate key")

	project, problems = loadProjectText(t, call(clusterProject+", host = "+sshCall(`, stop_limit = "2m"`)))
	require.Empty(t, problems)
	require.Equal(t, 2*time.Minute, project.Host.StopLimit)
	require.Equal(t, 5*time.Minute, project.Host.LostAfter, "lost_after keeps its default when only stop_limit is written")
}

func TestHostConfig_CredentialsNoneLoadsForSSHAndSlurmAndAnUnsetKeyStaysEmptyForEveryProvider(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{"ssh": sshCall(`, credentials = "none"`), "slurm": `host(provider = "slurm", disk_floor_gb = 50, credentials = "none")`} {
		project, problems := loadProjectText(t, call(clusterProject+", host = "+text))
		require.Empty(t, problems, name)
		require.Equal(t, model.HostCredentialsNone, project.Host.Credentials, name)
	}

	for name, text := range map[string]string{"skypilot": hostCall(""), "ssh": sshCall("")} {
		project, problems := loadProjectText(t, call(clusterProject+", host = "+text))
		require.Empty(t, problems, name)
		require.Empty(t, project.Host.Credentials, name)
	}
}

func TestHostConfig_CredentialsNoneOnAProviderThatStartsItsMachineWithCredentialsOfTheOwnerIsRefusedWithTheReasonAndTheFix(t *testing.T) {
	t.Parallel()

	for name, extra := range map[string]string{
		"alone":         "",
		"with a set-up": `, setup_command = "make setup"`,
		"with a block":  `, skypilot = skypilot(cloud = "gcp")`,
	} {
		_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(`, credentials = "none"`+extra)))

		require.Len(t, problems, 1, name)
		require.ErrorIs(t, problems[0], config.ErrRefused, name)
		require.ErrorContains(t, problems[0], `"none" is not allowed for provider "skypilot"`, name)
		require.ErrorContains(t, problems[0], "forward your SSH agent and copies your cloud credentials", name)
		require.ErrorContains(t, problems[0], "Remove the key.", name)
	}

	_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(`, credentials = "some"`)))
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], `the only value is "none"`, "a value that is not none is refused for its value first")
}

func TestHostConfig_ACredentialsValueOtherThanNoneIsRefusedAndNamesNone(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"some", "None", ""} {
		_, problems := loadProjectText(t, call(clusterProject+", host = "+sshCall(`, credentials = "`+text+`"`)))

		require.Len(t, problems, 1, text)
		require.ErrorIs(t, problems[0], config.ErrRefused, text)
		require.ErrorContains(t, problems[0], "`host.credentials`", text)
	}

	_, problems := loadProjectText(t, call(clusterProject+", host = "+sshCall(`, credentials = "some"`)))
	require.ErrorContains(t, problems[0], `the only value is "none"`)
}

func TestHostConfig_ALostAfterOrStopLimitThatIsNegativeOrNotADurationIsRefused(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"lost_after", "stop_limit"} {
		for _, text := range []string{"-1m", "0s", "soon", ""} {
			_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(", "+key+` = "`+text+`"`)))

			require.Len(t, problems, 1, key+" "+text)
			require.ErrorIs(t, problems[0], config.ErrRefused, key+" "+text)
			require.ErrorContains(t, problems[0], "`host."+key+"` must be a duration above zero, such as \"5m\"; got `"+text+"`. Fix the value.", key+" "+text)
		}
	}
}

func TestHostConfig_ALostJobWaitThatIsNotAnAcceptableDurationIsRefused(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"soon", "", "30s", "59s", "0s", "-5m", "24h1s", "25h"} {
		_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(`, lost_job_wait = "`+text+`"`)))

		require.Len(t, problems, 1, text)
		require.ErrorIs(t, problems[0], config.ErrRefused, text)
		require.ErrorContains(t, problems[0], "`host.lost_job_wait` must be a duration from 1m to 24h, such as \"60m\"; got `"+text+"`. Fix the value.", text)
	}

	_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(", lost_job_wait = 60")))

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], `for parameter "lost_job_wait": got int, want string`)
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

func TestLoadFolder_AHostSetupCommandOfAProviderThatBeehestCreatesReachesTheHostConfigAndAnOmittedOneStaysEmpty(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(`, setup_command = "make setup"`)))

	require.Empty(t, problems)
	require.Equal(t, "make setup", project.Host.SetupCommand)

	project, problems = loadProjectText(t, call(clusterProject+", host = "+hostCall("")))

	require.Empty(t, problems)
	require.Empty(t, project.Host.SetupCommand)
}
