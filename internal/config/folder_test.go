package config_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
)

const goodProject = "repo = \"o/n\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\ngate = \"make check\"\n"

const goodRunner = "runner {\nname = \"a\"\nagent = \"claude\"\nconfig_dir = \"~/c\"\n}\n"

func readFixture(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // fixture paths are constants in this file
}

func homeDir(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	return home
}

func loadProjectText(t *testing.T, text string) (config.Project, config.Problems) {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "projects", "p.hcl"), text)
	cfg, problems := config.LoadFolder(dir, nil)

	return cfg.Projects["p"], problems
}

func loadRunnersText(t *testing.T, text string) (config.Config, config.Problems) {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.hcl"), text)

	return config.LoadFolder(dir, nil)
}

var exampleConfig = filepath.Join("..", "..", "examples", "config")

func TestLoadFolder_TheExampleConfigLoads(t *testing.T) { //nolint:paralleltest // sets HOME
	home := homeDir(t)

	cfg, problems := config.LoadFolder(exampleConfig, nil)

	require.Empty(t, problems)
	require.Equal(t, []model.Runner{
		{Name: "claude-a", Agent: model.AgentClaude, ConfigDir: filepath.Join(home, ".claude-profiles/a")},
		{Name: "claude-b", Agent: model.AgentClaude, ConfigDir: filepath.Join(home, ".claude-profiles/b")},
	}, cfg.Runners)
	require.Equal(t, config.Pool{FiveHourMax: 90, SevenDayMax: 95}, cfg.Pool)
	require.Equal(t, 2, cfg.Agents.MaxConcurrent)
	require.Len(t, cfg.Projects, 1)
	require.Equal(t, model.Project{Name: "example", Repo: "your-org/your-repo", Tracker: model.TrackerGithub, Forge: model.ForgeGithub, BeadsPrefix: "proj", BaseBranch: "main", Gate: "make check", BuildHost: "local"},
		cfg.Projects["example"].Project)
}

func TestLoadFolder_TheExampleHoldsNoPlaceholderOrOwnerPath(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"runners.hcl", "projects/example.hcl"} {
		text, err := readFixture(filepath.Join(exampleConfig, name))
		require.NoError(t, err)
		require.NotContains(t, string(text), "decided in step")
		require.NotContains(t, string(text), "/Users/")
	}
}

func TestLoadFolder_DefaultsMustPassTheValidator(t *testing.T) {
	t.Parallel()

	require.Empty(t, config.DefaultConfig().Validate())
}

func TestLoadFolder_AFolderWithNoFilesIsTheDefaultConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg, problems := config.LoadFolder(dir, nil)

	require.Empty(t, problems)

	want := config.DefaultConfig()
	want.Dir = dir
	require.Equal(t, want, cfg)
}

func TestLoadFolder_AMissingFolderIsAProblem(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "none")
	_, problems := config.LoadFolder(missing, nil)

	require.Len(t, problems, 1)
	require.ErrorIs(t, problems[0], config.ErrRefused)
	require.ErrorContains(t, problems[0], missing)
	require.ErrorContains(t, problems[0], "OBEE_CONFIG")

	file := writeFile(t, filepath.Join(t.TempDir(), "f"), "")
	_, problems = config.LoadFolder(file, nil)
	require.Len(t, problems, 1)
}

func TestLoadFolder_ProjectsAreNamedByTheirFileInOrder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "projects", "b.hcl"), goodProject)
	writeFile(t, filepath.Join(dir, "projects", "a.hcl"), "repo = \"o/a\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\ngate = \"g\"\n")
	writeFile(t, filepath.Join(dir, "projects", "notes.md"), "not a project")

	cfg, problems := config.LoadFolder(dir, nil)

	require.Empty(t, problems)
	require.Len(t, cfg.Projects, 2)
	require.Equal(t, "o/a", cfg.Projects["a"].Repo)
	require.Equal(t, "o/n", cfg.Projects["b"].Repo)
	require.Equal(t, filepath.Join(dir, "projects", "a.hcl"), cfg.Projects["a"].Path)
}

func TestLoadFolder_BuildHost(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, goodProject)
	require.Empty(t, problems)
	require.Equal(t, "local", project.BuildHost, "a project without a build host builds locally")

	for _, name := range []string{"gpu-a", "A", "a1-b2-c3", "local"} {
		text := goodProject + "build_host = \"" + name + "\"\n"
		if name != "local" {
			text += hostBlock("")
		}

		project, problems = loadProjectText(t, text)
		require.Empty(t, problems, name)
		require.Equal(t, name, project.BuildHost)
	}
}

func TestLoadFolder_TheBuildHostRefusalNamesTheKeyAndBothForms(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, goodProject+"build_host = \"a b\"\n")

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "`build_host`")
	require.ErrorContains(t, problems[0], `"local"`)
	require.ErrorContains(t, problems[0], "SkyPilot cluster name")
	require.ErrorContains(t, problems[0], "`a b`")
	require.ErrorContains(t, problems[0], "p.hcl")
}

func TestLoadFolder_ABadProjectFileIsRefused(t *testing.T) {
	t.Parallel()

	const hostWords = "p.hcl:7:1: `build_host` must be \"local\" or a SkyPilot cluster name"

	tests := []struct {
		name  string
		text  string
		words string
	}{
		{"unknown_key", goodProject + "shade = \"red\"\n", "unknown key `shade`. Remove it"},
		{"unknown_key_gives_the_position", goodProject + "shade = \"red\"\n", "p.hcl:7:1: unknown key `shade`"},
		{"the_steps_block_is_gone", goodProject + "steps \"gate\" {\ntimeout = \"2h\"\n}\n", "p.hcl:7:1: unknown block `steps`. Remove it."},
		{"missing_repo", "base_branch = \"main\"\ngate = \"g\"\n", "p.hcl:1:1: `repo` must be a non-empty string"},
		{"blank_repo", "repo = \" \"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\ngate = \"g\"\n", "p.hcl:1:1: `repo` must be a non-empty string"},
		{"repo_of_the_wrong_type", "repo = [\"o/n\"]\nbase_branch = \"main\"\ngate = \"g\"\n", "p.hcl:1:"},
		{"repo_of_the_wrong_type_names_the_key", "repo = [\"o/n\"]\nbase_branch = \"main\"\ngate = \"g\"\n", "`repo` has the wrong type"},
		{"repo_without_owner", "repo = \"justname\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"m\"\ngate = \"g\"\n", "p.hcl:1:1: `repo` must be written owner/name"},
		{"repo_with_three_parts", "repo = \"a/b/c\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"m\"\ngate = \"g\"\n", "p.hcl:1:1: `repo` must be written owner/name"},
		{"repo_without_owner_text", "repo = \"/n\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"m\"\ngate = \"g\"\n", "p.hcl:1:1: `repo` must be written owner/name"},
		{"repo_without_name", "repo = \"o/\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"m\"\ngate = \"g\"\n", "p.hcl:1:1: `repo` must be written owner/name"},
		{"missing_tracker", "repo = \"o/n\"\nforge = \"github\"\nbase_branch = \"m\"\ngate = \"g\"\n", "p.hcl:1:1: `tracker` must be a non-empty string"},
		{"missing_forge", "repo = \"o/n\"\ntracker = \"github\"\nbase_branch = \"m\"\ngate = \"g\"\n", "p.hcl:1:1: `forge` must be a non-empty string"},
		{"unknown_tracker", "repo = \"o/n\"\ntracker = \"jira\"\nforge = \"github\"\nbase_branch = \"m\"\ngate = \"g\"\n", "p.hcl:2:1: `tracker`: unknown tracker \"jira\": use one of beads, github"},
		{"unknown_forge", "repo = \"o/n\"\ntracker = \"github\"\nforge = \"gitlab\"\nbase_branch = \"m\"\ngate = \"g\"\n", "p.hcl:3:1: `forge`: unknown forge \"gitlab\": use one of github"},
		{"beads_without_a_prefix", "repo = \"o/n\"\ntracker = \"beads\"\nforge = \"github\"\nbase_branch = \"m\"\ngate = \"g\"\n", "p.hcl:1:1: `beads_prefix` must be a non-empty string"},
		{"github_without_a_prefix", "repo = \"o/n\"\ntracker = \"github\"\nforge = \"github\"\nbase_branch = \"m\"\ngate = \"g\"\n", "p.hcl:1:1: `beads_prefix` must be a non-empty string"},
		{"missing_gate", "repo = \"o/n\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\n", "p.hcl:1:1: `gate` must be"},
		{"missing_base_branch", "repo = \"o/n\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\ngate = \"g\"\n", "p.hcl:1:1: `base_branch` must be"},
		{"blank_tracker", strings.Replace(goodProject, `tracker = "github"`, `tracker = " "`, 1), "p.hcl:2:1: `tracker` must be a non-empty string"},
		{"blank_forge", strings.Replace(goodProject, `forge = "github"`, `forge = " "`, 1), "p.hcl:3:1: `forge` must be a non-empty string"},
		{"blank_beads_prefix", strings.Replace(goodProject, `beads_prefix = "p"`, `beads_prefix = " "`, 1), "p.hcl:4:1: `beads_prefix` must be a non-empty string"},
		{"blank_base_branch", strings.Replace(goodProject, `base_branch = "main"`, `base_branch = " "`, 1), "p.hcl:5:1: `base_branch` must be a non-empty string"},
		{"blank_gate", strings.Replace(goodProject, `gate = "make check"`, `gate = " "`, 1), "p.hcl:6:1: `gate` must be a non-empty string"},
		{"empty_build_host", goodProject + "build_host = \"\"\n", "p.hcl:7:1: `build_host` must be"},
		{"build_host_starts_with_a_digit", goodProject + "build_host = \"1gpu\"\n", hostWords},
		{"build_host_with_underscore", goodProject + "build_host = \"gpu_a\"\n", hostWords},
		{"build_host_with_space", goodProject + "build_host = \"gpu a\"\n", hostWords},
		{"build_host_with_shell_text", goodProject + "build_host = \"gpu;id\"\n", hostWords},
		{"build_host_with_newline", goodProject + "build_host = \"gpu\\n\"\n", hostWords},
		{"build_host_starts_with_hyphen", goodProject + "build_host = \"-gpu\"\n", hostWords},
		{"bad_syntax", goodProject + "{", "Fix the syntax of the file"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, tc.text)

			require.Len(t, problems, 1)
			require.ErrorIs(t, problems[0], config.ErrRefused)
			require.ErrorContains(t, problems[0], tc.words)
			require.ErrorContains(t, problems[0], "p.hcl")
		})
	}
}

func TestLoadFolder_ABeadsProjectNeedsItsPrefixAndKeepsIt(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, "repo = \"o/n\"\ntracker = \"beads\"\nforge = \"github\"\nbeads_prefix = \"proj\"\nbase_branch = \"main\"\ngate = \"g\"\n")

	require.Empty(t, problems)
	require.Equal(t, model.TrackerBeads, project.Tracker)
	require.Equal(t, "proj", project.BeadsPrefix)
}

func TestLoadFolder_AProjectFileWithWindowsLineEndingsLoads(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, "repo = \"o/n\"\r\ntracker = \"github\"\r\nforge = \"github\"\r\nbeads_prefix = \"p\"\r\nbase_branch = \"main\"\r\ngate = \"make check\"\r\n")

	require.Empty(t, problems)
	require.Equal(t, "make check", project.Gate)
}

func TestLoadFolder_AProjectFileWithALoneCarriageReturnIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "repo = \"o/n\"\rtracker = \"github\"\rforge = \"github\"\rbeads_prefix = \"p\"\rbase_branch = \"main\"\rgate = \"g\"\r")

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "Fix the syntax of the file")
}

func TestLoadFolder_AProjectPathThatIsNotAFileIsReported(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects", "d.hcl"), 0o700))

	_, problems := config.LoadFolder(dir, nil)

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "cannot read")
}

func TestLoadFolder_EveryBadFileIsReportedOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.hcl"), "bogus = 1\n")
	writeFile(t, filepath.Join(dir, "projects", "a.hcl"), "x = 1\n")
	writeFile(t, filepath.Join(dir, "projects", "b.hcl"), "y = 1\n")

	_, problems := config.LoadFolder(dir, nil)

	require.Len(t, problems, 3)
	require.Contains(t, problems.Error(), "runners.hcl")
	require.Equal(t, 2, strings.Count(problems.Error(), "\n"))
}

func TestLoadFolder_Runners(t *testing.T) { //nolint:paralleltest // sets HOME
	home := homeDir(t)

	cfg, problems := loadRunnersText(t, goodRunner+"runner {\nname = \"b\"\nagent = \"opencode\"\nkeychain = \"svc\"\n}\n")

	require.Empty(t, problems)
	require.Equal(t, []model.Runner{
		{Name: "a", Agent: model.AgentClaude, ConfigDir: filepath.Join(home, "c")},
		{Name: "b", Agent: model.AgentOpenCode, Keychain: "svc"},
	}, cfg.Runners)
}

func TestLoadFolder_ARunnersFileWithNoRunnerIsEmpty(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "# none\n")

	require.Empty(t, problems)
	require.Empty(t, cfg.Runners)
}

func TestLoadFolder_ABadRunnersFileIsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		words string
	}{
		{"duplicate_name", goodRunner + goodRunner, "runners.hcl:6:1: runner `a` appears twice"},
		{"unknown_agent", "runner {\nname = \"a\"\nagent = \"gpt\"\n}\n", "runners.hcl:3:1: runner `a`: unknown agent \"gpt\": use one of claude, opencode"},
		{"unknown_key", "runner {\nname = \"a\"\nagent = \"claude\"\ntoken = \"x\"\n}\n", "runners.hcl:4:1: unknown key `token`. Remove it"},
		{"missing_name", "runner {\nagent = \"claude\"\n}\n", "runners.hcl:1:1: `name` must be"},
		{"missing_agent", "runner {\nname = \"a\"\n}\n", "runners.hcl:1:1: `agent` must be"},
		{"blank_name", "runner {\nname = \" \"\nagent = \"claude\"\n}\n", "runners.hcl:2:1: `name` must be"},
		{"blank_agent", "runner {\nname = \"a\"\nagent = \" \"\n}\n", "runners.hcl:3:1: `agent` must be"},
		{"config_dir_of_the_wrong_type", "runner {\nname = \"a\"\nagent = \"claude\"\nconfig_dir = [\"x\"]\n}\n", "runners.hcl:4:"},
		{"config_dir_of_the_wrong_type_names_the_key", "runner {\nname = \"a\"\nagent = \"claude\"\nconfig_dir = [\"x\"]\n}\n", "`config_dir` has the wrong type"},
		{"empty_config_dir", "runner {\nname = \"a\"\nagent = \"claude\"\nconfig_dir = \"\"\n}\n", "runners.hcl:4:1: `config_dir` must be"},
		{"runner_is_not_a_block", "runner = 3\n", "`runner` is a block, not a key"},
		{"keychain_of_the_wrong_type", "runner {\nname = \"a\"\nagent = \"claude\"\nkeychain = [\"x\"]\n}\n", "`keychain` has the wrong type"},
		{"empty_keychain", "runner {\nname = \"a\"\nagent = \"claude\"\nkeychain = \"\"\n}\n", "runners.hcl:4:1: `keychain` must be a non-empty string"},
		{"repeated_runner_key", "runner {\nname = \"a\"\nname = \"b\"\nagent = \"claude\"\n}\n", "runners.hcl:3:1: `name` is set twice"},
		{"unknown_top_level_key", "shade = 1\n", "runners.hcl:1:1: unknown key `shade`"},
		{"bad_syntax", "runner {\n", "Fix the syntax of the file"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadRunnersText(t, tc.text)

			require.Len(t, problems, 1)
			require.ErrorIs(t, problems[0], config.ErrRefused)
			require.ErrorContains(t, problems[0], tc.words)
			require.ErrorContains(t, problems[0], "runners.hcl")
		})
	}
}

func TestLoadFolder_ARunnerConfigDirExpandsTheHomeFolderOnlyAtTheStart(t *testing.T) { //nolint:paralleltest // sets HOME
	home := homeDir(t)

	cfg, problems := loadRunnersText(t, "runner {\nname = \"a\"\nagent = \"claude\"\nconfig_dir = \"~\"\n}\n"+
		"runner {\nname = \"b\"\nagent = \"claude\"\nconfig_dir = \"/x/~/y\"\n}\n"+
		"runner {\nname = \"c\"\nagent = \"claude\"\nconfig_dir = \"~other/y\"\n}\n")

	require.Empty(t, problems)
	require.Equal(t, []string{home, "/x/~/y", "~other/y"},
		[]string{cfg.Runners[0].ConfigDir, cfg.Runners[1].ConfigDir, cfg.Runners[2].ConfigDir})
}

func TestLoadFolder_ARunnerConfigDirNeedsAHomeFolderForTilde(t *testing.T) {
	t.Setenv("HOME", "")

	_, problems := loadRunnersText(t, goodRunner)

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "Set HOME")
}

func TestLoadFolder_Pool(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "pool {\nfive_hour_max = 50\nseven_day_max = 100\n}\n")
	require.Empty(t, problems)
	require.Equal(t, config.Pool{FiveHourMax: 50, SevenDayMax: 100}, cfg.Pool)

	cfg, problems = loadRunnersText(t, "pool {\nfive_hour_max = 12.5\n}\n")
	require.Empty(t, problems)
	require.Equal(t, config.Pool{FiveHourMax: 12.5, SevenDayMax: 95}, cfg.Pool, "a missing key keeps its default")

	cfg, problems = loadRunnersText(t, "pool {\nseven_day_max = 10\n}\n")
	require.Empty(t, problems)
	require.Equal(t, config.Pool{FiveHourMax: 90, SevenDayMax: 10}, cfg.Pool)
}

func TestLoadFolder_ABadPoolIsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		words string
	}{
		{"zero", "pool {\nfive_hour_max = 0\n}\n", "`five_hour_max` in the pool block must be a number above 0 and up to 100"},
		{"negative", "pool {\nseven_day_max = -1\n}\n", "`seven_day_max` in the pool block"},
		{"over_one_hundred", "pool {\nfive_hour_max = 100.5\n}\n", "`five_hour_max` in the pool block"},
		{"a_string", "pool {\nfive_hour_max = \"high\"\n}\n", "`five_hour_max` has the wrong type"},
		{"a_boolean", "pool {\nfive_hour_max = true\n}\n", "runners.hcl:2:17: `five_hour_max` has the wrong type"},
		{"unknown_key", "pool {\nthree_hour_max = 5\n}\n", "runners.hcl:2:1: unknown key `three_hour_max`"},
		{"repeated_pool_block", "pool {\n}\npool {\n}\n", "block `pool` appears twice"},
		{"not_a_block", "pool = 3\n", "`pool` is a block, not a key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadRunnersText(t, tc.text)

			require.Len(t, problems, 1)
			require.ErrorContains(t, problems[0], tc.words)
		})
	}
}

func TestLoadFolder_PoolCapOfExactlyOneHundredPasses(t *testing.T) {
	t.Parallel()

	_, problems := loadRunnersText(t, "pool {\nfive_hour_max = 100\nseven_day_max = 0.1\n}\n")

	require.Empty(t, problems)
}

func TestLoadFolder_AgentsMaxConcurrent(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "agents {\nmax_concurrent = 6\n}\n")
	require.Empty(t, problems)
	require.Equal(t, 6, cfg.Agents.MaxConcurrent)

	cfg, problems = loadRunnersText(t, "agents {\n}\n")
	require.Empty(t, problems)
	require.Equal(t, 2, cfg.Agents.MaxConcurrent, "a block without the key keeps the default")

	_, problems = loadRunnersText(t, "agents {\nmax_concurrent = 0\n}\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.max_concurrent must be 1 or more")
	require.ErrorContains(t, problems[0], "runners.hcl")
	require.ErrorContains(t, problems[0], "OBEE_AGENTS_MAX_CONCURRENT")

	_, problems = loadRunnersText(t, "agents {\nmax_concurrent = 1\n}\n")
	require.Empty(t, problems)

	_, problems = loadRunnersText(t, "agents {\nmax_threads = 1\n}\n")
	require.ErrorContains(t, problems[0], "runners.hcl:2:1: unknown key `max_threads`")
	_, problems = loadRunnersText(t, "agents {\nmax_concurrent = \"many\"\n}\n")
	require.ErrorContains(t, problems[0], "runners.hcl:2:19: `max_concurrent` has the wrong type")

	_, problems = loadRunnersText(t, "agents {\nmax_concurrent = 1\nmax_concurrent = 2\n}\n")
	require.ErrorContains(t, problems[0], "runners.hcl:3:1: `max_concurrent` is set twice")
}

func TestLoadFolder_EnvironmentOverlay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.hcl"), "agents {\nmax_concurrent = 2\n}\npool {\nfive_hour_max = 50\n}\n")

	cfg, problems := config.LoadFolder(dir, map[string]string{
		"OBEE_AGENTS_MAX_CONCURRENT": "6", "OBEE_POOL_FIVE_HOUR_MAX": "70", "OBEE_POOL_SEVEN_DAY_MAX": "80.5", "REPO": "ignored",
	})
	require.Empty(t, problems)
	require.Equal(t, 6, cfg.Agents.MaxConcurrent)
	require.Equal(t, config.Pool{FiveHourMax: 70, SevenDayMax: 80.5}, cfg.Pool)

	cfg, problems = config.LoadFolder(dir, map[string]string{"OBEE_AGENTS_MAX_CONCURRENT": ""})
	require.Empty(t, problems)
	require.Equal(t, 2, cfg.Agents.MaxConcurrent, "an empty variable leaves the file value")
}

func TestLoadFolder_AnOverlayValueIsValidated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		env   map[string]string
		words string
	}{
		{"text_for_max_concurrent", map[string]string{"OBEE_AGENTS_MAX_CONCURRENT": "many"}, "OBEE_AGENTS_MAX_CONCURRENT=many is not a whole number"},
		{"zero_for_max_concurrent", map[string]string{"OBEE_AGENTS_MAX_CONCURRENT": "0"}, "agents.max_concurrent must be 1 or more"},
		{"text_for_five_hour_cap", map[string]string{"OBEE_POOL_FIVE_HOUR_MAX": "lots"}, "OBEE_POOL_FIVE_HOUR_MAX=lots is not a number"},
		{"text_for_seven_day_cap", map[string]string{"OBEE_POOL_SEVEN_DAY_MAX": "lots"}, "OBEE_POOL_SEVEN_DAY_MAX=lots is not a number"},
		{"cap_over_one_hundred", map[string]string{"OBEE_POOL_SEVEN_DAY_MAX": "101"}, "`seven_day_max` in the pool block"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := config.LoadFolder(t.TempDir(), tc.env)

			require.NotEmpty(t, problems)
			require.ErrorContains(t, problems, tc.words)
		})
	}
}

func TestLoadFolder_AProjectsPathThatIsAFileIsReported(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "projects"), "not a folder")

	_, problems := config.LoadFolder(dir, nil)

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "cannot read "+filepath.Join(dir, "projects"))
}

func TestLoadFolder_ARefusalKeepsItsCause(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(dir, "missing.hcl"), filepath.Join(dir, "projects", "p.hcl")))

	_, problems := config.LoadFolder(dir, nil)

	require.Len(t, problems, 1)
	require.ErrorIs(t, problems[0], config.ErrRefused)
	require.ErrorIs(t, problems[0], fs.ErrNotExist)

	file := writeFile(t, filepath.Join(dir, "projects", "f"), "x")
	_, problems = config.LoadFolder(file, nil)
	require.Len(t, problems, 1)

	_, problems = config.LoadFolder(filepath.Join(file, "sub"), nil)
	require.Len(t, problems, 1)
}

func TestLoadFolder_ARepeatedKeyIsReportedAsSuch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		words string
	}{
		{"repeated_key", goodProject + "repo = \"o/m\"\n", "p.hcl:7:1: `repo` is set twice. Keep one."},
		{"value_then_block", goodProject + "repo {\nx = 1\n}\n", "`repo` is a key, not a block"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, tc.text)

			require.Len(t, problems, 1)
			require.ErrorContains(t, problems[0], tc.words)
			require.NotContains(t, problems[0].Error(), "wrong type")
		})
	}
}

func TestLoadFolder_ANotANumberPoolCapIsRefused(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"OBEE_POOL_FIVE_HOUR_MAX", "OBEE_POOL_SEVEN_DAY_MAX"} {
		_, problems := config.LoadFolder(t.TempDir(), map[string]string{name: "NaN"})

		require.Len(t, problems, 1, name)
		require.ErrorContains(t, problems[0], "must be a number above 0 and up to 100")
	}
}

func TestLoadFolder_ABadPoolCapNamesTheFileAndTheVariable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, problems := config.LoadFolder(dir, map[string]string{"OBEE_POOL_FIVE_HOUR_MAX": "101", "OBEE_POOL_SEVEN_DAY_MAX": "0"})

	require.Len(t, problems, 2)

	text := problems.Error()
	require.Contains(t, text, "Set it in "+filepath.Join(dir, "runners.hcl")+" or in OBEE_POOL_FIVE_HOUR_MAX.")
	require.Contains(t, text, "Set it in "+filepath.Join(dir, "runners.hcl")+" or in OBEE_POOL_SEVEN_DAY_MAX.")
}
