package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadFolder_BuildHostListsTheClustersARunMayChooseAndTheFirstIsTheDefault(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, build_host = ["gpu", "gpu-b"], host = `+hostCall("")))

	require.Empty(t, problems)
	require.Equal(t, "gpu", project.BuildHost, "the first name is the default of a run")
	require.Equal(t, []string{"gpu", "gpu-b"}, project.BuildHosts)

	project, problems = loadProjectText(t, call(clusterProject+", host = "+hostCall("")))

	require.Empty(t, problems)
	require.Equal(t, "gpu", project.BuildHost)
	require.Empty(t, project.BuildHosts, "one name has no other host to choose")
}

func TestLoadFolder_BuildHostRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		words string
	}{
		{"local_with_a_cluster", call(goodProject + `, build_host = ["local", "gpu-b"], host = ` + hostCall("")), "lists \"local\" with other hosts"},
		{"repeats_a_name", call(goodProject + `, build_host = ["gpu-b", "gpu-b"], host = ` + hostCall("")), "`build_host` lists `gpu-b` twice"},
		{"bad_name", call(goodProject + `, build_host = ["gpu", "a b"], host = ` + hostCall("")), "`build_host` must be \"local\" or a SkyPilot cluster name"},
		{"empty_list", call(goodProject + `, build_host = []`), "`build_host` is an empty list"},
		{"not_a_name", call(goodProject + `, build_host = 3`), "want a name or a list of names"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, test.text)

			require.NotEmpty(t, problems)
			require.ErrorContains(t, problems[0], test.words)
		})
	}
}

func TestLoadFolder_TheKeyBuildHostsIsNotAKey(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(clusterProject+`, build_hosts = ["gpu-b"], host = `+hostCall("")))

	require.NotEmpty(t, problems, "obee has one key for the hosts of a project")
}
