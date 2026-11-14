package config_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
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
		Name: "p", Tracker: "local", Claims: "local", Source: "/srv/repo", Tools: []string{"git"}, BaseBranch: "main", Gate: "make check", TestReport: "r.xml", BuildHost: "local",
	}, project.Project)
}

func TestLoadFolder_AFileWithOnlyTheOldKeysGetsLocalClaims(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject))

	require.Empty(t, problems)
	require.Equal(t, "o/n", project.Repo)
	require.Equal(t, model.TrackerName("github"), project.Tracker)
	require.Equal(t, model.ForgeName("github"), project.Forge)
	require.Equal(t, "p", project.BeadsPrefix)
	require.Empty(t, project.Source)
	require.Equal(t, model.ClaimsName("local"), project.Claims)
}

func TestLoadFolder_ClaimsAndTrackerNameTheirAdapters(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject+`, tracker = "beads", claims = "beads", beads_prefix = "p"`))

	require.Empty(t, problems)
	require.Equal(t, model.TrackerName("beads"), project.Tracker)
	require.Equal(t, model.ClaimsName("beads"), project.Claims)
	require.Equal(t, "p", project.BeadsPrefix)
}

func TestLoadFolder_AForgeOnlyProjectNeedsNoSource(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(`repo = "o/n", forge = "github", `+tail))

	require.Empty(t, problems)
	require.Equal(t, model.ForgeName("github"), project.Forge)
	require.Equal(t, model.TrackerName("local"), project.Tracker)
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
		{"unknown_claims", call(`claims = "github", ` + src + tail), "p.star:1:8: `claims`: unknown claims \"github\": use one of beads, local"},
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

func TestLoadFolder_AProjectWithNoSourceAndNoForgeLoadsWithoutGateTestReportOrBaseBranch(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(``))

	require.Empty(t, problems)
	require.False(t, project.HasRepository())
	require.Empty(t, project.Source)
	require.Empty(t, project.Gate)
	require.Empty(t, project.TestReport)
}

func TestLoadFolder_AProjectWithNoRepositoryThatSetsAGateStillNeedsATestReport(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(`gate = "make check"`))

	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "test_report")

	project, problems := loadProjectText(t, call(`gate = "make check", test_report = "r.xml"`))

	require.Empty(t, problems)
	require.Equal(t, "r.xml", project.TestReport)
}

func TestLoadFolder_AProjectWithASourceStillNeedsBaseBranchGateAndTestReport(t *testing.T) {
	t.Parallel()

	for _, missing := range []string{"gate", "base_branch", "test_report"} {
		args := map[string]string{"source": `source = "/srv/repo"`, "base_branch": `base_branch = "main"`, "gate": `gate = "g"`, "test_report": `test_report = "r.xml"`}
		delete(args, missing)

		_, problems := loadProjectText(t, call(strings.Join(slices.Collect(maps.Values(args)), ", ")))

		require.Len(t, problems, 1, missing)
		require.Contains(t, problems[0].Error(), missing, missing)
	}
}

func TestLoadFolder_AProjectWithAForgeHasARepository(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(`repo = "o/n", forge = "github", `+tail))

	require.Empty(t, problems)
	require.True(t, project.HasRepository())
}

func TestLoadFolder_APinFileNeedsARepository(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(`pin_file = "tools.txt"`))

	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "pin_file")
}
