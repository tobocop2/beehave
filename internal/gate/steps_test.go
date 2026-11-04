package gate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
)

var (
	passed = []gate.Step{{Name: gate.StepCommand, Status: gate.StatusPass}}
	failed = []gate.Step{{Name: gate.StepCommand, Status: gate.StatusFail}}
)

func step(name string, status gate.Status) gate.Step { return gate.Step{Name: name, Status: status} }

func TestSteps_AGateWithNoTestReportGivesOneStepFromItsExitCode(t *testing.T) {
	t.Parallel()

	for code, want := range map[int]gate.Status{0: gate.StatusPass, 1: gate.StatusFail} {
		result, err := runWithReports(t, code, "", nil)

		require.NoError(t, err)
		assert.Equal(t, []gate.Step{step(gate.StepCommand, want)}, result.Steps)
	}
}

func TestSteps_AReportWithTestsGivesAPassForTheCommandAndForTheTests(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/a.xml": rootSuite})

	require.NoError(t, err)
	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusPass), step(gate.StepTests, gate.StatusPass)}, result.Steps)
	assert.True(t, result.Passed)
}

func TestSteps_AGateThatExitsZeroWithNoResultLineIsUnknownAndNeverAPass(t *testing.T) {
	t.Parallel()

	for name, files := range map[string]map[string]string{
		"no report file":               nil,
		"a report with zero":           {"r/a.xml": zeroTests},
		"a report that does not parse": {"r/a.xml": badXML},
	} {
		result, err := runWithReports(t, 0, "r/*.xml", files)

		require.NoError(t, err, name)
		assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusPass), step(gate.StepTests, gate.StatusUnknown)}, result.Steps, name)
		assert.False(t, result.Passed, name)
	}
}

func TestSteps_AGateThatExitsZeroWithAFailedTestCaseFailsTheTestsStepAndNamesTheCount(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"a failure element":                `<testsuite tests="2"><testcase/><testcase><failure/></testcase></testsuite>`,
		"an error element":                 `<testsuite tests="2"><testcase/><testcase><error/></testcase></testsuite>`,
		"a failures attribute":             `<testsuites><testsuite tests="3" failures="1"><testcase/></testsuite></testsuites>`,
		"an errors attribute":              `<testsuite tests="3" errors="1"/>`,
		"a failure inside a nested suite":  `<testsuites><testsuite tests="1"><testsuite tests="1"><testcase><failure/></testcase></testsuite></testsuite></testsuites>`,
		"a failed case with the attribute": `<testsuite tests="1" failures="1"><testcase><failure/></testcase></testsuite>`,
	} {
		result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/a.xml": content})

		require.NoError(t, err, name)
		assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusPass), step(gate.StepTests, gate.StatusFail)}, result.Steps, name)
		assert.False(t, result.Passed, name)
		assert.Contains(t, result.Report, "1 failed", name)
	}
}

func TestSteps_AReportThatCountsNoTestsButHoldsAFailureFailsTheTestsStep(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"a suite with tests=0 and a failures attribute":       `<testsuite tests="0" failures="1"/>`,
		"a suite with tests=0 and an errors attribute":        `<testsuites><testsuite tests="0" errors="2"/></testsuites>`,
		"a testsuites root with a failures attribute only":    `<testsuites tests="3" failures="1"><testsuite tests="3"><testcase/></testsuite></testsuites>`,
		"a testsuites root with an errors attribute only":     `<testsuites tests="3" errors="1"><testsuite tests="3"/></testsuites>`,
		"a testsuites root that counts no tests and failures": `<testsuites tests="0" failures="1"/>`,
	} {
		result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/a.xml": content})

		require.NoError(t, err, name)
		assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusPass), step(gate.StepTests, gate.StatusFail)}, result.Steps, name)
		assert.False(t, result.Passed, name)
	}
}

func TestSteps_AReportWithFailuresAndAnotherWithNoneCountsTheFailuresOnce(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 0, "r/*.xml", map[string]string{
		"r/a.xml": `<testsuite tests="2" failures="2"><testcase><failure/></testcase><testcase><failure/></testcase></testsuite>`,
		"r/b.xml": rootSuite,
	})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "2 failed")
}

func TestSteps_AFailedCommandLeavesTheTestsStepUnknownBecauseTheReportIsNotRead(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 1, "r/*.xml", map[string]string{"r/a.xml": rootSuite})

	require.NoError(t, err)
	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusFail), step(gate.StepTests, gate.StatusUnknown)}, result.Steps)
	assert.False(t, result.Passed)
}

func TestSteps_AHostJobGivesOneStepFromItsState(t *testing.T) {
	t.Parallel()

	for state, want := range map[model.JobState]gate.Status{model.JobSucceeded: gate.StatusPass, model.JobFailed: gate.StatusFail} {
		g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{state: state})

		result, err := g.Run(context.Background(), hostRequest(project("cluster")))

		require.NoError(t, err)
		assert.Equal(t, []gate.Step{step(gate.StepCommand, want)}, result.Steps)
	}
}

func withReport(project model.Project) model.Project {
	project.TestReport = "reports/*.xml"

	return project
}

func TestSteps_AHostGateWithATestReportGivesTheTestsStepFromTheJobState(t *testing.T) {
	t.Parallel()

	want := map[model.JobState][]gate.Step{
		model.JobSucceeded: {step(gate.StepCommand, gate.StatusPass), step(gate.StepTests, gate.StatusPass)},
		model.JobFailed:    {step(gate.StepCommand, gate.StatusFail), step(gate.StepTests, gate.StatusUnknown)},
	}

	for state, steps := range want {
		g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{state: state})

		result, err := g.Run(context.Background(), hostRequest(withReport(project("cluster"))))

		require.NoError(t, err)
		assert.Equal(t, steps, result.Steps, state)
		assert.Equal(t, state == model.JobSucceeded, result.Passed, state)
	}
}

func TestHostCommand_FailsTheJobWhenTheReportGlobMatchesNoTestCase(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(withReport(hostProject())))

	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.True(t, strings.HasSuffix(remote.submitted[0].Command, ` && sh -c 'make check' && { [ "$(cat reports/*.xml 2>/dev/null | grep -c '<testcase')" -gt 0 ] || { echo obee-gate-no-tests; false; }; }`), remote.submitted[0].Command)
}

func TestHostCommand_QuotesTheLiteralPartsOfTheReportGlobAndKeepsTheWildcards(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)
	project := hostProject()
	project.TestReport = "my out/$x/[ab]?.xml"

	_, err := g.Run(context.Background(), hostRequest(project))

	require.NoError(t, err)
	assert.Contains(t, remote.submitted[0].Command, `cat 'my out/$x/'[ab]?.xml 2>/dev/null`)
}

func TestSteps_AGateThatOurTimeoutKilledIsUnresolvedNotRed(t *testing.T) {
	t.Parallel()

	for _, report := range []string{"", "r/*.xml"} {
		shell := &fakeShell{output: "partial\n", code: gate.ExitTimedOut}
		g := build(shell, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{})
		project := project("local")
		project.TestReport = report

		result, err := g.Run(context.Background(), gate.Request{Project: project, Dir: "/w"})

		require.NoError(t, err)
		assert.False(t, result.Passed)
		assert.Contains(t, result.Report, "ran past its time limit")
		assert.Contains(t, result.Report, "partial")

		for _, s := range result.Steps {
			assert.Equal(t, gate.StatusUnknown, s.Status, s.Name)
		}

		assert.Len(t, result.Steps, map[string]int{"": 1, "r/*.xml": 2}[report])
	}
}

func TestSteps_AGateKilledByASignalGivesEveryStepUnknownAndNoError(t *testing.T) {
	t.Parallel()

	for _, code := range []int{128 + 9, 128 + 15} {
		for _, report := range []string{"", "r/*.xml"} {
			shell := &fakeShell{output: "partial output\n", code: code}
			g := build(shell, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{})
			project := project("local")
			project.TestReport = report

			result, err := g.Run(context.Background(), gate.Request{Project: project, Dir: "/w"})

			require.NoError(t, err, "exit %d", code)
			assert.False(t, result.Passed)
			assert.Equal(t, headA, result.Head)
			assert.Contains(t, result.Report, "killed by a signal")
			assert.Contains(t, result.Report, "partial output")
			assert.Equal(t, allUnknown(report != ""), result.Steps)
		}
	}
}

func TestSteps_AHostJobThatDiedWentStaleOrTimedOutGivesEveryStepUnknownAndNoError(t *testing.T) {
	t.Parallel()

	cases := map[model.JobState]string{
		model.JobDied:     "the scheduler holds no record of it",
		model.JobStale:    "raise `host.stale_minutes`",
		model.JobTimedOut: "Raise `host.max_run`",
	}

	for state, advice := range cases {
		for _, report := range []string{"", "r/*.xml"} {
			g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{state: state})
			project := hostProject()
			project.TestReport = report

			result, err := g.Run(context.Background(), hostRequest(project))

			require.NoError(t, err, string(state))
			assert.False(t, result.Passed, string(state))
			assert.Equal(t, headA, result.Head, string(state))
			assert.Contains(t, result.Report, advice, string(state))
			assert.Equal(t, allUnknown(report != ""), result.Steps, string(state))
		}
	}
}

func TestSteps_AStoppedRunIsAnErrorAndNeverAVoidGate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	local := build(&fakeShell{output: "partial\n", code: 128 + 15}, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{})
	_, err := local.Run(ctx, gate.Request{Project: project("local"), Dir: "/w"})
	require.ErrorIs(t, err, context.Canceled, "a shell killed by the stop of the run")

	for _, state := range []model.JobState{model.JobDied, model.JobStale, model.JobTimedOut} {
		g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{state: state})

		_, err = g.Run(ctx, hostRequest(hostProject()))
		require.ErrorIs(t, err, context.Canceled, string(state))
	}
}

func TestSteps_AResumedHostJobThatDiedGivesEveryStepUnknownForTheHeadItWasSubmittedWith(t *testing.T) {
	t.Parallel()

	for _, state := range []model.JobState{model.JobDied, model.JobStale, model.JobTimedOut} {
		g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{state: state})
		request := hostRequest(hostProject())
		request.Resume, request.ResumeHead = "job-3", headB

		result, err := g.Run(context.Background(), request)

		require.NoError(t, err, string(state))
		assert.False(t, result.Passed, string(state))
		assert.Equal(t, headB, result.Head, string(state))
		assert.Equal(t, allUnknown(false), result.Steps, string(state))
	}
}

// allUnknown is the steps of a gate that gave no result: the command, and the tests when the project sets test_report.
func allUnknown(withTests bool) []gate.Step {
	steps := []gate.Step{step(gate.StepCommand, gate.StatusUnknown)}
	if withTests {
		steps = append(steps, step(gate.StepTests, gate.StatusUnknown))
	}

	return steps
}
