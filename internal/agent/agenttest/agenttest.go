// Package agenttest holds the conformance suite for agent.Adapter. The test of an adapter runs the suite against the adapter, and the suite starts a fake CLI in place of the real one.
// Every new adapter package must call TestAdapter from its tests.
package agenttest

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	prompt   = "do the step"
	token    = "tok-suite-value"
	leakName = "BEEHAVE_SUITE_LEAK"
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
	t.Run("a_launch_keeps_its_files_inside_the_run_folder", func(t *testing.T) {
		CheckLaunch(t, make())
	})
	t.Run("a_line_with_no_call_holds_no_call", func(t *testing.T) {
		CheckCalls(t, make())
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

	if err := exectest.Write(filepath.Join(dir, profile.Binary), fakeCLI); err != nil {
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

	args := append(slices.Clone(profile.Args), profile.EditArgs...)
	expect(r, slices.Contains(seen, "args="+strings.Join(args, " ")), "the CLI did not get the arguments of the profile %q", args)
	expect(r, slices.Contains(seen, "stdin="+prompt), "the CLI did not get the prompt on stdin")

	for _, name := range profile.ConfigVars {
		expect(r, slices.Contains(seen, name+"="+dir), "the login variable %s does not hold the login folder", name)
	}

	expect(r, profile.TokenVar == "" || hasPrefix(seen, profile.TokenVar+"="), "the token variable %s is not set", profile.TokenVar)
	expect(r, !hasPrefix(seen, leakName+"="), "the CLI saw %s, a variable of the caller", leakName)
}

// CheckLaunch checks that every file of the launch of the guard and of the sandbox stays inside the run folder: a local path, with no absolute path and no "..".
// The runner refuses a run whose launch breaks this, so an adapter that breaks it never starts.
func CheckLaunch(r Reporter, adapter agent.Adapter) {
	r.Helper()

	const runDir = "/run/folder"

	checkFiles(r, "guard", adapter.Guard("beehave check command", runDir).Files)

	if adapter.Profile().Sandbox {
		checkFiles(r, "sandbox", adapter.Sandbox(agent.SandboxPlan{Work: "/work"}, "beehave check command", runDir).Files)
	}
}

func checkFiles(r Reporter, source string, files map[string]string) {
	r.Helper()

	for name := range files {
		expect(r, filepath.IsLocal(name), "the launch file %q is outside the run folder (%s)", name, source)
	}
}

// CheckCalls checks that a line which is not a tool call holds no call.
func CheckCalls(r Reporter, adapter agent.Adapter) {
	r.Helper()

	seen := adapter.Calls("not a line of the output")

	expect(r, len(seen.Shell) == 0, "a line with no call holds the shell calls %v", seen.Shell)
	expect(r, len(seen.Skills) == 0, "a line with no call holds the skill calls %q", seen.Skills)
	expect(r, len(seen.Refused) == 0 && !seen.Result, "a line with no call holds a refusal or a result: %+v", seen)
}

func hasPrefix(lines []string, prefix string) bool {
	return slices.ContainsFunc(lines, func(line string) bool { return strings.HasPrefix(line, prefix) })
}
