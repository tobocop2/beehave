package cli_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/model"
)

const gateList = "## Gate steps\nDerived from: ci.yaml at 1a2b3c4d\n| name | command |\n|---|---|\n| fmt | `cargo fmt --check` |\n"

const rulesFile = "# Rules\n\n## Maintainer preflight\n\nnone\n\n## CI checks and their base\n\n- Run: make check\n- Base: origin/main\n\n## Standing rules\n\n- Take: none\n"

// projectConfig writes a config folder with one project named p, the repo o/n and the base branch main.
func projectConfig(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.toml", validRunners)
	writeConfig(t, dir, "projects/p.toml", validProject)

	return dir
}

// twoProjectConfig writes a config folder with project p (repo o/n, base branch main) and project a (repo o/other, base branch develop).
// A command that ignores --project and takes project a gets other values than the ones that the tests assert.
func twoProjectConfig(t *testing.T) string {
	t.Helper()

	dir := projectConfig(t)
	other := strings.NewReplacer(`"o/n"`, `"o/other"`, `"main"`, `"develop"`).Replace(validProject)
	writeConfig(t, dir, "projects/a.toml", other)

	return dir
}

func TestCheckJob_TakesTheLaneScriptAndListAsFlags(t *testing.T) {
	t.Parallel()

	script := writeFile(t, "job.sh", "#!/bin/sh\necho hi\n")
	list := writeFile(t, "list.md", gateList)

	code, stdout, stderr := runCLI(t, nil, cli.BuildInfo{}, "check", "job", "--script", script, "--list", list)

	require.Equal(t, 1, code, stderr)
	assert.NotEmpty(t, stdout)
}

func TestCheckJob_RefusesAMissingFlag(t *testing.T) {
	t.Parallel()

	script := writeFile(t, "job.sh", "#!/bin/sh\necho hi\n")

	code, _, stderr := runCLI(t, nil, cli.BuildInfo{}, "check", "job", "--script", script)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "list")
}

func TestLedgerAdd_PRFlagReadsTheBodyOfThePullRequest(t *testing.T) {
	t.Parallel()

	var gotRepo string

	var gotPR int

	info := cli.BuildInfo{Check: cli.CheckDeps{PRBody: func(_ context.Context, repo string, pr int) (string, error) {
		gotRepo, gotPR = repo, pr

		return "Closes #5\n", nil
	}}}

	code, stdout, stderr := runCLI(t, map[string]string{"REPO": "o/env", "OBEE_STATE_DIR": t.TempDir()}, info, "ledger", "add", "name", "lane-a", "review", "--pr", "7")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, 7, gotPR)
	assert.Equal(t, "o/env", gotRepo)
	assert.Contains(t, stdout, "5")
}

func TestCheckScope_ProjectFlagSuppliesTheRepo(t *testing.T) {
	t.Parallel()

	var gotRepo string

	info := cli.BuildInfo{Check: cli.CheckDeps{
		Closes:    func(string) ([]string, error) { return []string{"5"}, nil },
		OwnerAdds: func(string) ([]string, error) { return nil, nil },
		PRBody: func(_ context.Context, repo string, _ int) (string, error) {
			gotRepo = repo

			return "Closes #5\n", nil
		},
	}}

	code, _, stderr := runCLI(t, map[string]string{"REPO": "o/env"}, info, "check", "scope", "7", "--config", twoProjectConfig(t), "--project", "p")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "o/n", gotRepo)
}

func TestCheckPapers_ProjectFlagSuppliesTheTrunk(t *testing.T) {
	t.Parallel()

	var got []string

	info := cli.BuildInfo{Check: cli.CheckDeps{Git: func(_ context.Context, args []string) model.CommandResult {
		got = args

		return model.CommandResult{}
	}}}

	code, _, stderr := runCLI(t, nil, info, "check", "papers", "HEAD", "--config", twoProjectConfig(t), "--project", "p")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, strings.Join(got, " "), "origin/main..HEAD")
	assert.NotContains(t, strings.Join(got, " "), "develop")
}

func TestCheckScope_AnUnknownProjectExitsTwo(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{
		Closes:    func(string) ([]string, error) { return nil, nil },
		OwnerAdds: func(string) ([]string, error) { return nil, nil },
		PRBody:    func(context.Context, string, int) (string, error) { return "", nil },
	}}

	code, _, stderr := runCLI(t, nil, info, "check", "scope", "7", "--config", projectConfig(t), "--project", "zzz")

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, `no project "zzz"`)
}

func TestProjectFlags_RefuseAnUnusableConfigOnEveryCommand(t *testing.T) {
	t.Parallel()

	info := cli.BuildInfo{Check: cli.CheckDeps{
		Git:       func(context.Context, []string) model.CommandResult { return model.CommandResult{} },
		Closes:    func(string) ([]string, error) { return nil, nil },
		OwnerAdds: func(string) ([]string, error) { return nil, nil },
		PRBody:    func(context.Context, string, int) (string, error) { return "", nil },
	}}
	rules := writeFile(t, "rules.md", rulesFile)
	brief := []string{"brief", "new", "impl", "lane-a", "sonnet", "--key", "12", "--mechanism", "m1", "--rules", rules, "--acceptance", "none: no output", "--base-result", "none: no output"}

	commands := map[string][]string{
		"papers": {"check", "papers", "HEAD"},
		"scope":  {"check", "scope", "7"},
		"brief":  brief,
	}

	for name, args := range commands {
		t.Run(name+"_with_a_missing_folder", func(t *testing.T) {
			t.Parallel()

			code, _, stderr := runCLI(t, map[string]string{"OBEE_STATE_DIR": t.TempDir()}, info, append(args, "--config", filepath.Join(t.TempDir(), "none"))...)

			require.Equal(t, 2, code)
			assert.Contains(t, stderr, "obee check config")
		})
	}
}

func TestProjectFlags_RefuseAMissingHomeFolder(t *testing.T) {
	t.Setenv("HOME", "")

	info := cli.BuildInfo{Check: cli.CheckDeps{Git: func(context.Context, []string) model.CommandResult { return model.CommandResult{} }}}

	code, _, stderr := runCLI(t, nil, info, "check", "papers", "HEAD", "--project", "p")

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "cannot find the home folder")
}

func TestCheckPapers_ConfigAloneChoosesTheOnlyProject(t *testing.T) {
	t.Parallel()

	var got []string

	info := cli.BuildInfo{Check: cli.CheckDeps{Git: func(_ context.Context, args []string) model.CommandResult {
		got = args

		return model.CommandResult{}
	}}}

	code, _, stderr := runCLI(t, nil, info, "check", "papers", "HEAD", "--config", projectConfig(t))

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, strings.Join(got, " "), "origin/main..HEAD")
}

func TestCheckPapers_ARangeWithTwoDotsIsKept(t *testing.T) {
	t.Parallel()

	var got []string

	info := cli.BuildInfo{Check: cli.CheckDeps{Git: func(_ context.Context, args []string) model.CommandResult {
		got = args

		return model.CommandResult{}
	}}}

	code, _, stderr := runCLI(t, nil, info, "check", "papers", "a..b", "--project", "p")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, strings.Join(got, " "), "a..b")
}

func TestLedgerAdd_PRFlagWithoutAForgeExitsTwo(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCLI(t, map[string]string{"OBEE_STATE_DIR": t.TempDir()}, cli.BuildInfo{}, "ledger", "add", "name", "lane-a", "review", "--pr", "7")

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "--body-file")
}

func TestCheckJob_RefusesEachMissingFlag(t *testing.T) {
	t.Parallel()

	list := writeFile(t, "list.md", gateList)
	script := writeFile(t, "job.sh", "#!/bin/sh\n")

	cases := map[string][]string{
		"script": {"--list", list},
		"list":   {"--script", script},
	}

	for flag, args := range cases {
		code, _, stderr := runCLI(t, nil, cli.BuildInfo{}, append([]string{"check", "job"}, args...)...)

		require.Equal(t, 2, code)
		assert.Contains(t, stderr, "--"+flag)
	}
}

func TestBriefNew_ProjectFlagSuppliesTheBaseBranch(t *testing.T) {
	t.Parallel()

	rules := writeFile(t, "rules.md", rulesFile)
	config := twoProjectConfig(t)

	code, stdout, stderr := runCLI(t, map[string]string{"OBEE_STATE_DIR": t.TempDir()}, cli.BuildInfo{}, "brief", "new", "impl", "lane-a", "sonnet", "--key", "12", "--mechanism", "m1", "--rules", rules,
		"--acceptance", "none: no output", "--base-result", "none: no output", "--config", config, "--project", "p")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "Base branch of your pull request: `main`")
	assert.NotContains(t, stdout, "develop")
}
