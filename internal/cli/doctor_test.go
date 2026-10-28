package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
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
	writeConfig(t, m.config, "runners.hcl", "runner {\nname = \"a\"\nagent = \"claude\"\nconfig_dir = \""+m.login+"\"\nkeychain = \"obee-a\"\n}\n")

	writeConfig(t, m.config, "projects/p.hcl", validProject)

	for _, tool := range []string{"bd", "gh", "git"} {
		require.NoError(t, os.WriteFile(filepath.Join(m.bin, tool), []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // a fake program must be executable
	}

	return m
}

func (m machine) env() map[string]string { return map[string]string{"PATH": m.bin} }

func readableTokens(context.Context, string) bool { return true }

func currentBd(context.Context) error { return nil }

// runDoctor runs the command with a complete Doctor: a reader that the test leaves nil is a passing reader.
func runDoctor(t *testing.T, m machine, doctor cli.Doctor, env map[string]string) (int, string) {
	t.Helper()

	if doctor.TokenReadable == nil {
		doctor.TokenReadable = readableTokens
	}

	if doctor.BdVersion == nil {
		doctor.BdVersion = currentBd
	}

	code, stdout, stderr := execDoctor(m, doctor, env)
	require.Empty(t, stderr)

	return code, stdout
}

func execDoctor(m machine, doctor cli.Doctor, env map[string]string) (int, string, string) {
	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"doctor", "--config", m.config}, cli.BuildInfo{Version: "x", Doctor: doctor}, env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestDoctor_EveryCheckPassesOnAGoodMachine(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	code, out := runDoctor(t, m, cli.Doctor{TokenReadable: readableTokens, BdVersion: currentBd}, m.env())

	require.Equal(t, 0, code)
	require.Equal(t, "ok   config readable\nok   git on the path\nok   bd on the path\nok   gh on the path\nok   bd version\nok   config_dir of a exists\nok   token of a readable\n", out)
}

const localProjectFile = "source = \"/srv/repo\"\nbase_branch = \"main\"\ngate = \"make check\"\n"

func TestDoctor_AProjectWithNoBeadsAndNoGithubNeedsOnlyGit(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	writeConfig(t, m.config, "projects/p.hcl", localProjectFile)
	require.NoError(t, os.Remove(filepath.Join(m.bin, "bd")))
	require.NoError(t, os.Remove(filepath.Join(m.bin, "gh")))

	code, out := runDoctor(t, m, cli.Doctor{BdVersion: func(context.Context) error { return errors.New("never asked") }}, m.env())

	require.Equal(t, 0, code)
	require.Equal(t, "ok   config readable\nok   git on the path\nok   config_dir of a exists\nok   token of a readable\n", out)
}

func TestDoctor_GitIsRequiredEvenWhenTheConfigHasNoProject(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	require.NoError(t, os.Remove(filepath.Join(m.config, "projects", "p.hcl")))
	require.NoError(t, os.Remove(filepath.Join(m.bin, "git")))

	code, out := runDoctor(t, m, cli.Doctor{}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL git on the path\n     fix: Install git.\n")
	require.NotContains(t, out, "bd on the path")
}

func TestDoctor_OnlyTheBeadsProjectNeedsBdAndOnlyTheGithubProjectNeedsGh(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	writeConfig(t, m.config, "projects/p.hcl", "source = \"/srv/repo\"\nbase_branch = \"main\"\ngate = \"make check\"\nclaims = \"beads\"\nbeads_prefix = \"p\"\n")
	require.NoError(t, os.Remove(filepath.Join(m.bin, "gh")))

	code, out := runDoctor(t, m, cli.Doctor{}, m.env())

	require.Equal(t, 0, code)
	require.Contains(t, out, "ok   bd on the path\n")
	require.NotContains(t, out, "gh on the path")
}

func TestDoctor_AConfigThatDoesNotLoadFailsWithTheFix(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	writeConfig(t, m.config, "runners.hcl", "runner {\nname = \"a\"\nagent = \"nobody\"\n}\n")

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
	require.Equal(t, []string{"obee-a"}, asked)
	require.Contains(t, out, "FAIL token of a readable\n     fix: Run `claude setup-token`, then: security add-generic-password -s obee-a -a $USER -w\n")
}

func TestDoctor_ARunnerWithoutAKeychainEntryHasNoTokenCheck(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	writeConfig(t, m.config, "runners.hcl", "runner {\nname = \"a\"\nagent = \"claude\"\n}\n")

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

			code, out := runDoctor(t, m, cli.Doctor{BdVersion: currentBd}, m.env())

			require.Equal(t, 1, code)
			require.Contains(t, out, "FAIL "+tool+" on the path\n     fix: Install "+tool+".\n")
		})
	}
}

func TestDoctor_AMissingBdSkipsTheVersionCheck(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	require.NoError(t, os.Remove(filepath.Join(m.bin, "bd")))

	_, out := runDoctor(t, m, cli.Doctor{BdVersion: func(context.Context) error { return errors.New("never asked") }}, m.env())

	require.NotContains(t, out, "bd version")
}

func TestDoctor_AnOldBdFailsWithTheMessageOfTheVersionCheck(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	old := func(context.Context) error {
		return errors.New("obee needs bd 1.3.2 or newer for claims; found 0.56.1")
	}

	code, out := runDoctor(t, m, cli.Doctor{BdVersion: old}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL bd version\n     fix: obee needs bd 1.3.2 or newer for claims; found 0.56.1\n")
}

func TestDoctor_ADoctorWithoutAReaderIsRefusedAndNoCheckRuns(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		doctor cli.Doctor
		fields []string
	}{
		"both":  {cli.Doctor{}, []string{"TokenReadable", "BdVersion"}},
		"token": {cli.Doctor{BdVersion: currentBd}, []string{"TokenReadable"}},
		"bd":    {cli.Doctor{TokenReadable: readableTokens}, []string{"BdVersion"}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m := newMachine(t)
			code, out, errText := execDoctor(m, tc.doctor, m.env())

			require.Equal(t, 1, code)
			require.Empty(t, out)

			for _, field := range tc.fields {
				require.Contains(t, errText, field)
			}
		})
	}
}

func TestDoctor_AVersionErrorWithNoTextIsStillAFailure(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	silent := func(context.Context) error { return errors.New("") }

	code, out := runDoctor(t, m, cli.Doctor{BdVersion: silent}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL bd version\n")
}

func TestDoctor_FailsWithExitOneWhenThereIsNoHomeFolder(t *testing.T) {
	t.Setenv("HOME", "")

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"doctor"}, cli.BuildInfo{Version: "x", Doctor: cli.Doctor{TokenReadable: readableTokens, BdVersion: currentBd}}, map[string]string{}, &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), "obee: cannot find the home folder")
}

func TestDoctor_AMissingConfigFolderFailsTheConfigCheck(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	m.config = filepath.Join(m.config, "none")

	code, out := runDoctor(t, m, cli.Doctor{}, m.env())

	require.Equal(t, 1, code)
	require.Contains(t, out, "FAIL config readable")
}
