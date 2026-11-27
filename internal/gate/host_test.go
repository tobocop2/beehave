package gate_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/gate"
	"github.com/tobocop2/beehest/internal/model"
)

const (
	clone = defaultRoot + "/beehest-gate-demo-a1b2c3"
	sweep = `( for bh_d in ` + defaultRoot + `/beehest-gate-*/; do bh_d=${bh_d%/}; [ -d "$bh_d" ] && [ ! -L "$bh_d" ] && [ -f "$bh_d/.beehest-job" ] || continue; ` +
		`( flock -n 8 && rm -rf -- "$bh_d" ) 8<"$bh_d"; done; : )`
	hostCommand = sweep + ` && trap 'echo beehest-gate-done; rm -rf ` + clone + `' EXIT && trap 'exit 143' HUP INT TERM && rm -rf ` + clone + ` && mkdir ` + clone +
		` && exec 8<` + clone + ` && flock -n 8 && : > ` + clone + `/.beehest-job && cd ` + clone +
		` && git clone -- https://github.com/owner/name.git tree && cd tree && ` +
		`git checkout --detach a1b2c3 && [ "$(git rev-parse HEAD)" = a1b2c3 ] && sh -c 'make check'`
)

func hostProject() model.Project {
	return model.Project{Name: "demo", Repo: "owner/name", Gate: "make check", BuildHost: "gpu-a"}
}

func TestRun_ThePathToAHostGateChecksOutTheSentCommitInAFreshFolderBeforeTheGate(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(hostProject()))
	require.NoError(t, err)
	assert.Equal(t, []model.JobSpec{{Name: "gate-demo", Command: hostCommand, Folder: clone}}, remote.submitted)
}

func TestRun_EveryWordOfTheHostCommandIsQuoted(t *testing.T) {
	t.Parallel()

	project := hostProject()
	project.Gate = "make 'check'; rm x"
	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(project))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.Contains(t, remote.submitted[0].Command, `sh -c 'make '\''check'\''; rm x'`)
}

func TestRun_TheGateCommandOnAHostRunsUnderTheRunLimitOfTheHost(t *testing.T) {
	t.Parallel()

	project := hostProject()
	project.Host = &model.HostConfig{MaxRun: 90 * time.Minute}
	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(project))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.Contains(t, remote.submitted[0].Command, `[ "$(git rev-parse HEAD)" = a1b2c3 ] && timeout 5400 sh -c 'make check'`)
}

func TestRun_AHostRunLimitWithAPartOfASecondIsRoundedUpInTheWrap(t *testing.T) {
	t.Parallel()

	project := hostProject()
	project.Host = &model.HostConfig{MaxRun: 1900 * time.Millisecond}
	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(project))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.Contains(t, remote.submitted[0].Command, "&& timeout 2 sh -c 'make check'")
}

func TestRun_AProjectWithNoHostBlockGetsAGateCommandWithNoTimeout(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(hostProject()))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.NotContains(t, remote.submitted[0].Command, "timeout")
}

func TestRun_AWorkRootWithASpaceAndADollarIsQuotedEverywhereThePathIsUsed(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded, root: "/home/o b$x"}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(hostProject()))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)

	path := `'/home/o b$x/beehest-gate-demo-a1b2c3'`
	assert.Contains(t, remote.submitted[0].Command, `trap 'echo beehest-gate-done; rm -rf '\''/home/o b$x/beehest-gate-demo-a1b2c3'\''' EXIT`)
	assert.Contains(t, remote.submitted[0].Command, ` && rm -rf `+path+` && mkdir `+path+` && exec 8<`+path+` && flock -n 8 && : > `+path+`/.beehest-job && cd `+path+` && git clone`)
	assert.Contains(t, remote.submitted[0].Command, `for bh_d in '/home/o b$x'/beehest-gate-*/; do`)
}

func TestRun_ACloneFolderOutsideTheWorkRootIsRefusedAndNoJobStarts(t *testing.T) {
	t.Parallel()

	for name, change := range map[string]func(*fakeRemote, *model.Project, *gate.State){
		"relative root":      func(r *fakeRemote, _ *model.Project, _ *gate.State) { r.root = "home/ob" },
		"root with dots":     func(r *fakeRemote, _ *model.Project, _ *gate.State) { r.root = "/home/../etc" },
		"root of the disk":   func(r *fakeRemote, _ *model.Project, _ *gate.State) { r.root = "/" },
		"project with slash": func(_ *fakeRemote, p *model.Project, _ *gate.State) { p.Name = "../x" },
		"project with inner": func(_ *fakeRemote, p *model.Project, _ *gate.State) { p.Name = "a/b" },
		"head with slash":    func(_ *fakeRemote, _ *model.Project, s *gate.State) { s.Head = "a/b" },
		"empty project":      func(_ *fakeRemote, p *model.Project, _ *gate.State) { p.Name = "" },
		"dot project":        func(_ *fakeRemote, p *model.Project, _ *gate.State) { p.Name = ".." },
		"head with space":    func(_ *fakeRemote, _ *model.Project, s *gate.State) { s.Head = "a b" },
	} {
		remote, project, state := &fakeRemote{state: model.JobSucceeded}, hostProject(), clean(headA)
		change(remote, &project, &state)

		_, err := build(&fakeShell{}, &fakeTree{states: []gate.State{state}}, remote).Run(context.Background(), hostRequest(project))
		require.ErrorIs(t, err, gate.ErrCloneOutsideRoot, name)
		assert.Empty(t, remote.submitted, name)
	}
}

func TestRun_AWorkRootThatTheHostCannotReadStartsNoJob(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{rootErr: errors.New("ssh down")}

	_, err := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote).Run(context.Background(), hostRequest(hostProject()))
	require.ErrorContains(t, err, "read the work root of gpu-a: ssh down")
	assert.Empty(t, remote.submitted)
}

// gitShim is a git that the test steers by environment: the clone makes the folder, the checkout and the head come from the variables.
const gitShim = `#!/bin/sh
case "$1" in
clone) [ -z "$SHIM_CLONE_FAILS" ] || exit 128; mkdir "$4" ;;
checkout) [ -z "$SHIM_CHECKOUT_FAILS" ] || exit 1 ;;
rev-parse) echo "$SHIM_HEAD" ;;
esac
`

// runGateCommand runs the command that the gate sends to the host for a gate command, with the shim as git.
// It returns the exit code, the login folder that the command ran under, and whether the gate command ran.
func runGateCommand(t *testing.T, gateCommand string, env ...string) (int, string, bool) {
	t.Helper()

	code, home, ran, _ := runGateCommandWithOutput(t, gateCommand, env...)

	return code, home, ran
}

// runGateCommandWithOutput is runGateCommand that also returns the output of the command.
func runGateCommandWithOutput(t *testing.T, gateCommand string, env ...string) (int, string, bool, string) {
	t.Helper()

	project := hostProject()
	project.Gate = gateCommand

	return runProjectCommand(t, project, env...)
}

// runProjectCommand runs the command that the gate sends to the host for project, with the shim as git.
// It reads the output from a file, as the host job log is a file: a pipe would wait on any descendant that a killed step leaves behind.
func runProjectCommand(t *testing.T, project model.Project, env ...string) (int, string, bool, string) {
	t.Helper()

	home, bin := t.TempDir(), t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), gitShim))

	remote := &fakeRemote{state: model.JobSucceeded, root: home}
	_, err := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote).Run(context.Background(), hostRequest(project))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)

	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", remote.submitted[0].Command) //nolint:gosec // the command is the one the code under test composes, run against a shim git
	dirs := []string{bin, "/usr/bin", "/bin"}

	for _, tool := range []string{"timeout", "flock"} {
		if found, lookErr := exec.LookPath(tool); lookErr == nil {
			dirs = append(dirs, filepath.Dir(found)) // a machine without the tool in /usr/bin (macOS) has it elsewhere
		}
	}

	cmd.Env = append([]string{"HOME=" + home, "PATH=" + strings.Join(dirs, ":")}, env...)

	logPath := filepath.Join(t.TempDir(), "job.log")
	logFile, err := os.Create(logPath) //nolint:gosec // a path in the test's own temporary folder
	require.NoError(t, err)

	cmd.Stdout = logFile
	code := 0

	err = cmd.Run()
	require.NoError(t, logFile.Close())

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else {
		require.NoError(t, err)
	}

	out, err := os.ReadFile(logPath) //nolint:gosec // the file created above
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(home, "gate-ran"))

	return code, home, statErr == nil, string(out)
}

// runHostCommand runs the command that the gate sends to the host and reports whether it exited 0 and whether the gate command ran.
func runHostCommand(t *testing.T, env ...string) (bool, bool) {
	t.Helper()

	code, _, ran := runGateCommand(t, `touch "$HOME/gate-ran"`, env...)

	return code == 0, ran
}

func TestHostCommand_TheCloneFolderIsRemovedWhenTheShellExitsAndAFailingGateKeepsItsStatus(t *testing.T) {
	t.Parallel()

	for gateCommand, want := range map[string]int{"exit 0": 0, "exit 3": 3} {
		code, home, _ := runGateCommand(t, gateCommand, "SHIM_HEAD="+headA)
		assert.Equal(t, want, code, gateCommand)

		left, err := filepath.Glob(filepath.Join(home, "beehest-gate-*"))
		require.NoError(t, err)
		assert.Empty(t, left, gateCommand)
	}
}

func TestHostCommand_TheMarkerLineIsWrittenBeforeTheCloneIsDeletedWhateverTheGateResult(t *testing.T) {
	t.Parallel()

	for _, gateCommand := range []string{"exit 0", "exit 3", `kill -TERM $PPID; sleep 1`} {
		code, home, _, out := runGateCommandWithOutput(t, gateCommand, "SHIM_HEAD="+headA)
		assert.Contains(t, out, "beehest-gate-done\n", gateCommand)

		left, err := filepath.Glob(filepath.Join(home, "beehest-gate-*"))
		require.NoError(t, err)
		assert.Empty(t, left, gateCommand)

		if gateCommand == "exit 3" {
			assert.Equal(t, 3, code)
		}
	}
}

func TestHostCommand_AHungGateEndsWithExitStatus124UnderTheRunLimitAndTheCloneIsStillDeleted(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("no timeout binary on this machine")
	}

	project := hostProject()
	project.Host = &model.HostConfig{MaxRun: time.Second}
	project.Gate = "sleep 30; true" // two commands, so the shell forks sleep as a child; timeout signals the whole process group, so no child keeps the output open

	start := time.Now()
	code, home, _, out := runProjectCommand(t, project, "SHIM_HEAD="+headA)

	assert.Equal(t, 124, code, "timeout ends a hung gate with status 124 and the trap keeps it")
	assert.Less(t, time.Since(start), 20*time.Second, "the run limit ended the gate, not the sleep")
	assert.Contains(t, out, "beehest-gate-done\n")

	left, err := filepath.Glob(filepath.Join(home, "beehest-gate-*"))
	require.NoError(t, err)
	assert.Empty(t, left)
}

func TestHostCommand_TheMarkerIsInTheCommandBeforeTheDelete(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(hostProject()))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)

	command := remote.submitted[0].Command
	require.Contains(t, command, "echo beehest-gate-done")
	require.Contains(t, command, "echo beehest-gate-done; rm -rf")
	assert.Equal(t, strings.Index(command, "echo beehest-gate-done"), strings.Index(command, "echo beehest-gate-done; rm -rf"))
}

func TestHostCommand_AFailedCloneAlsoRemovesTheFolder(t *testing.T) {
	t.Parallel()

	code, home, _ := runGateCommand(t, "exit 0", "SHIM_HEAD="+headA, "SHIM_CLONE_FAILS=1")
	assert.NotZero(t, code)

	left, err := filepath.Glob(filepath.Join(home, "beehest-gate-*"))
	require.NoError(t, err)
	assert.Empty(t, left)
}

func TestHostCommand_ARealShellFailsAGateThatPassedWithNoTestCaseInItsReportAndPassesOneWithATest(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		report   string
		wantCode bool
		wantMark bool
	}{
		"a report with a test case": {report: `<testsuite><testcase name="a"/></testsuite>`, wantCode: true},
		"a report with no test":     {report: `<testsuite tests="0"></testsuite>`, wantMark: true},
	} {
		project := hostProject()
		project.TestReport = "reports/*.xml"
		project.Gate = "mkdir reports && echo '" + test.report + "' > reports/a.xml"

		code, _, _, out := runProjectCommand(t, project, "SHIM_HEAD="+headA)

		assert.Equal(t, test.wantCode, code == 0, name)
		assert.Equal(t, test.wantMark, strings.Contains(out, "beehest-gate-no-tests"), name)
	}
}

func TestHostCommand_PassesWhenTheCheckedOutHeadIsTheSentCommit(t *testing.T) {
	t.Parallel()

	passed, ran := runHostCommand(t, "SHIM_HEAD="+headA)
	assert.True(t, passed)
	assert.True(t, ran)
}

func TestHostCommand_ACloneThatFailsIsAFailedGateAndTheGateDoesNotRun(t *testing.T) {
	t.Parallel()

	passed, ran := runHostCommand(t, "SHIM_HEAD="+headA, "SHIM_CLONE_FAILS=1")
	assert.False(t, passed)
	assert.False(t, ran)
}

func TestHostCommand_ACommitThatIsNotPushedFailsTheCheckoutAndTheGateDoesNotRun(t *testing.T) {
	t.Parallel()

	passed, ran := runHostCommand(t, "SHIM_HEAD="+headA, "SHIM_CHECKOUT_FAILS=1")
	assert.False(t, passed)
	assert.False(t, ran)
}

func TestHostCommand_AHeadThatIsNotTheSentCommitFailsAndTheGateDoesNotRun(t *testing.T) {
	t.Parallel()

	passed, ran := runHostCommand(t, "SHIM_HEAD=ffffff")
	assert.False(t, passed)
	assert.False(t, ran)
}
