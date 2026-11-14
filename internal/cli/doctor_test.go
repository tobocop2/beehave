package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
)

// machine is a config folder, a login folder and a PATH folder with fake bd and gh files.
type machine struct {
	config string
	login  string
	bin    string
}

func newMachine(t *testing.T) machine {
	t.Helper()

	m := machine{config: t.TempDir(), login: t.TempDir(), bin: t.TempDir()}
	writeConfig(t, m.config, "runners.star", "runner(name = \"a\", agent = \"claude\", config_dir = \""+m.login+"\", keychain = \"beehave-a\")\n")

	writeConfig(t, m.config, "projects/p.star", validProject)

	for _, tool := range []string{"bd", "gh", "git"} {
		require.NoError(t, exectest.Write(filepath.Join(m.bin, tool), "#!/bin/sh\n"))
	}

	return m
}

func (m machine) env() map[string]string { return map[string]string{"PATH": m.bin} }

func readableTokens(context.Context, string) bool { return true }

func currentBd(context.Context) error { return nil }

// bdVersion is the version check of the tool bd, as the doctor reads it.
func bdVersion(check func(context.Context) error) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{"bd": check}
}

// claudeLogin is the login reader of the agent claude, as the doctor reads it.
func claudeLogin(read func(context.Context, model.Runner) model.Reading) map[model.Agent]func(context.Context, model.Runner) model.Reading {
	return map[model.Agent]func(context.Context, model.Runner) model.Reading{"claude": read}
}

// declaresNone is a complete Doctor whose adapter table declares no tool version, no login read and no fix text: its maps are empty, not nil.
func declaresNone(token func(context.Context, string) bool) cli.Doctor {
	return cli.Doctor{
		TokenReadable:  token,
		ToolVersions:   map[string]func(context.Context) error{},
		Logins:         map[model.Agent]func(context.Context, model.Runner) model.Reading{},
		ConfigDirFixes: map[model.Agent]func(string) string{},
		TokenFixes:     map[model.Agent]func(string) string{},
	}
}

// runDoctor runs the command with a complete Doctor: a reader that the test leaves nil is a passing reader.
func runDoctor(t *testing.T, m machine, doctor cli.Doctor, env map[string]string) (int, string) {
	t.Helper()

	if doctor.TokenReadable == nil {
		doctor.TokenReadable = readableTokens
	}

	if doctor.ToolVersions == nil {
		doctor.ToolVersions = bdVersion(currentBd)
	}

	if doctor.ConfigDirFixes == nil {
		doctor.ConfigDirFixes = map[model.Agent]func(string) string{"claude": func(dir string) string { return "Log in to it: CLAUDE_CONFIG_DIR=" + dir + " claude" }}
	}

	if doctor.TokenFixes == nil {
		doctor.TokenFixes = map[model.Agent]func(string) string{
			"claude": func(entry string) string {
				return "Run `claude setup-token`, then: security add-generic-password -s " + entry + " -a $USER -w"
			},
		}
	}

	if doctor.Logins == nil {
		doctor.Logins = claudeLogin(func(context.Context, model.Runner) model.Reading { return model.Known(model.Usage{}) })
	}

	code, stdout, stderr := execDoctor(m, doctor, env)
	require.Empty(t, stderr)

	return code, stdout
}

func execDoctor(m machine, doctor cli.Doctor, env map[string]string) (int, string, string) {
	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"doctor", "--config", m.config}, cli.BuildInfo{Registry: registry(), Version: "x", Doctor: doctor}, env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestDoctor_EveryCheckPassesOnAGoodMachine(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	code, out := runDoctor(t, m, cli.Doctor{TokenReadable: readableTokens, ToolVersions: bdVersion(currentBd)}, m.env())

	require.Equal(t, 0, code)
	require.Equal(t, "ok   config readable\nok   git on the path\nok   bd on the path\nok   gh on the path\nok   bd version\nok   config_dir of a exists\nok   login of a has plan limits\nok   token of a readable\n", out)
}

const localProjectFile = "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n"

func TestDoctor_AProjectWithNoBeadsAndNoGithubNeedsOnlyGit(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	writeConfig(t, m.config, "projects/p.star", localProjectFile)
	require.NoError(t, os.Remove(filepath.Join(m.bin, "bd")))
	require.NoError(t, os.Remove(filepath.Join(m.bin, "gh")))

	code, out := runDoctor(t, m, cli.Doctor{ToolVersions: bdVersion(func(context.Context) error { return errors.New("never asked") })}, m.env())

	require.Equal(t, 0, code)
	require.Equal(t, "ok   config readable\nok   git on the path\nok   config_dir of a exists\nok   login of a has plan limits\nok   token of a readable\n", out)
}

func TestDoctor_GitIsRequiredEvenWhenTheConfigHasNoProject(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	require.NoError(t, os.Remove(filepath.Join(m.config, "projects", "p.star")))
	require.NoError(t, os.Remove(filepath.Join(m.bin, "git")))

	code, out := runDoctor(t, m, cli.Doctor{}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL git on the path\n     fix: Install git.\n")
	require.NotContains(t, out, "bd on the path")
}

func TestDoctor_OnlyTheBeadsProjectNeedsBdAndOnlyTheGithubProjectNeedsGh(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	writeConfig(t, m.config, "projects/p.star", "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\", claims = \"beads\", beads = beads(prefix = \"p\"))\n")
	require.NoError(t, os.Remove(filepath.Join(m.bin, "gh")))

	code, out := runDoctor(t, m, cli.Doctor{}, m.env())

	require.Equal(t, 0, code)
	require.Contains(t, out, "ok   bd on the path\n")
	require.NotContains(t, out, "gh on the path")
}

func TestDoctor_AConfigThatDoesNotLoadFailsWithTheFix(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	writeConfig(t, m.config, "runners.star", "runner(name = \"a\", agent = \"nobody\")\n")

	code, out := runDoctor(t, m, cli.Doctor{}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL config readable\n     fix: ")
	require.Contains(t, out, "unknown agent")
	require.Contains(t, out, "Fix the file")
}

func TestDoctor_AMissingConfigDirFailsWithTheLoginCommand(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	require.NoError(t, os.Remove(m.login))

	code, out := runDoctor(t, m, cli.Doctor{}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL config_dir of a exists\n     fix: Log in to it: CLAUDE_CONFIG_DIR="+m.login+" claude\n")
}

func TestDoctor_ARunnerWithNoPlanLimitsFailsWithTheLoginFix(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	var asked []string

	read := func(_ context.Context, runner model.Runner) model.Reading {
		asked = append(asked, runner.Name+" "+runner.ConfigDir)

		return model.UnknownLogin("runner a has no Claude plan limits. Log in with `CLAUDE_CONFIG_DIR=" + runner.ConfigDir + " claude` and run /login")
	}

	code, out := runDoctor(t, m, cli.Doctor{Logins: claudeLogin(read)}, m.env())

	require.Equal(t, 1, code)
	require.Equal(t, []string{"a " + m.login}, asked)
	require.Contains(t, out, "FAIL login of a has plan limits\n     fix: runner a has no Claude plan limits. Log in with `CLAUDE_CONFIG_DIR="+m.login+" claude` and run /login\n")
}

func TestDoctor_AUsageThatCannotBeReadFailsWithTheReason(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	read := func(context.Context, model.Runner) model.Reading { return model.Unknown("claude is not installed") }

	code, out := runDoctor(t, m, cli.Doctor{Logins: claudeLogin(read)}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL login of a has plan limits\n     fix: claude is not installed\n")
}

func TestDoctor_ARunnerWithoutAConfigDirIsNotReadForUsage(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	writeConfig(t, m.config, "runners.star", validRunners)

	read := func(context.Context, model.Runner) model.Reading { return model.Unknown("never asked") }

	code, out := runDoctor(t, m, cli.Doctor{Logins: claudeLogin(read)}, m.env())

	require.Equal(t, 0, code)
	require.NotContains(t, out, "login of")
}

func TestDoctor_AConfigDirThatIsAFileFails(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	require.NoError(t, os.Remove(m.login))
	require.NoError(t, os.WriteFile(m.login, []byte("x"), 0o600))

	code, out := runDoctor(t, m, cli.Doctor{}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL config_dir of a exists")
}

func TestDoctor_AnUnreadableTokenFailsWithTheKeychainFix(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	var asked []string

	tokens := func(_ context.Context, entry string) bool {
		asked = append(asked, entry)

		return false
	}

	code, out := runDoctor(t, m, cli.Doctor{TokenReadable: tokens}, m.env())

	require.Equal(t, 1, code)
	require.Equal(t, []string{"beehave-a"}, asked)
	require.Contains(t, out, "FAIL token of a readable\n     fix: Run `claude setup-token`, then: security add-generic-password -s beehave-a -a $USER -w\n")
}

func TestDoctor_ARunnerWithoutAKeychainEntryHasNoTokenCheck(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	writeConfig(t, m.config, "runners.star", validRunners)

	_, out := runDoctor(t, m, cli.Doctor{TokenReadable: func(context.Context, string) bool { return false }}, m.env())

	require.NotContains(t, out, "token of")
	require.NotContains(t, out, "config_dir of")
}

func TestDoctor_AMissingToolFailsWithTheInstallFix(t *testing.T) {
	t.Parallel()

	for _, tool := range []string{"bd", "gh", "git"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()

			m := newMachine(t)
			require.NoError(t, os.Remove(filepath.Join(m.bin, tool)))

			code, out := runDoctor(t, m, cli.Doctor{ToolVersions: bdVersion(currentBd)}, m.env())

			require.Equal(t, 1, code)
			require.Contains(t, out, "FAIL "+tool+" on the path\n     fix: Install "+tool+".\n")
		})
	}
}

func TestDoctor_AMissingBdSkipsTheVersionCheck(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	require.NoError(t, os.Remove(filepath.Join(m.bin, "bd")))

	_, out := runDoctor(t, m, cli.Doctor{ToolVersions: bdVersion(func(context.Context) error { return errors.New("never asked") })}, m.env())

	require.NotContains(t, out, "bd version")
}

func TestDoctor_AnOldBdFailsWithTheMessageOfTheVersionCheck(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	old := func(context.Context) error {
		return errors.New("beehave needs bd 1.3.2 or newer for claims; found 0.56.1")
	}

	code, out := runDoctor(t, m, cli.Doctor{ToolVersions: bdVersion(old)}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL bd version\n     fix: beehave needs bd 1.3.2 or newer for claims; found 0.56.1\n")
}

func TestDoctor_ADoctorWithoutAReaderIsRefusedByNameAndNoCheckRuns(t *testing.T) {
	t.Parallel()

	whole := func() cli.Doctor {
		return cli.Doctor{
			TokenReadable:  readableTokens,
			ToolVersions:   bdVersion(currentBd),
			Logins:         claudeLogin(func(context.Context, model.Runner) model.Reading { return model.Known(model.Usage{}) }),
			ConfigDirFixes: map[model.Agent]func(string) string{},
			TokenFixes:     map[model.Agent]func(string) string{},
		}
	}

	cases := map[string]struct {
		strip func(*cli.Doctor)
		field string
	}{
		"token":         {func(d *cli.Doctor) { d.TokenReadable = nil }, "TokenReadable"},
		"tool versions": {func(d *cli.Doctor) { d.ToolVersions = nil }, "ToolVersions"},
		"logins":        {func(d *cli.Doctor) { d.Logins = nil }, "Logins"},
		"config fixes":  {func(d *cli.Doctor) { d.ConfigDirFixes = nil }, "ConfigDirFixes"},
		"token fixes":   {func(d *cli.Doctor) { d.TokenFixes = nil }, "TokenFixes"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			doctor := whole()
			tc.strip(&doctor)

			m := newMachine(t)
			code, out, errText := execDoctor(m, doctor, m.env())

			require.Equal(t, 1, code)
			require.Empty(t, out)
			require.Contains(t, errText, tc.field)
		})
	}

	m := newMachine(t)
	code, out, errText := execDoctor(m, cli.Doctor{}, m.env())

	require.Equal(t, 1, code)
	require.Empty(t, out)
	require.Contains(t, errText, "TokenReadable, ToolVersions, Logins, ConfigDirFixes, TokenFixes")
}

func TestDoctor_AToolWithNoVersionCheckAndAnAgentWithNoLoginReaderNeedOnlyTheirPresence(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	code, out, errText := execDoctor(m, declaresNone(readableTokens), m.env())

	require.Equal(t, 0, code)
	require.Empty(t, errText)
	require.NotContains(t, out, "bd version")
	require.NotContains(t, out, "login of")
}

func TestDoctor_AVersionErrorWithNoTextIsStillAFailure(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	silent := func(context.Context) error { return errors.New("") }

	code, out := runDoctor(t, m, cli.Doctor{ToolVersions: bdVersion(silent)}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL bd version\n")
}

func TestDoctor_FailsWithExitOneWhenThereIsNoHomeFolder(t *testing.T) {
	t.Setenv("HOME", "")

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"doctor"}, cli.BuildInfo{Registry: registry(), Version: "x", Doctor: declaresNone(readableTokens)}, map[string]string{}, &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), "beehave: cannot find the home folder")
}

func TestDoctor_AMissingConfigFolderFailsTheConfigCheck(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	m.config = filepath.Join(m.config, "none")

	code, out := runDoctor(t, m, cli.Doctor{}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL config readable")
}

func TestDoctor_AnAgentWithNoFixTextGetsTheGenericFix(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	require.NoError(t, os.Remove(m.login))

	denied := func(context.Context, string) bool { return false }
	code, out, errText := execDoctor(m, declaresNone(denied), m.env())

	require.Equal(t, 1, code)
	require.Empty(t, errText)
	require.Contains(t, out, "FAIL config_dir of a exists\n     fix: Log in to the claude CLI in "+m.login+".\n")
	require.Contains(t, out, "FAIL token of a readable\n     fix: Store the token of the claude CLI in the Keychain entry beehave-a.\n")
}
