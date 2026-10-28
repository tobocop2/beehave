package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
)

const clusterProject = goodProject + "build_host = \"gpu\"\n"

// hostBlock returns a valid host block with extra lines added inside it.
func hostBlock(extra string) string {
	return "host {\nprovider = \"skypilot\"\ndisk_floor_gb = 50\n" + extra + "}\n"
}

func TestLoadFolder_HostTableDefaultsAndKeys(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, clusterProject+hostBlock(""))

	require.Empty(t, problems)
	require.Equal(t, &model.HostConfig{Provider: model.HostSkyPilot, IdleMinutes: 60, MaxRun: 24 * time.Hour, DiskFloorGB: 50}, project.Host)

	project, problems = loadProjectText(t, clusterProject+"host {\nprovider = \"skypilot\"\nidle_minutes = 30\nmax_run = \"12h\"\ndisk_floor_gb = 20\n"+
		"private_remote = \"git@example.com:org/repo.git\"\nskypilot {\ncloud = \"gcp\"\nregion = \"us-central1\"\ninstance_type = \"n2-standard-8\"\n}\n}\n")

	require.Empty(t, problems)
	require.Equal(t, &model.HostConfig{
		Provider: model.HostSkyPilot, IdleMinutes: 30, MaxRun: 12 * time.Hour, DiskFloorGB: 20, PrivateRemote: "git@example.com:org/repo.git",
		SkyPilot: model.SkyPilotConfig{Cloud: "gcp", Region: "us-central1", InstanceType: "n2-standard-8"},
	}, project.Host)
}

func TestLoadFolder_ALocalProjectHasNoHostTable(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, goodProject)

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
		{"table_with_local_build", goodProject + "build_host = \"local\"\n" + hostBlock(""), "p.hcl:8:1: the `host` block is not allowed when `build_host` is \"local\""},
		{"table_with_default_build", goodProject + hostBlock(""), "p.hcl:7:1: the `host` block is not allowed when `build_host` is \"local\""},
		{"cluster_without_table", clusterProject, "p.hcl:7:1: the `host` block is required when `build_host` is a cluster name"},
		{"missing_provider", clusterProject + "host {\ndisk_floor_gb = 50\n}\n", "p.hcl:8:1: `host.provider` must be a non-empty string"},
		{"blank_provider", clusterProject + "host {\nprovider = \" \"\ndisk_floor_gb = 50\n}\n", "p.hcl:9:1: `host.provider` must be a non-empty string"},
		{"unknown_provider", clusterProject + "host {\nprovider = \"ssh\"\ndisk_floor_gb = 50\n}\n", "p.hcl:9:1: `host.provider`: unknown host provider \"ssh\": use one of skypilot"},
		{"missing_disk_floor", clusterProject + "host {\nprovider = \"skypilot\"\n}\n", "p.hcl:8:1: `host.disk_floor_gb` is required"},
		{"zero_disk_floor", clusterProject + "host {\nprovider = \"skypilot\"\ndisk_floor_gb = 0\n}\n", "p.hcl:10:1: `host.disk_floor_gb` must be above zero"},
		{"huge_disk_floor", clusterProject + "host {\nprovider = \"skypilot\"\ndisk_floor_gb = 17179869184\n}\n", "p.hcl:10:1: `host.disk_floor_gb` must be above zero and at most 1048576; got 17179869184"},
		{"dash_private_remote", clusterProject + hostBlock("private_remote = \"--upload-pack=x:y\"\n"), "p.hcl:11:1: `host.private_remote` must name a machine"},
		{"negative_idle", clusterProject + hostBlock("idle_minutes = -1\n"), "p.hcl:11:1: `host.idle_minutes` must be above zero"},
		{"bad_max_run", clusterProject + hostBlock("max_run = \"soon\"\n"), "p.hcl:11:1: `host.max_run` must be a duration above zero"},
		{"zero_max_run", clusterProject + hostBlock("max_run = \"0s\"\n"), "p.hcl:11:1: `host.max_run` must be a duration above zero"},
		{"path_private_remote", clusterProject + hostBlock("private_remote = \"/srv/repo.git\"\n"), "p.hcl:11:1: `host.private_remote` must name a machine"},
		{"table_of_another_provider", clusterProject + hostBlock("gcloud {\ncloud = \"gcp\"\n}\n"), "unknown block `gcloud`. Remove it"},
		{"unknown_host_key", clusterProject + hostBlock("shade = 1\n"), "unknown key `shade`. Remove it"},
		{"unknown_provider_key", clusterProject + hostBlock("skypilot {\nzone = \"a\"\n}\n"), "unknown key `zone`. Remove it"},
		{"host_key_of_the_wrong_type", clusterProject + hostBlock("idle_minutes = \"soon\"\n"), "p.hcl:"},
		{"repeated_host_block", clusterProject + hostBlock("") + hostBlock(""), "p.hcl:12:1: block `host` appears twice"},
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
