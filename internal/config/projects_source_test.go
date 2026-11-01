package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

const (
	localProject = `source = "/srv/repo", base_branch = "main", gate = "make check", test_report = "r.xml"`
	tail         = `base_branch = "main", gate = "g", test_report = "r.xml"`
)

func TestLoadFolder_AProjectWithOnlySourceBaseBranchAndGateLoadsWithLocalAdapters(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject))

	require.Empty(t, problems)
	require.Equal(t, model.Project{
		Name: "p", Tracker: model.TrackerLocal, Claims: model.ClaimsLocal, Source: "/srv/repo", BaseBranch: "main", Gate: "make check", TestReport: "r.xml", BuildHost: "local",
	}, project.Project)
}

func TestLoadFolder_AFileWithOnlyTheOldKeysGetsLocalClaims(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject))

	require.Empty(t, problems)
	require.Equal(t, "o/n", project.Repo)
	require.Equal(t, model.TrackerGithub, project.Tracker)
	require.Equal(t, model.ForgeGithub, project.Forge)
	require.Equal(t, "p", project.BeadsPrefix)
	require.Empty(t, project.Source)
	require.Equal(t, model.ClaimsLocal, project.Claims)
}

func TestLoadFolder_ClaimsAndTrackerNameTheirAdapters(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject+`, tracker = "beads", claims = "beads", beads_prefix = "p"`))

	require.Empty(t, problems)
	require.Equal(t, model.TrackerBeads, project.Tracker)
	require.Equal(t, model.ClaimsBeads, project.Claims)
	require.Equal(t, "p", project.BeadsPrefix)
}

func TestLoadFolder_AForgeOnlyProjectNeedsNoSource(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(`repo = "o/n", forge = "github", `+tail))

	require.Empty(t, problems)
	require.Equal(t, model.ForgeGithub, project.Forge)
	require.Equal(t, model.TrackerLocal, project.Tracker)
}

func TestLoadFolder_ASourceThatIsAUrlOrAPathWithALocalHostIsKept(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"https://example.com/o/n.git", "ssh://git@example.com/o/n.git", "git@example.com:o/n.git"} {
		project, problems := loadProjectText(t, call(`source = "`+source+`", `+tail))
		require.Empty(t, problems, source)
		require.Equal(t, source, project.Source)
	}

	project, problems := loadProjectText(t, call(`source = "https://example.com/o/n.git", `+tail+`, build_host = "gpu", host = `+hostCall("")))
	require.Empty(t, problems)
	require.Equal(t, "gpu", project.BuildHost)
}

func TestLoadFolder_ALeadingTildeOfASourceExpands(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	project, problems := loadProjectText(t, call(`source = "~/work/repo", `+tail))

	require.Empty(t, problems)
	require.Equal(t, filepath.Join(home, "work", "repo"), project.Source)
}

func TestLoadFolder_ASourceWithATildeAndNoHomeFolderIsRefused(t *testing.T) {
	t.Setenv("HOME", "")

	_, problems := loadProjectText(t, call(`source = "~/repo", `+tail))

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "cannot expand `~/repo`")
}

func TestLoadFolder_AScpStyleSourceIsAGitUrlWithOrWithoutAUser(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"git@host:o/n.git", "host:o/n.git", "ssh://host/o/n.git"} {
		project, problems := loadProjectText(t, call(`source = "`+source+`", `+tail+`, build_host = "gpu", host = `+hostCall("")))

		require.Empty(t, problems, source)
		require.Equal(t, source, project.Source)
	}
}

func TestLoadFolder_ARelativePathIsNotAScpStyleSource(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"work/repo", "./repo", "work/a:b", "./a:b"} {
		_, problems := loadProjectText(t, call(`source = "`+source+`", `+tail))

		require.Len(t, problems, 1, source)
		require.ErrorContains(t, problems[0], "`source` must be a git URL or an absolute path; got `"+source+"`")
	}
}

func TestLoadFolder_TheSourceRefusals(t *testing.T) {
	t.Parallel()

	const src = `source = "/srv/repo", `

	tests := []struct {
		name  string
		text  string
		words string
	}{
		{"no_forge_and_no_source", call(tail), "p.star:1:8: the project has no forge and no `source`. Set `source` to a git URL or an absolute path, or set `forge = \"github\"`."},
		{"no_forge_and_no_source_with_local_adapters", call(`tracker = "local", ` + tail), "p.star:1:8: the project has no forge and no `source`"},
		{"relative_source", call(`source = "work/repo", ` + tail), "p.star:1:8: `source` must be a git URL or an absolute path; got `work/repo`. Fix the value."},
		{"dot_source", call(`source = "./repo", ` + tail), "p.star:1:8: `source` must be a git URL or an absolute path; got `./repo`"},
		{"blank_source", call(`source = " ", ` + tail), "p.star:1:8: `source` must be a non-empty string"},
		{
			"path_source_with_a_remote_build_host", call(`tracker = "local", ` + src + tail + `, build_host = "gpu", host = ` + hostCall("")),
			"p.star:1:8: a path `source` needs `build_host = \"local\"`, because the host \"gpu\" cannot read a path on this machine. Use a git URL for `source`.",
		},
		{"tilde_path_source_with_a_remote_build_host", call(`source = "~/repo", ` + tail + `, build_host = "gpu", host = ` + hostCall("")), "p.star:1:8: a path `source` needs `build_host = \"local\"`"},
		{"github_tracker_without_repo", call(`tracker = "github", ` + src + tail), "p.star:1:8: `repo` must be a non-empty string"},
		{"github_forge_without_repo", call(`forge = "github", ` + tail), "p.star:1:8: `repo` must be a non-empty string"},
		{"beads_tracker_without_prefix", call(`tracker = "beads", ` + src + tail), "p.star:1:8: `beads_prefix` must be a non-empty string"},
		{"beads_claims_without_prefix", call(`claims = "beads", ` + src + tail), "p.star:1:8: `beads_prefix` must be a non-empty string"},
		{"unknown_claims", call(`claims = "github", ` + src + tail), "p.star:1:8: `claims`: unknown claims \"github\": use one of local, beads"},
		{"blank_claims", call(`claims = " ", ` + src + tail), "p.star:1:8: `claims` must be a non-empty string"},
		{"repo_given_but_malformed_with_no_github", call(`repo = "justname", ` + src + tail), "p.star:1:8: `repo` must be written owner/name"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, tc.text)

			require.Len(t, problems, 1)
			require.ErrorContains(t, problems[0], tc.words)
		})
	}
}
