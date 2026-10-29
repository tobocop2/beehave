package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
)

const clusterProject = goodProject + `, build_host = "gpu"`

// hostCall returns a valid host call with extra arguments added after the two required ones. Each extra starts with ", ".
func hostCall(extra string) string {
	return `host(provider = "skypilot", disk_floor_gb = 50` + extra + ")"
}

func TestLoadFolder_HostTableDefaultsAndKeys(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall("")))

	require.Empty(t, problems)
	require.Equal(t, &model.HostConfig{Provider: model.HostSkyPilot, IdleMinutes: 60, MaxRun: 24 * time.Hour, DiskFloorGB: 50}, project.Host)

	project, problems = loadProjectText(t, call(clusterProject+`, host = host(provider = "skypilot", idle_minutes = 30, max_run = "12h", disk_floor_gb = 20,
  private_remote = "git@example.com:org/repo.git",
  skypilot = skypilot(cloud = "gcp", region = "us-central1", instance_type = "n2-standard-8"))`))

	require.Empty(t, problems)
	require.Equal(t, &model.HostConfig{
		Provider: model.HostSkyPilot, IdleMinutes: 30, MaxRun: 12 * time.Hour, DiskFloorGB: 20, PrivateRemote: "git@example.com:org/repo.git",
		SkyPilot: model.SkyPilotConfig{Cloud: "gcp", Region: "us-central1", InstanceType: "n2-standard-8"},
	}, project.Host)
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
		{"unknown_provider", call(clusterProject + `, host = host(provider = "ssh", disk_floor_gb = 50)`), "`host.provider`: unknown host provider \"ssh\": use one of skypilot"},
		{"missing_disk_floor", call(clusterProject + `, host = host(provider = "skypilot")`), "host: missing argument for disk_floor_gb"},
		{"zero_disk_floor", call(clusterProject + `, host = host(provider = "skypilot", disk_floor_gb = 0)`), "`host.disk_floor_gb` must be above zero"},
		{"huge_disk_floor", call(clusterProject + `, host = host(provider = "skypilot", disk_floor_gb = 17179869184)`), "`host.disk_floor_gb` must be above zero and at most 1048576; got 17179869184"},
		{"dash_private_remote", call(clusterProject + ", host = " + hostCall(`, private_remote = "--upload-pack=x:y"`)), "`host.private_remote` must name a machine"},
		{"negative_idle", call(clusterProject + ", host = " + hostCall(", idle_minutes = -1")), "`host.idle_minutes` must be above zero"},
		{"bad_max_run", call(clusterProject + ", host = " + hostCall(`, max_run = "soon"`)), "`host.max_run` must be a duration above zero"},
		{"zero_max_run", call(clusterProject + ", host = " + hostCall(`, max_run = "0s"`)), "`host.max_run` must be a duration above zero"},
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
