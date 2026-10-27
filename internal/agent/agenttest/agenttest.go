// Package agenttest holds the conformance suite for agent.Adapter. The test of an adapter runs the suite against the adapter, and the suite starts a fake CLI in place of the real one.
// Every new adapter package must call TestAdapter from its tests.
package agenttest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
)

const (
	prompt     = "do the step"
	token      = "tok-suite-value"
	leakName   = "OBEE_SUITE_LEAK"
	scriptMode = 0o700
	// fakeCLI prints the arguments, the standard input and the environment of the run, one item for each line.
	fakeCLI = "#!/bin/sh\necho \"args=$*\"\necho \"stdin=$(cat)\"\nenv\n"
)

// Reporter is the part of testing.T the suite uses. A test of the suite passes a recorder to see a failure.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// Factory returns a new adapter.
type Factory func() agent.Adapter

// TestAdapter runs the conformance suite against the adapter that make returns, as testing/fstest.TestFS does for a file system.
func TestAdapter(t *testing.T, make Factory) {
	t.Helper()
	t.Run("the_profile_names_a_binary_and_a_login", func(t *testing.T) {
		CheckProfile(t, make())
	})
	t.Run("a_run_follows_the_profile", func(t *testing.T) {
		CheckRun(t, make(), t.TempDir())
	})
	t.Run("an_unreadable_transcript_is_an_error", func(t *testing.T) {
		CheckUnreadable(t, make(), filepath.Join(t.TempDir(), "none.jsonl"))
	})
}

func expect(r Reporter, ok bool, format string, args ...any) {
	r.Helper()

	if !ok {
		r.Errorf(format, args...)
	}
}

// CheckProfile checks that the profile names a binary to find on PATH and the variables that point the CLI at its own login.
func CheckProfile(r Reporter, adapter agent.Adapter) {
	r.Helper()

	profile := adapter.Profile()

	expect(r, profile.Binary != "", "the profile has no binary")
	expect(r, !strings.ContainsRune(profile.Binary, filepath.Separator), "the binary %q is a path: the runner looks the name up on PATH", profile.Binary)
	expect(r, len(profile.ConfigVars) > 0, "the profile has no login variable: the CLI would use the login of the caller")
}

// CheckRun starts a fake CLI named by the profile, in dir, and checks what it saw: the arguments of the profile, the prompt on stdin,
// the login folder in each login variable, the token in its variable and none of the other variables of the caller.
func CheckRun(r Reporter, adapter agent.Adapter, dir string) {
	r.Helper()

	profile := adapter.Profile()

	if err := os.WriteFile(filepath.Join(dir, profile.Binary), []byte(fakeCLI), scriptMode); err != nil {
		r.Errorf("cannot write the fake CLI: %v", err)

		return
	}

	var out bytes.Buffer

	result := runFake(adapter, dir, &out)

	expect(r, result.Outcome == agent.OutcomeExited && result.ExitCode == 0, "the run did not exit with code 0: %+v", result)
	checkSeen(r, profile, dir, strings.Split(out.String(), "\n"))
}

// runFake starts the adapter in dir with the prompt, the token and one variable of the caller that must not reach the CLI.
func runFake(adapter agent.Adapter, dir string, out io.Writer) agent.Result {
	return agent.Runner{}.Run(context.Background(), agent.Spec{
		Adapter: adapter,
		Runner:  model.Runner{Name: "suite", Agent: model.Agent("suite"), ConfigDir: dir},
		Dir:     dir,
		Prompt:  prompt,
		Env:     map[string]string{"PATH": dir + ":/bin:/usr/bin", leakName: "leak"},
		Token:   token,
		Log:     out,
	})
}

// checkSeen checks the lines the fake CLI printed against the profile.
func checkSeen(r Reporter, profile agent.Profile, dir string, seen []string) {
	r.Helper()

	expect(r, slices.Contains(seen, "args="+strings.Join(profile.Args, " ")), "the CLI did not get the arguments of the profile %q", profile.Args)
	expect(r, slices.Contains(seen, "stdin="+prompt), "the CLI did not get the prompt on stdin")

	for _, name := range profile.ConfigVars {
		expect(r, slices.Contains(seen, name+"="+dir), "the login variable %s does not hold the login folder", name)
	}

	expect(r, profile.TokenVar == "" || hasPrefix(seen, profile.TokenVar+"="), "the token variable %s is not set", profile.TokenVar)
	expect(r, !hasPrefix(seen, leakName+"="), "the CLI saw %s, a variable of the caller", leakName)
}

// CheckUnreadable checks that both readers refuse the transcript at missing, a path with no file, with an error that wraps agent.ErrUnreadable.
func CheckUnreadable(r Reporter, adapter agent.Adapter, missing string) {
	r.Helper()

	_, bashErr := adapter.BashCalls(missing)
	_, skillErr := adapter.SkillCalls(missing, "")

	expect(r, errors.Is(bashErr, agent.ErrUnreadable), "BashCalls of a missing file returned %v, not agent.ErrUnreadable", bashErr)
	expect(r, errors.Is(skillErr, agent.ErrUnreadable), "SkillCalls of a missing file returned %v, not agent.ErrUnreadable", skillErr)
}

func hasPrefix(lines []string, prefix string) bool {
	return slices.ContainsFunc(lines, func(line string) bool { return strings.HasPrefix(line, prefix) })
}
