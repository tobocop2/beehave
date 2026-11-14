package config_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// goodProject is the argument list of a project call that loads. A test adds arguments after it.
// unreportedProject has no test_report, which the loader refuses for a gate on this machine and for a gate on a build host.
const unreportedProject = `repo = "o/n", tracker = "github", forge = "github", base_branch = "main", gate = "make check"`

const hostProject = unreportedProject + `, test_report = "reports/*.xml"`

const goodProject = hostProject

const goodRunner = "runner(name = \"a\", agent = \"claude\", config_dir = \"~/c\")\n"

// call is a project file with one project call and the given arguments.
func call(args string) string { return "project(" + args + ")\n" }

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
	writeFile(t, filepath.Join(dir, "projects", "p.star"), text)
	cfg, problems := loadFolder(dir, nil)

	return cfg.Projects["p"], problems
}

func loadRunnersText(t *testing.T, text string) (config.Config, config.Problems) {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), text)

	return loadFolder(dir, nil)
}

var exampleConfig = filepath.Join("..", "..", "examples", "config")

func TestLoadFolder_TheExampleConfigLoads(t *testing.T) { //nolint:paralleltest // sets HOME
	home := homeDir(t)

	cfg, problems := loadFolder(exampleConfig, nil)

	require.Empty(t, problems)
	require.Equal(t, []model.Runner{
		{Name: "claude-a", Agent: "claude", ConfigDir: filepath.Join(home, ".claude-profiles/a")},
		{Name: "claude-b", Agent: "claude", ConfigDir: filepath.Join(home, ".claude-profiles/b")},
	}, cfg.Runners)
	require.Equal(t, config.Pool{FiveHourMax: 90, SevenDayMax: 95}, cfg.Pool)
	require.Equal(t, 2, cfg.Agents.MaxConcurrent)
	require.Len(t, cfg.Projects, 1)
	require.Equal(t, model.Project{Name: "example", Repo: "your-org/your-repo", Tracker: "github", Claims: "beads", Forge: "github", Blocks: map[string]*model.Block{"beads": model.NewBlock(filepath.Join(exampleConfig, "projects", "example.star")+":12:18", []model.BlockEntry{model.TextEntry("prefix", "proj")})}, Tools: []string{"git", "bd", "gh"}, BaseBranch: "main", Gate: "make check", TestReport: "reports/*.xml", BuildHost: "local"},
		cfg.Projects["example"].Project)
}

func TestLoadFolder_TheLocalExampleProjectLoads(t *testing.T) { //nolint:paralleltest // sets HOME
	home := homeDir(t)

	text, err := readFixture(filepath.Join("..", "..", "examples", "local", "project.star"))
	require.NoError(t, err)

	project, problems := loadProjectText(t, string(text))

	require.Empty(t, problems)
	require.Equal(t, model.Project{
		Name: "p", Tracker: "local", Claims: "local", Source: filepath.Join(home, "work/your-repo"), Tools: []string{"git"}, BaseBranch: "main", Gate: "make check", TestReport: "reports/*.xml", BuildHost: "local",
	}, project.Project)
}

func TestLoadFolder_TheExampleHoldsNoPlaceholderOrOwnerPath(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"runners.star", "projects/example.star"} {
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
	cfg, problems := loadFolder(dir, nil)

	require.Empty(t, problems)

	want := config.DefaultConfig()
	want.Dir = dir
	require.Equal(t, want, cfg)
}

func TestLoadFolder_AMissingFolderIsAProblem(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "none")
	_, problems := loadFolder(missing, nil)

	require.Len(t, problems, 1)
	require.ErrorIs(t, problems[0], config.ErrRefused)
	require.ErrorContains(t, problems[0], missing)
	require.ErrorContains(t, problems[0], "BEEHAVE_CONFIG")

	file := writeFile(t, filepath.Join(t.TempDir(), "f"), "")
	_, problems = loadFolder(file, nil)
	require.Len(t, problems, 1)
}

func TestLoadFolder_ProjectsAreNamedByTheirFileInOrder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "projects", "b.star"), call(goodProject))
	writeFile(t, filepath.Join(dir, "projects", "a.star"), call(`repo = "o/a", tracker = "github", forge = "github", base_branch = "main", gate = "g", test_report = "r.xml"`))
	writeFile(t, filepath.Join(dir, "projects", "notes.md"), "not a project")

	cfg, problems := loadFolder(dir, nil)

	require.Empty(t, problems)
	require.Len(t, cfg.Projects, 2)
	require.Equal(t, "o/a", cfg.Projects["a"].Repo)
	require.Equal(t, "o/n", cfg.Projects["b"].Repo)
	require.Equal(t, filepath.Join(dir, "projects", "a.star"), cfg.Projects["a"].Path)
}

func TestLoadFolder_BuildHost(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject))
	require.Empty(t, problems)
	require.Equal(t, "local", project.BuildHost, "a project without a build host builds locally")

	for _, name := range []string{"gpu-a", "A", "a1-b2-c3", "local"} {
		args := hostProject + `, build_host = "` + name + `"`
		if name != "local" {
			args += ", host = " + hostCall("")
		}

		project, problems = loadProjectText(t, call(args))
		require.Empty(t, problems, name)
		require.Equal(t, name, project.BuildHost)
	}
}

func TestLoadFolder_BuildHostAcceptsAnSSHAliasWithDots(t *testing.T) {
	t.Parallel()

	const alias = "beehave-rig.us-central1-a.lance-bench-8389"

	project, problems := loadProjectText(t, call(hostProject+`, build_host = "`+alias+`", host = `+hostCall("")))
	require.Empty(t, problems)
	require.Equal(t, alias, project.BuildHost)

	project, problems = loadProjectText(t, call(hostProject+`, build_host = ["`+alias+`", "gpu-b"], host = `+hostCall("")))
	require.Empty(t, problems)
	require.Equal(t, []string{alias, "gpu-b"}, project.BuildHosts)
}

func TestLoadFolder_TheBuildHostRefusalNamesTheKeyAndBothForms(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(hostProject+`, build_host = "a b"`))

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "`build_host`")
	require.ErrorContains(t, problems[0], `"local"`)
	require.ErrorContains(t, problems[0], "the name of a build host")
	require.ErrorContains(t, problems[0], "`a b`")
	require.ErrorContains(t, problems[0], "p.star")
}

func TestLoadFolder_ABadProjectFileIsRefused(t *testing.T) {
	t.Parallel()

	const (
		hostWords = "p.star:1:8: `build_host` must be \"local\" or the name of a build host"
		local     = `base_branch = "m", gate = "g"`
		github    = `tracker = "github", forge = "github", ` + local
	)

	tests := []struct {
		name  string
		text  string
		words string
	}{
		{"unknown_argument", call(goodProject + `, shade = "red"`), `project: unexpected keyword argument "shade"`},
		{"unknown_argument_gives_the_position", call(goodProject + `, shade = "red"`), "p.star:1:8: project: unexpected keyword argument"},
		{"positional_argument", call(`"o/n"`), "p.star:1:8: `project` takes keyword arguments only"},
		{"missing_repo", call(`tracker = "github", base_branch = "main", gate = "g", source = "/s"`), "p.star:1:8: `repo` must be a non-empty string"},
		{"blank_repo", call(`repo = " ", ` + github), "p.star:1:8: `repo` must be a non-empty string"},
		{"repo_of_the_wrong_type", call(`repo = ["o/n"], base_branch = "main", gate = "g"`), `for parameter "repo": got list, want string`},
		{"repo_without_owner", call(`repo = "justname", ` + github), "p.star:1:8: `repo` must be written owner/name"},
		{"repo_with_three_parts", call(`repo = "a/b/c", ` + github), "p.star:1:8: `repo` must be written owner/name"},
		{"repo_without_owner_text", call(`repo = "/n", ` + github), "p.star:1:8: `repo` must be written owner/name"},
		{"repo_without_name", call(`repo = "o/", ` + github), "p.star:1:8: `repo` must be written owner/name"},
		{"unknown_tracker", call(`repo = "o/n", tracker = "jira", forge = "github", ` + local), "p.star:1:8: `tracker`: unknown tracker \"jira\": use one of beads, github, local"},
		{"unknown_forge", call(`repo = "o/n", tracker = "github", forge = "gitlab", ` + local), "p.star:1:8: `forge`: unknown forge \"gitlab\": use one of github"},
		{"beads_without_a_block", call(`repo = "o/n", tracker = "beads", forge = "github", ` + local), "p.star:1:8: the tracker `beads` needs the `beads` block. Write beads(prefix = ...)."},
		{"beads_claims_without_a_block", call(`repo = "o/n", claims = "beads", ` + github), "p.star:1:8: the claims `beads` needs the `beads` block. Write beads(prefix = ...)."},
		{"missing_gate", call(`repo = "o/n", tracker = "github", forge = "github", base_branch = "main"`), "`gate` must be a non-empty string"},
		{"missing_base_branch", call(`repo = "o/n", tracker = "github", forge = "github", gate = "g"`), "`base_branch` must be a non-empty string"},
		{"blank_tracker", call(strings.Replace(goodProject, `tracker = "github"`, `tracker = " "`, 1)), "p.star:1:8: `tracker` must be a non-empty string"},
		{"blank_forge", call(strings.Replace(goodProject, `forge = "github"`, `forge = " "`, 1)), "p.star:1:8: `forge` must be a non-empty string"},
		{"blank_base_branch", call(strings.Replace(goodProject, `base_branch = "main"`, `base_branch = " "`, 1)), "p.star:1:8: `base_branch` must be a non-empty string"},
		{"blank_gate", call(strings.Replace(goodProject, `gate = "make check"`, `gate = " "`, 1)), "p.star:1:8: `gate` must be a non-empty string"},
		{"empty_build_host", call(hostProject + `, build_host = ""`), "p.star:1:8: `build_host` must be"},
		{"build_host_starts_with_a_digit", call(hostProject + `, build_host = "1gpu"`), hostWords},
		{"build_host_with_underscore", call(hostProject + `, build_host = "gpu_a"`), hostWords},
		{"build_host_starts_with_a_dot", call(hostProject + `, build_host = ".gpu"`), hostWords},
		{"build_host_with_space", call(hostProject + `, build_host = "gpu a"`), hostWords},
		{"build_host_with_shell_text", call(hostProject + `, build_host = "gpu;id"`), hostWords},
		{"build_host_with_newline", call(hostProject + `, build_host = "gpu\n"`), hostWords},
		{"build_host_starts_with_hyphen", call(hostProject + `, build_host = "-gpu"`), hostWords},
		{"bad_syntax", call(goodProject) + "{", "Fix the syntax of the file"},
		{"undefined_name", "projet()\n", "p.star:1:1: undefined: projet"},
		{"no_project_call", "# none\n", "the file does not call `project`"},
		{"second_project_call", call(goodProject) + call(goodProject), "p.star:2:8: block `project` appears twice. Keep one."},
		{"a_loop_is_refused", "for x in [1]:\n  pass\n", "Fix the syntax of the file"},
		{"a_load_is_refused", "load(\"x.star\", \"y\")\n", "load not implemented by this application"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, tc.text)

			require.Len(t, problems, 1)
			require.ErrorIs(t, problems[0], config.ErrRefused)
			require.ErrorContains(t, problems[0], tc.words)
			require.ErrorContains(t, problems[0], "p.star")
		})
	}
}

func TestLoadFolder_ABeadsProjectNeedsItsBlockAndKeepsIt(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(`repo = "o/n", tracker = "beads", forge = "github", beads = beads(prefix = "proj"), base_branch = "main", gate = "g", test_report = "r.xml"`))

	require.Empty(t, problems)
	require.Equal(t, model.TrackerName("beads"), project.Tracker)

	prefix, given := project.Blocks["beads"].Text("prefix")
	require.True(t, given)
	require.Equal(t, "proj", prefix)
}

func TestLoadFolder_AProjectFileWithWindowsLineEndingsLoads(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, "project(\r\n  repo = \"o/n\",\r\n  tracker = \"github\", forge = \"github\",\r\n  base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\",\r\n)\r\n")

	require.Empty(t, problems)
	require.Equal(t, "make check", project.Gate)
}

func TestLoadFolder_TheTestReportKeyIsReadAndAnEmptyOneIsRefused(t *testing.T) {
	t.Parallel()

	base := `repo = "o/n", tracker = "beads", forge = "github", beads = beads(prefix = "proj"), base_branch = "main", gate = "g"`

	project, problems := loadProjectText(t, "project("+base+", test_report = \"out/*.xml\")\n")
	require.Empty(t, problems)
	require.Equal(t, "out/*.xml", project.TestReport)

	_, problems = loadProjectText(t, "project("+base+", test_report = \"\")\n")
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "`test_report` must be a non-empty string")
}

func TestLoadFolder_ATestReportOnABuildHostIsAccepted(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(hostProject+`, build_host = "gpu-a", host = `+hostCall("")))

	require.Empty(t, problems)
	require.Equal(t, "reports/*.xml", project.TestReport)
}

func TestLoadFolder_AGateThatSetsNoTestReportIsRefusedOnThisMachineAndOnABuildHost(t *testing.T) {
	t.Parallel()

	for _, args := range []string{unreportedProject, unreportedProject + `, build_host = "local"`, unreportedProject + `, build_host = "gpu-a", host = ` + hostCall("")} {
		_, problems := loadProjectText(t, call(args))

		require.Len(t, problems, 1, args)
		require.Contains(t, problems[0].Error(), "p.star:1:8:")
		require.Contains(t, problems[0].Error(), "`test_report` is not set")
		require.Contains(t, problems[0].Error(), "no test count")
	}
}

func TestLoadFolder_ThePinFileKeyIsReadAnEmptyOneIsRefusedAndABuildHostIsRefused(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, pin_file = ".tool-versions"`))
	require.Empty(t, problems)
	require.Equal(t, ".tool-versions", project.PinFile)

	project, problems = loadProjectText(t, call(goodProject))
	require.Empty(t, problems)
	require.Empty(t, project.PinFile)

	_, problems = loadProjectText(t, call(goodProject+`, pin_file = ""`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "`pin_file` must be a non-empty string")

	project, problems = loadProjectText(t, call(goodProject+`, build_host = "gpu-a", host = `+hostCall("")+`, pin_file = ".tool-versions"`))
	require.Empty(t, problems, "a gate on a build host checks the pins on the host")
	require.Equal(t, ".tool-versions", project.PinFile)
}

func TestLoadFolder_APinFileThatLeavesTheWorkTreeIsRefused(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/etc/tool-versions", "../.tool-versions", "a/../../b", ".."} {
		_, problems := loadProjectText(t, call(goodProject+`, pin_file = "`+path+`"`))

		require.Len(t, problems, 1, path)
		require.Contains(t, problems[0].Error(), "`pin_file`", path)
		require.Contains(t, problems[0].Error(), "inside the work tree", path)
	}

	project, problems := loadProjectText(t, call(goodProject+`, pin_file = "tools/.tool-versions"`))
	require.Empty(t, problems)
	require.Equal(t, "tools/.tool-versions", project.PinFile)
}

func TestLoadFolder_AChangelogIsAFactAndMustBeAPathInsideTheWorkTree(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, changelog = "docs/CHANGELOG.md"`))
	require.Empty(t, problems)
	require.Equal(t, "docs/CHANGELOG.md", project.Changelog)

	project, problems = loadProjectText(t, call(goodProject))
	require.Empty(t, problems)
	require.Empty(t, project.Changelog)

	for _, path := range []string{"", "/etc/CHANGELOG", "../CHANGELOG.md", "./CHANGELOG.md", "docs/"} {
		_, problems = loadProjectText(t, call(goodProject+`, changelog = "`+path+`"`))
		require.Len(t, problems, 1, path)
		require.Contains(t, problems[0].Error(), "`changelog`", path)
	}
}

func TestLoadFolder_AProjectPathThatIsNotAFileIsReported(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects", "d.star"), 0o700))

	_, problems := loadFolder(dir, nil)

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "cannot read")
}

func TestLoadFolder_EveryBadFileIsReportedOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), "bogus()\n")
	writeFile(t, filepath.Join(dir, "projects", "a.star"), "x()\n")
	writeFile(t, filepath.Join(dir, "projects", "b.star"), "y()\n")

	_, problems := loadFolder(dir, nil)

	require.Len(t, problems, 3)
	require.Contains(t, problems.Error(), "runners.star")
	require.Equal(t, 2, strings.Count(problems.Error(), "\n"))
}

func TestLoadFolder_AnOldHCLFileIsRefusedByName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	old := writeFile(t, filepath.Join(dir, "runners.hcl"), "runner {\n}\n")
	writeFile(t, filepath.Join(dir, "runners.star"), goodRunner)
	oldProject := writeFile(t, filepath.Join(dir, "projects", "demo.hcl"), "source = \"/s\"\n")

	_, problems := loadFolder(dir, nil)

	require.Len(t, problems, 2)
	require.ErrorIs(t, problems[0], config.ErrRefused)
	require.ErrorContains(t, problems[0], old)
	require.ErrorContains(t, problems[0], "Rewrite the file as runners.star")
	require.ErrorContains(t, problems[1], oldProject)
	require.ErrorContains(t, problems[1], "Rewrite the file as demo.star")
}

func TestLoadFolder_Runners(t *testing.T) { //nolint:paralleltest // sets HOME
	home := homeDir(t)

	cfg, problems := loadRunnersText(t, goodRunner+"runner(name = \"b\", agent = \"opencode\", keychain = \"svc\")\n")

	require.Empty(t, problems)
	require.Equal(t, []model.Runner{
		{Name: "a", Agent: "claude", ConfigDir: filepath.Join(home, "c")},
		{Name: "b", Agent: "opencode", Keychain: "svc"},
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
		{"duplicate_name", goodRunner + goodRunner, "runners.star:2:7: runner `a` appears twice"},
		{"unknown_agent", `runner(name = "a", agent = "gpt")`, "runners.star:1:7: runner `a`: unknown agent \"gpt\": use one of claude, opencode"},
		{"unknown_argument", `runner(name = "a", agent = "claude", token = "x")`, `runner: unexpected keyword argument "token"`},
		{"missing_name", `runner(agent = "claude")`, "runner: missing argument for name"},
		{"missing_agent", `runner(name = "a")`, "runner: missing argument for agent"},
		{"blank_name", `runner(name = " ", agent = "claude")`, "runners.star:1:7: `name` must be"},
		{"blank_agent", `runner(name = "a", agent = " ")`, "runners.star:1:7: `agent` must be"},
		{"config_dir_of_the_wrong_type", `runner(name = "a", agent = "claude", config_dir = ["x"])`, `for parameter "config_dir": got list, want string`},
		{"empty_config_dir", `runner(name = "a", agent = "claude", config_dir = "")`, "runners.star:1:7: `config_dir` must be"},
		{"keychain_of_the_wrong_type", `runner(name = "a", agent = "claude", keychain = ["x"])`, `for parameter "keychain": got list, want string`},
		{"empty_keychain", `runner(name = "a", agent = "claude", keychain = "")`, "runners.star:1:7: `keychain` must be a non-empty string"},
		{"repeated_runner_argument", `runner(name = "a", name = "b", agent = "claude")`, "Fix the syntax of the file"},
		{"unknown_constructor", "shade()\n", "runners.star:1:1: undefined: shade"},
		{"bad_syntax", "runner(\n", "Fix the syntax of the file"},
		{"positional_argument", `runner("a")`, "runners.star:1:7: `runner` takes keyword arguments only"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadRunnersText(t, tc.text)

			require.Len(t, problems, 1)
			require.ErrorIs(t, problems[0], config.ErrRefused)
			require.ErrorContains(t, problems[0], tc.words)
			require.ErrorContains(t, problems[0], "runners.star")
		})
	}
}

func TestLoadFolder_ARunnerConfigDirExpandsTheHomeFolderOnlyAtTheStart(t *testing.T) { //nolint:paralleltest // sets HOME
	home := homeDir(t)

	cfg, problems := loadRunnersText(t, `runner(name = "a", agent = "claude", config_dir = "~")
runner(name = "b", agent = "claude", config_dir = "/x/~/y")
runner(name = "c", agent = "claude", config_dir = "~other/y")
`)

	require.Empty(t, problems)
	require.Equal(t, []string{home, "/x/~/y", "~other/y"},
		[]string{cfg.Runners[0].ConfigDir, cfg.Runners[1].ConfigDir, cfg.Runners[2].ConfigDir})
}

func TestLoadFolder_AVariableNamedLikeABlockIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadRunnersText(t, "runner = 3\nshade = 1\n")

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "runners.star:1:1: `runner` is a variable, and a config file takes none.")
}

func TestLoadFolder_ARunnerConfigDirNeedsAHomeFolderForTilde(t *testing.T) {
	t.Setenv("HOME", "")

	_, problems := loadRunnersText(t, goodRunner)

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "Set HOME")
}

func TestLoadFolder_Pool(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "pool(five_hour_max = 50, seven_day_max = 100)\n")
	require.Empty(t, problems)
	require.Equal(t, config.Pool{FiveHourMax: 50, SevenDayMax: 100}, cfg.Pool)

	cfg, problems = loadRunnersText(t, "pool(five_hour_max = 12.5)\n")
	require.Empty(t, problems)
	require.Equal(t, config.Pool{FiveHourMax: 12.5, SevenDayMax: 95}, cfg.Pool, "a missing argument keeps its default")

	cfg, problems = loadRunnersText(t, "pool(seven_day_max = 10)\n")
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
		{"zero", "pool(five_hour_max = 0)\n", "`five_hour_max` in the pool block must be a number above 0 and up to 100"},
		{"negative", "pool(seven_day_max = -1)\n", "`seven_day_max` in the pool block"},
		{"over_one_hundred", "pool(five_hour_max = 100.5)\n", "`five_hour_max` in the pool block"},
		{"a_string", "pool(five_hour_max = \"high\")\n", `for parameter "five_hour_max": got string, want int or float`},
		{"a_boolean", "pool(five_hour_max = True)\n", `runners.star:1:5: pool: for parameter "five_hour_max": got bool`},
		{"unknown_argument", "pool(three_hour_max = 5)\n", `pool: unexpected keyword argument "three_hour_max"`},
		{"repeated_pool_call", "pool()\npool()\n", "runners.star:2:5: block `pool` appears twice"},
		{"not_a_call", "pool = 3\n", "runners.star:1:1: `pool` is a variable, and a config file takes none."},
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

	_, problems := loadRunnersText(t, "pool(five_hour_max = 100, seven_day_max = 0.1)\n")

	require.Empty(t, problems)
}

func TestLoadFolder_AgentsMaxConcurrent(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "agents(max_concurrent = 6)\n")
	require.Empty(t, problems)
	require.Equal(t, 6, cfg.Agents.MaxConcurrent)

	cfg, problems = loadRunnersText(t, "agents()\n")
	require.Empty(t, problems)
	require.Equal(t, 2, cfg.Agents.MaxConcurrent, "a call without the argument keeps the default")

	_, problems = loadRunnersText(t, "agents(max_concurrent = 0)\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.max_concurrent must be 1 or more")
	require.ErrorContains(t, problems[0], "runners.star")
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_MAX_CONCURRENT")

	_, problems = loadRunnersText(t, "agents(max_concurrent = 1)\n")
	require.Empty(t, problems)

	_, problems = loadRunnersText(t, "agents(max_threads = 1)\n")
	require.ErrorContains(t, problems[0], `runners.star:1:7: agents: unexpected keyword argument "max_threads"`)
	_, problems = loadRunnersText(t, "agents(max_concurrent = \"many\")\n")
	require.ErrorContains(t, problems[0], `runners.star:1:7: agents: for parameter "max_concurrent": got string, want int`)

	_, problems = loadRunnersText(t, "agents(max_concurrent = 1)\nagents(max_concurrent = 2)\n")
	require.ErrorContains(t, problems[0], "runners.star:2:7: block `agents` appears twice. Keep one.")
}

func TestLoadFolder_AgentsMaxReviewConcurrentIsItsOwnLimit(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "agents(max_concurrent = 6, max_review_concurrent = 3)\n")
	require.Empty(t, problems)
	require.Equal(t, 6, cfg.Agents.MaxConcurrent)
	require.Equal(t, 3, cfg.Agents.MaxReviewConcurrent)

	cfg, problems = loadRunnersText(t, "agents(max_concurrent = 6)\n")
	require.Empty(t, problems)
	require.Equal(t, 2, cfg.Agents.MaxReviewConcurrent, "a call without the argument keeps the default")

	_, problems = loadRunnersText(t, "agents(max_review_concurrent = 0)\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.max_review_concurrent must be 1 or more")
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT")

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), "agents(max_review_concurrent = 2)\n")

	cfg, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT": "5"})
	require.Empty(t, problems)
	require.Equal(t, 5, cfg.Agents.MaxReviewConcurrent)

	_, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT": "many"})
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT=many is not a whole number")

	_, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT": "0"})
	require.ErrorContains(t, problems[0], "agents.max_review_concurrent must be 1 or more")
}

func TestLoadFolder_AgentsMaxTurnsAndBudgetDefaultToNoCapAndRefuseANegative(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "agents()\n")
	require.Empty(t, problems)
	require.Equal(t, 0, cfg.Agents.MaxTurns)
	require.InDelta(t, 0.0, cfg.Agents.MaxBudgetUSD, 0)

	cfg, problems = loadRunnersText(t, "agents(max_turns = 30, max_budget_usd = 2.5)\n")
	require.Empty(t, problems)
	require.Equal(t, 30, cfg.Agents.MaxTurns)
	require.InDelta(t, 2.5, cfg.Agents.MaxBudgetUSD, 0)

	_, problems = loadRunnersText(t, "agents(max_turns = -1)\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.max_turns must be 0 (no cap) or more")
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_MAX_TURNS")

	_, problems = loadRunnersText(t, "agents(max_budget_usd = -0.5)\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.max_budget_usd must be 0 (no cap) or more")
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_MAX_BUDGET_USD")
}

func TestLoadFolder_AgentsMaxRunBudgetDefaultsToNoCapTakesTheEnvAndRefusesABadValue(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "agents()\n")
	require.Empty(t, problems)
	require.InDelta(t, 0.0, cfg.Agents.MaxRunBudgetUSD, 0)

	cfg, problems = loadRunnersText(t, "agents(max_run_budget_usd = 12.5)\n")
	require.Empty(t, problems)
	require.InDelta(t, 12.5, cfg.Agents.MaxRunBudgetUSD, 0)

	_, problems = loadRunnersText(t, "agents(max_run_budget_usd = -1)\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.max_run_budget_usd must be 0 (no cap) or more")
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_MAX_RUN_BUDGET_USD")

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), "agents(max_run_budget_usd = 1)\n")

	cfg, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_MAX_RUN_BUDGET_USD": "7.5"})
	require.Empty(t, problems)
	require.InDelta(t, 7.5, cfg.Agents.MaxRunBudgetUSD, 0)

	_, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_MAX_RUN_BUDGET_USD": "lots"})
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_MAX_RUN_BUDGET_USD=lots is not a number")
}

func TestLoadFolder_TheCapVariablesOverlayTheFileAndABadValueIsRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), "agents(max_turns = 10, max_budget_usd = 1)\n")

	cfg, problems := loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_MAX_TURNS": "25", "BEEHAVE_AGENTS_MAX_BUDGET_USD": "3.25"})
	require.Empty(t, problems)
	require.Equal(t, 25, cfg.Agents.MaxTurns)
	require.InDelta(t, 3.25, cfg.Agents.MaxBudgetUSD, 0)

	_, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_MAX_TURNS": "many"})
	require.NotEmpty(t, problems)
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_MAX_TURNS=many is not a whole number")

	_, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_MAX_BUDGET_USD": "lots"})
	require.NotEmpty(t, problems)
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_MAX_BUDGET_USD=lots is not a number")
}

func TestLoadFolder_ABudgetThatIsNotFiniteIsRefused(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"Inf", "+Inf", "-Inf", "NaN", "1e999"} {
		_, problems := loadFolder(t.TempDir(), map[string]string{"BEEHAVE_AGENTS_MAX_BUDGET_USD": value})

		require.NotEmpty(t, problems, value)
		require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_MAX_BUDGET_USD", value)
	}

	_, problems := loadRunnersText(t, "agents(max_budget_usd = float(\"inf\"))\n")
	require.NotEmpty(t, problems)
	require.ErrorContains(t, problems[0], "agents.max_budget_usd")
}

func TestLoadFolder_EnvironmentOverlay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), "agents(max_concurrent = 2)\npool(five_hour_max = 50)\n")

	cfg, problems := loadFolder(dir, map[string]string{
		"BEEHAVE_AGENTS_MAX_CONCURRENT": "6", "BEEHAVE_POOL_FIVE_HOUR_MAX": "70", "BEEHAVE_POOL_SEVEN_DAY_MAX": "80.5", "REPO": "ignored",
	})
	require.Empty(t, problems)
	require.Equal(t, 6, cfg.Agents.MaxConcurrent)
	require.Equal(t, config.Pool{FiveHourMax: 70, SevenDayMax: 80.5}, cfg.Pool)

	cfg, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_MAX_CONCURRENT": ""})
	require.Empty(t, problems)
	require.Equal(t, 2, cfg.Agents.MaxConcurrent, "an empty variable leaves the file value")

	cfg, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT": "5"})
	require.Empty(t, problems)
	require.Equal(t, 5, cfg.Agents.MaxReviewConcurrent)
	require.Equal(t, 2, cfg.Agents.MaxConcurrent, "the review limit is its own variable")
}

func TestLoadFolder_AnOverlayValueIsValidated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		env   map[string]string
		words string
	}{
		{"text_for_max_concurrent", map[string]string{"BEEHAVE_AGENTS_MAX_CONCURRENT": "many"}, "BEEHAVE_AGENTS_MAX_CONCURRENT=many is not a whole number"},
		{"zero_for_max_concurrent", map[string]string{"BEEHAVE_AGENTS_MAX_CONCURRENT": "0"}, "agents.max_concurrent must be 1 or more"},
		{"text_for_max_review_concurrent", map[string]string{"BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT": "many"}, "BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT=many is not a whole number"},
		{"zero_for_max_review_concurrent", map[string]string{"BEEHAVE_AGENTS_MAX_REVIEW_CONCURRENT": "0"}, "agents.max_review_concurrent must be 1 or more"},
		{"text_for_five_hour_cap", map[string]string{"BEEHAVE_POOL_FIVE_HOUR_MAX": "lots"}, "BEEHAVE_POOL_FIVE_HOUR_MAX=lots is not a number"},
		{"text_for_seven_day_cap", map[string]string{"BEEHAVE_POOL_SEVEN_DAY_MAX": "lots"}, "BEEHAVE_POOL_SEVEN_DAY_MAX=lots is not a number"},
		{"cap_over_one_hundred", map[string]string{"BEEHAVE_POOL_SEVEN_DAY_MAX": "101"}, "`seven_day_max` in the pool block"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadFolder(t.TempDir(), tc.env)

			require.NotEmpty(t, problems)
			require.ErrorContains(t, problems, tc.words)
		})
	}
}

func TestLoadFolder_AProjectsPathThatIsAFileIsReported(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "projects"), "not a folder")

	_, problems := loadFolder(dir, nil)

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "cannot read "+filepath.Join(dir, "projects"))
}

func TestLoadFolder_ARefusalKeepsItsCause(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(dir, "missing.star"), filepath.Join(dir, "projects", "p.star")))

	_, problems := loadFolder(dir, nil)

	require.Len(t, problems, 1)
	require.ErrorIs(t, problems[0], config.ErrRefused)
	require.ErrorIs(t, problems[0], fs.ErrNotExist)

	file := writeFile(t, filepath.Join(dir, "projects", "f"), "x")
	_, problems = loadFolder(file, nil)
	require.Len(t, problems, 1)

	_, problems = loadFolder(filepath.Join(file, "sub"), nil)
	require.Len(t, problems, 1)
}

func TestLoadFolder_ARepeatedArgumentIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(goodProject+`, repo = "o/m"`))

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "Fix the syntax of the file")
	require.ErrorContains(t, problems[0], "repo")
}

func TestLoadFolder_RecursionIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadRunnersText(t, "def f(n):\n  return f(n)\nf(1)\n")

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "runners.star:")
}

func TestLoadFolder_ANotANumberPoolCapIsRefused(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"BEEHAVE_POOL_FIVE_HOUR_MAX", "BEEHAVE_POOL_SEVEN_DAY_MAX"} {
		_, problems := loadFolder(t.TempDir(), map[string]string{name: "NaN"})

		require.Len(t, problems, 1, name)
		require.ErrorContains(t, problems[0], "must be a number above 0 and up to 100")
	}
}

func TestLoadFolder_ABadPoolCapNamesTheFileAndTheVariable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, problems := loadFolder(dir, map[string]string{"BEEHAVE_POOL_FIVE_HOUR_MAX": "101", "BEEHAVE_POOL_SEVEN_DAY_MAX": "0"})

	require.Len(t, problems, 2)

	text := problems.Error()
	require.Contains(t, text, "Set it in "+filepath.Join(dir, "runners.star")+" or in BEEHAVE_POOL_FIVE_HOUR_MAX.")
	require.Contains(t, text, "Set it in "+filepath.Join(dir, "runners.star")+" or in BEEHAVE_POOL_SEVEN_DAY_MAX.")
}

func TestLoadFolder_AgentsReviewPollMinutesDefaultsToNoPollAndRefusesANegative(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "agents()\n")
	require.Empty(t, problems)
	require.Equal(t, 0, cfg.Agents.ReviewPollMinutes)

	cfg, problems = loadRunnersText(t, "agents(review_poll_minutes = 60)\n")
	require.Empty(t, problems)
	require.Equal(t, 60, cfg.Agents.ReviewPollMinutes)

	_, problems = loadRunnersText(t, "agents(review_poll_minutes = -1)\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.review_poll_minutes must be 0 (no poll) or more")
	require.ErrorContains(t, problems[0], "BEEHAVE_REVIEW_POLL_MINUTES")
}

func TestLoadFolder_AgentsRetentionDaysDefaultsToSevenKeepsForeverAtZeroAndRefusesANegative(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "agents()\n")
	require.Empty(t, problems)
	require.Equal(t, 7, cfg.Agents.RetentionDays, "the default keeps the 7 days that the engine always used")

	cfg, problems = loadRunnersText(t, "agents(retention_days = 30)\n")
	require.Empty(t, problems)
	require.Equal(t, 30, cfg.Agents.RetentionDays)

	cfg, problems = loadRunnersText(t, "agents(retention_days = 0)\n")
	require.Empty(t, problems)
	require.Equal(t, 0, cfg.Agents.RetentionDays, "0 keeps the finished runs for ever")

	_, problems = loadRunnersText(t, "agents(retention_days = -1)\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.retention_days must be 0 (keep for ever) or more")
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_RETENTION_DAYS")
}

func TestLoadFolder_AgentsRetentionDaysHasAnUpperBoundThatKeepsTheDurationInRange(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "agents(retention_days = 36500)\n")
	require.Empty(t, problems)
	require.Equal(t, 36500, cfg.Agents.RetentionDays)

	_, problems = loadRunnersText(t, "agents(retention_days = 36501)\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.retention_days is 36501: set it to 36500 (100 years) or less")
	require.ErrorContains(t, problems[0], "BEEHAVE_AGENTS_RETENTION_DAYS")
}

func TestLoadFolder_TheRetentionVariableOverlaysTheFileAndIsValidated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), "agents(retention_days = 30)\n")

	cfg, problems := loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_RETENTION_DAYS": "14"})
	require.Empty(t, problems)
	require.Equal(t, 14, cfg.Agents.RetentionDays)

	cfg, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_RETENTION_DAYS": "0"})
	require.Empty(t, problems)
	require.Equal(t, 0, cfg.Agents.RetentionDays, "0 in the variable keeps the runs for ever")

	cfg, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_RETENTION_DAYS": ""})
	require.Empty(t, problems)
	require.Equal(t, 30, cfg.Agents.RetentionDays, "an empty variable leaves the file value")

	_, problems = loadFolder(dir, map[string]string{"BEEHAVE_AGENTS_RETENTION_DAYS": "a week"})
	require.ErrorContains(t, problems, "BEEHAVE_AGENTS_RETENTION_DAYS=a week is not a whole number")
}

func TestLoadFolder_TheReviewPollVariableOverlaysTheFileAndIsValidated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), "agents(review_poll_minutes = 30)\n")

	cfg, problems := loadFolder(dir, map[string]string{"BEEHAVE_REVIEW_POLL_MINUTES": "15"})
	require.Empty(t, problems)
	require.Equal(t, 15, cfg.Agents.ReviewPollMinutes)

	cfg, problems = loadFolder(dir, map[string]string{"BEEHAVE_REVIEW_POLL_MINUTES": ""})
	require.Empty(t, problems)
	require.Equal(t, 30, cfg.Agents.ReviewPollMinutes, "an empty variable leaves the file value")

	_, problems = loadFolder(dir, map[string]string{"BEEHAVE_REVIEW_POLL_MINUTES": "hourly"})
	require.ErrorContains(t, problems, "BEEHAVE_REVIEW_POLL_MINUTES=hourly is not a whole number")
}

func TestLoadFolder_TheReviewPollIntervalHasAnUpperBoundOfOneWeek(t *testing.T) {
	t.Parallel()

	cfg, problems := loadRunnersText(t, "agents(review_poll_minutes = 10080)\n")
	require.Empty(t, problems)
	require.Equal(t, 10080, cfg.Agents.ReviewPollMinutes)

	_, problems = loadRunnersText(t, "agents(review_poll_minutes = 10081)\n")
	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "agents.review_poll_minutes is 10081: set it to 10080 (one week) or less")
	require.ErrorContains(t, problems[0], "BEEHAVE_REVIEW_POLL_MINUTES")

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "runners.star"), "agents()\n")

	_, problems = loadFolder(dir, map[string]string{"BEEHAVE_REVIEW_POLL_MINUTES": "307445735"})
	require.ErrorContains(t, problems, "agents.review_poll_minutes is 307445735: set it to 10080 (one week) or less")
}
