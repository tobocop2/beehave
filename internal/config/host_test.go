package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
)

const clusterProject = goodProject + "build_host = \"gpu\"\n"

func TestLoadFolder_HostTableDefaultsAndKeys(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, clusterProject+"[host]\nprovider = \"skypilot\"\ndisk_floor_gb = 50\n")

	require.Empty(t, problems)
	require.Equal(t, &model.HostConfig{Provider: model.HostSkyPilot, IdleMinutes: 60, MaxRun: 24 * time.Hour, DiskFloorGB: 50}, project.Host)

	project, problems = loadProjectText(t, clusterProject+"[host]\nprovider = \"skypilot\"\nidle_minutes = 30\nmax_run = \"12h\"\ndisk_floor_gb = 20\n"+
		"private_remote = \"git@example.com:org/repo.git\"\n[host.skypilot]\ncloud = \"gcp\"\nregion = \"us-central1\"\ninstance_type = \"n2-standard-8\"\n")

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

	const table = "[host]\nprovider = \"skypilot\"\ndisk_floor_gb = 50\n"

	tests := []struct {
		name  string
		text  string
		words string
	}{
		{"table_with_local_build", goodProject + "build_host = \"local\"\n" + table, "is not allowed when `build_host` is \"local\""},
		{"table_with_default_build", goodProject + table, "is not allowed when `build_host` is \"local\""},
		{"cluster_without_table", clusterProject, "is required when `build_host` is a cluster name"},
		{"missing_provider", clusterProject + "[host]\ndisk_floor_gb = 50\n", "`host.provider` must be a non-empty string"},
		{"unknown_provider", clusterProject + "[host]\nprovider = \"ssh\"\ndisk_floor_gb = 50\n", "use one of skypilot"},
		{"missing_disk_floor", clusterProject + "[host]\nprovider = \"skypilot\"\n", "`host.disk_floor_gb` is required"},
		{"zero_disk_floor", clusterProject + "[host]\nprovider = \"skypilot\"\ndisk_floor_gb = 0\n", "`host.disk_floor_gb` must be above zero"},
		{"huge_disk_floor", clusterProject + "[host]\nprovider = \"skypilot\"\ndisk_floor_gb = 17179869184\n", "`host.disk_floor_gb` must be above zero and at most 1048576; got 17179869184"},
		{"dash_private_remote", clusterProject + table + "private_remote = \"--upload-pack=x:y\"\n", "`host.private_remote` must name a machine"},
		{"negative_idle", clusterProject + table + "idle_minutes = -1\n", "`host.idle_minutes` must be above zero"},
		{"bad_max_run", clusterProject + table + "max_run = \"soon\"\n", "`host.max_run` must be a duration above zero"},
		{"zero_max_run", clusterProject + table + "max_run = \"0s\"\n", "`host.max_run` must be a duration above zero"},
		{"path_private_remote", clusterProject + table + "private_remote = \"/srv/repo.git\"\n", "`host.private_remote` must name a machine"},
		{"table_of_another_provider", clusterProject + table + "[host.gcloud]\ncloud = \"gcp\"\n", "unknown key `host.gcloud`"},
		{"unknown_host_key", clusterProject + table + "shade = 1\n", "unknown key `host.shade`"},
		{"unknown_provider_key", clusterProject + table + "[host.skypilot]\nzone = \"a\"\n", "unknown key `host.skypilot.zone`"},
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
