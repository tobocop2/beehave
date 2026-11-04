// Package agenttest holds the conformance suite for agent.Adapter. The test of an adapter runs the suite against the adapter, and the suite starts a fake CLI in place of the real one.
// Every new adapter package must call TestAdapter from its tests.
package agenttest

import (
	"bytes"
	"context"
	"fmt"
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

// Option changes what TestAdapter checks.
type Option func(*options)

type options struct{ costLine string }

// WithCostLine gives a line of the output of the CLI that reports dollars. An adapter whose profile sets StreamsCost must pass one: the suite checks that
// Calls reads dollars from it (Seen.Spent above 0) and returns the same other fields for each reader.
func WithCostLine(line string) Option {
	return func(o *options) { o.costLine = line }
}

// TestAdapter runs the conformance suite against the adapter that make returns, as testing/fstest.TestFS does for a file system.
func TestAdapter(t *testing.T, make Factory, opts ...Option) {
	t.Helper()

	var o options
	for _, opt := range opts {
		opt(&o)
	}

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
	testLimits(t, make, o)
}

// testLimits runs the arms for the no-edit settings and the cost cap of the profile.
func testLimits(t *testing.T, make Factory, o options) {
	t.Helper()
	t.Run("a_step_with_no_edits_follows_the_no_edit_profile", func(t *testing.T) {
		CheckNoEdits(t, make(), t.TempDir())
	})
	t.Run("a_cost_cap_follows_the_profile", func(t *testing.T) {
		CheckBudget(t, make(), t.TempDir(), o.costLine)
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
func runFake(adapter agent.Adapter, dir string, out io.Writer, change ...func(*agent.Spec)) agent.Result {
	spec := agent.Spec{
		Adapter: adapter,
		Runner:  model.Runner{Name: "suite", Agent: model.Agent("suite"), ConfigDir: dir},
		Dir:     dir,
		Prompt:  prompt,
		Env:     map[string]string{"PATH": dir + ":/bin:/usr/bin", leakName: "leak"},
		Token:   token,
		Log:     out,
	}

	for _, set := range change {
		set(&spec)
	}

	return agent.Runner{}.Run(context.Background(), spec)
}

// CheckNoEdits starts the fake CLI with NoEdits set, and checks that it got the arguments of the profile followed by NoEditArgs, with no EditArgs, and every
// variable of NoEditEnv. A run that may edit must get none of them.
func CheckNoEdits(r Reporter, adapter agent.Adapter, dir string) {
	r.Helper()

	profile := adapter.Profile()

	if !writeFake(r, profile, dir) {
		return
	}

	var noEdits, edits bytes.Buffer

	runFake(adapter, dir, &noEdits, func(s *agent.Spec) { s.NoEdits = true })
	runFake(adapter, dir, &edits)

	checkNoEditSeen(r, profile, strings.Split(noEdits.String(), "\n"), strings.Split(edits.String(), "\n"))
}

// checkNoEditSeen checks the lines that the fake CLI printed for a step with no edits (seen) and for a step that may edit (mayEdit).
func checkNoEditSeen(r Reporter, profile agent.Profile, seen, mayEdit []string) {
	r.Helper()

	args := append(slices.Clone(profile.Args), profile.NoEditArgs...)
	expect(r, slices.Contains(seen, "args="+strings.Join(args, " ")), "the CLI of a step with no edits did not get the arguments %q", args)

	for name, value := range profile.NoEditEnv {
		expect(r, name != profile.TokenVar && !slices.Contains(profile.ConfigVars, name), "the no-edit variable %s is a login variable of the profile", name)
		expect(r, slices.Contains(seen, name+"="+value), "the CLI of a step with no edits did not get the variable %s", name)
		expect(r, !hasPrefix(mayEdit, name+"="), "the CLI of a step that may edit got the no-edit variable %s", name)
	}
}

// CheckBudget starts the fake CLI with a cost cap of one dollar and checks that the profile keeps its word. A profile with BudgetFlag must pass the flag and the
// cap. A profile with StreamsCost and no flag must start, and the fake CLI reports no cost, so the run ends as OutcomeBudgetCap. A profile with neither must
// refuse the run. A profile with StreamsCost also needs costLine, a line that reports dollars, which Calls must read as Seen.Spent above 0.
func CheckBudget(r Reporter, adapter agent.Adapter, dir, costLine string) {
	r.Helper()

	profile := adapter.Profile()

	if !writeFake(r, profile, dir) {
		return
	}

	var out bytes.Buffer

	result := runFake(adapter, dir, &out, func(s *agent.Spec) { s.MaxBudget = 1 })
	checkCapped(r, profile, result, out.String())

	if profile.StreamsCost {
		checkCostLine(r, adapter, costLine)
	}
}

// checkCapped checks the outcome of a run with a cost cap of one dollar against the way that the profile caps cost.
func checkCapped(r Reporter, profile agent.Profile, result agent.Result, out string) {
	r.Helper()

	switch {
	case profile.BudgetFlag != "":
		args := strings.Join(slices.Concat(profile.Args, profile.EditArgs, []string{profile.BudgetFlag, "1"}), " ")
		expect(r, strings.Contains(out, "args="+args), "the CLI did not get the cost cap %q %q", profile.BudgetFlag, "1")
	case profile.StreamsCost:
		expect(r, result.Outcome == agent.OutcomeBudgetCap, "a capped run that reported no cost ended as %q, not as a budget cap", result.Outcome)
	default:
		expect(r, result.Outcome == agent.OutcomeNotStarted, "the profile has no way to cap the cost, but a capped run ended as %q", result.Outcome)
	}
}

// writeFake writes the fake CLI that the profile names into dir. It reports false, after a failure, when it cannot.
func writeFake(r Reporter, profile agent.Profile, dir string) bool {
	r.Helper()

	if err := exectest.Write(filepath.Join(dir, profile.Binary), fakeCLI); err != nil {
		r.Errorf("cannot write the fake CLI: %v", err)

		return false
	}

	return true
}

// checkCostLine checks that Calls reads dollars from the cost line, and that its other fields are the same for a second reader of the same line.
func checkCostLine(r Reporter, adapter agent.Adapter, line string) {
	r.Helper()

	if line == "" {
		r.Errorf("the profile sets StreamsCost, but the suite has no line that reports dollars: pass WithCostLine")

		return
	}

	first, second := adapter.Calls(line), adapter.Calls(line)

	expect(r, first.Spent > 0, "the line %q reports dollars, but Calls returns Spent %v", line, first.Spent)

	first.Spent, second.Spent = 0, 0
	expect(r, fmt.Sprintf("%+v", first) == fmt.Sprintf("%+v", second), "Calls returns different fields (besides Spent) for a second reader of the line %q", line)
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
