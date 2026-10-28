package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

const localProject = "source = \"/srv/repo\"\nbase_branch = \"main\"\ngate = \"make check\"\n"

func TestLoadFolder_AProjectWithOnlySourceBaseBranchAndGateLoadsWithLocalAdapters(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, localProject)

	require.Empty(t, problems)
	require.Equal(t, model.Project{
		Name: "p", Tracker: model.TrackerLocal, Claims: model.ClaimsLocal, Source: "/srv/repo", BaseBranch: "main", Gate: "make check", BuildHost: "local",
	}, project.Project)
}

func TestLoadFolder_AFileWithOnlyTheOldKeysGetsLocalClaims(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, goodProject)

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

	project, problems := loadProjectText(t, localProject+"tracker = \"beads\"\nclaims = \"beads\"\nbeads_prefix = \"p\"\n")

	require.Empty(t, problems)
	require.Equal(t, model.TrackerBeads, project.Tracker)
	require.Equal(t, model.ClaimsBeads, project.Claims)
	require.Equal(t, "p", project.BeadsPrefix)
}

func TestLoadFolder_AForgeOnlyProjectNeedsNoSource(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, "repo = \"o/n\"\nforge = \"github\"\nbase_branch = \"main\"\ngate = \"g\"\n")

	require.Empty(t, problems)
	require.Equal(t, model.ForgeGithub, project.Forge)
	require.Equal(t, model.TrackerLocal, project.Tracker)
}

func TestLoadFolder_ASourceThatIsAUrlOrAPathWithALocalHostIsKept(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"https://example.com/o/n.git", "ssh://git@example.com/o/n.git", "git@example.com:o/n.git"} {
		project, problems := loadProjectText(t, "source = \""+source+"\"\nbase_branch = \"main\"\ngate = \"g\"\n")
		require.Empty(t, problems, source)
		require.Equal(t, source, project.Source)
	}

	project, problems := loadProjectText(t, "source = \"https://example.com/o/n.git\"\nbase_branch = \"main\"\ngate = \"g\"\nbuild_host = \"gpu\"\n"+hostBlock(""))
	require.Empty(t, problems)
	require.Equal(t, "gpu", project.BuildHost)
}

func TestLoadFolder_ALeadingTildeOfASourceExpands(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	project, problems := loadProjectText(t, "source = \"~/work/repo\"\nbase_branch = \"main\"\ngate = \"g\"\n")

	require.Empty(t, problems)
	require.Equal(t, filepath.Join(home, "work", "repo"), project.Source)
}

func TestLoadFolder_ASourceWithATildeAndNoHomeFolderIsRefused(t *testing.T) {
	t.Setenv("HOME", "")

	_, problems := loadProjectText(t, "source = \"~/repo\"\nbase_branch = \"main\"\ngate = \"g\"\n")

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "cannot expand `~/repo`")
}

func TestLoadFolder_AScpStyleSourceIsAGitUrlWithOrWithoutAUser(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"git@host:o/n.git", "host:o/n.git", "ssh://host/o/n.git"} {
		project, problems := loadProjectText(t, "source = \""+source+"\"\nbase_branch = \"main\"\ngate = \"g\"\nbuild_host = \"gpu\"\n"+hostBlock(""))

		require.Empty(t, problems, source)
		require.Equal(t, source, project.Source)
	}
}

func TestLoadFolder_ARelativePathIsNotAScpStyleSource(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"work/repo", "./repo", "work/a:b", "./a:b"} {
		_, problems := loadProjectText(t, "source = \""+source+"\"\nbase_branch = \"main\"\ngate = \"g\"\n")

		require.Len(t, problems, 1, source)
		require.ErrorContains(t, problems[0], "`source` must be a git URL or an absolute path; got `"+source+"`")
	}
}

func TestLoadFolder_TheSourceRefusals(t *testing.T) {
	t.Parallel()

	const tail = "base_branch = \"main\"\ngate = \"g\"\n"

	tests := []struct {
		name  string
		text  string
		words string
	}{
		{"no_forge_and_no_source", tail, "p.hcl:1:1: the project has no forge and no `source`. Set `source` to a git URL or an absolute path, or set `forge = \"github\"`."},
		{"no_forge_and_no_source_with_local_adapters", "tracker = \"local\"\n" + tail, "p.hcl:1:1: the project has no forge and no `source`"},
		{"relative_source", "source = \"work/repo\"\n" + tail, "p.hcl:1:1: `source` must be a git URL or an absolute path; got `work/repo`. Fix the value."},
		{"dot_source", "source = \"./repo\"\n" + tail, "p.hcl:1:1: `source` must be a git URL or an absolute path; got `./repo`"},
		{"blank_source", "source = \" \"\n" + tail, "p.hcl:1:1: `source` must be a non-empty string"},
		{
			"path_source_with_a_remote_build_host", "tracker = \"local\"\nsource = \"/srv/repo\"\n" + tail + "build_host = \"gpu\"\n" + hostBlock(""),
			"p.hcl:2:1: a path `source` needs `build_host = \"local\"`, because the host \"gpu\" cannot read a path on this machine. Use a git URL for `source`.",
		},
		{"tilde_path_source_with_a_remote_build_host", "source = \"~/repo\"\n" + tail + "build_host = \"gpu\"\n" + hostBlock(""), "p.hcl:1:1: a path `source` needs `build_host = \"local\"`"},
		{"github_tracker_without_repo", "tracker = \"github\"\nsource = \"/srv/repo\"\n" + tail, "p.hcl:1:1: `repo` must be a non-empty string"},
		{"github_forge_without_repo", "forge = \"github\"\n" + tail, "p.hcl:1:1: `repo` must be a non-empty string"},
		{"beads_tracker_without_prefix", "tracker = \"beads\"\nsource = \"/srv/repo\"\n" + tail, "p.hcl:1:1: `beads_prefix` must be a non-empty string"},
		{"beads_claims_without_prefix", "claims = \"beads\"\nsource = \"/srv/repo\"\n" + tail, "p.hcl:1:1: `beads_prefix` must be a non-empty string"},
		{"unknown_claims", "claims = \"github\"\nsource = \"/srv/repo\"\n" + tail, "p.hcl:1:1: `claims`: unknown claims \"github\": use one of local, beads"},
		{"blank_claims", "claims = \" \"\nsource = \"/srv/repo\"\n" + tail, "p.hcl:1:1: `claims` must be a non-empty string"},
		{"repo_given_but_malformed_with_no_github", "repo = \"justname\"\nsource = \"/srv/repo\"\n" + tail, "p.hcl:1:1: `repo` must be written owner/name"},
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
