package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	igate "github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// gateCalls records every program that the gate starts, in order.
type gateCalls struct {
	binaries []string
	dirs     []string
	args     [][]string

	// status is what `git status` prints. An empty string is a clean tree.
	status string
}

// gateRunner returns a local runner that answers `git rev-parse` with head, `git status` with the status of calls, and any other program with gate.
func gateRunner(calls *gateCalls, head, gate model.CommandResult) func(string, string, time.Duration) proc.Runner {
	return func(binary, dir string, _ time.Duration) proc.Runner {
		return recording{run: func(args []string) model.CommandResult {
			calls.binaries = append(calls.binaries, binary)
			calls.dirs = append(calls.dirs, dir)
			calls.args = append(calls.args, args)

			if binary != "git" {
				return gate
			}

			if args[0] == "status" {
				return model.CommandResult{Stdout: calls.status}
			}

			if head.ExitCode != 0 {
				return head
			}

			// One `git rev-parse HEAD HEAD^{tree}` prints the head, then the tree.
			name := strings.TrimSpace(head.Stdout)

			return model.CommandResult{Stdout: name + "\ntree-of-" + name + "\n"}
		}}
	}
}

func TestProjectGate_TheHeadAndItsTreeAreReadByOneGitCall(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := projectGate{
		projects: localProject(),
		programs: programs{trees: treeAt("/tree"), local: gateRunner(calls, model.CommandResult{Stdout: "abc123\n"}, model.CommandResult{Stdout: "ok"})},
	}

	_, err := gate.Run(t.Context(), "p", "1")

	require.NoError(t, err)

	var reads [][]string

	for i, binary := range calls.binaries {
		if binary == "git" && calls.args[i][0] == "rev-parse" {
			reads = append(reads, calls.args[i])
		}
	}

	assert.Contains(t, reads, []string{"rev-parse", "HEAD", "HEAD^{tree}"})
	assert.Len(t, reads, 2, "one read before the gate and one after it, never a head and a tree apart")
}

func TestProjectGate_AnAnswerWithoutTheHeadAndTheTreeFailsTheGateBeforeItRuns(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	local := func(binary, _ string, _ time.Duration) proc.Runner {
		return recording{run: func(args []string) model.CommandResult {
			calls.binaries = append(calls.binaries, binary)

			return model.CommandResult{Stdout: "abc123\n"}
		}}
	}
	gate := projectGate{projects: localProject(), programs: programs{trees: treeAt("/tree"), local: local}}

	_, err := gate.Run(t.Context(), "p", "1")

	require.ErrorContains(t, err, "git rev-parse in /tree printed 1 lines, and the head and the tree make 2")
	assert.NotContains(t, calls.binaries, "sh")
}

type recording struct {
	run func(args []string) model.CommandResult
}

func (r recording) Run(_ context.Context, args []string) model.CommandResult { return r.run(args) }

// cloneForge is a forge that knows only the URL that its repository is cloned from.
type cloneForge struct {
	forge.Forge

	url string
}

func (c cloneForge) CloneURL() string { return c.url }

// forgeAt returns the forge lookup of a project whose repository is cloned from url.
func forgeAt(url string) func(model.Project) (forge.Forge, error) {
	return func(model.Project) (forge.Forge, error) { return cloneForge{url: url}, nil }
}

func localProject() map[string]config.Project {
	return map[string]config.Project{"p": {Project: model.Project{Gate: "make check", BuildHost: config.LocalHost}}}
}

func clusterProject() map[string]config.Project {
	return map[string]config.Project{"p": {Project: model.Project{Name: "p", Gate: "make check", BuildHost: "cluster", Host: &model.HostConfig{}}}}
}

func TestProjectGate_ALocalGateReportsAPassOnTheHeadOfTheWorktree(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := projectGate{
		projects: localProject(),
		programs: programs{trees: treeAt("/tree"), local: gateRunner(calls, model.CommandResult{Stdout: "abc123\n"}, model.CommandResult{Stdout: "ok"})},
	}

	got, err := gate.Run(t.Context(), "p", "1")

	require.NoError(t, err)
	assert.Equal(t, flow.GateResult{Passed: true, Report: "ok", Head: "abc123", Tree: "tree-of-abc123", Steps: []flow.GateStep{{Name: "command", Status: "pass"}}}, got)
	assert.Equal(t, []string{"git", "git", "sh", "git", "git"}, calls.binaries, "the tree is read before the gate runs and again after it")
	assert.Equal(t, []string{"-c", "make check"}, calls.args[2])
	assert.Equal(t, "/tree", calls.dirs[2])
}

func TestProjectGate_KeyNamesTheHeadAndTheTreeAndStartsNoGate(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := projectGate{
		projects: localProject(),
		programs: programs{trees: treeAt("/tree"), local: gateRunner(calls, model.CommandResult{Stdout: "abc123\n"}, model.CommandResult{Stdout: "ok"})},
	}

	got, err := gate.Key(t.Context(), "p", "1")

	require.NoError(t, err)
	assert.NotEmpty(t, got.ID)
	assert.Equal(t, "abc123", got.Head)
	assert.Equal(t, "tree-of-abc123", got.Tree)
	assert.NotContains(t, calls.binaries, "sh", "a key never runs the gate")
}

func TestProjectGate_KeyRefusesWhatItCannotReadWithoutRunningTheGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		project string
		head    model.CommandResult
		wantErr string
	}{
		{"unknown project", "zzz", model.CommandResult{Stdout: "abc"}, errUnknownProject.Error()},
		{"head error", "p", model.CommandResult{Stderr: "fatal: no repository\n", ExitCode: 128}, "read the gate key of item 1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			calls := &gateCalls{}
			gate := projectGate{projects: localProject(), programs: programs{trees: treeAt("/tree"), local: gateRunner(calls, test.head, model.CommandResult{})}}

			_, err := gate.Key(t.Context(), test.project, "1")

			require.ErrorContains(t, err, test.wantErr)
			assert.NotContains(t, calls.binaries, "sh")
		})
	}
}

func TestProjectGate_ATreeWithUncommittedChangesIsRefusedAndTheGateNeverRuns(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{status: " M main.go\n"}
	gate := projectGate{
		projects: localProject(),
		programs: programs{trees: treeAt("/tree"), local: gateRunner(calls, model.CommandResult{Stdout: "abc123\n"}, model.CommandResult{Stdout: "ok"})},
	}

	_, err := gate.Run(t.Context(), "p", "1")

	require.ErrorIs(t, err, igate.ErrTreeChanged)
	assert.NotContains(t, calls.binaries, "sh", "a dirty tree is never judged")
}

func TestProjectGate_ALocalGateThatFailsKeepsTheTailOfItsOutput(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", model.ReportLimit) + "the findings"
	calls := &gateCalls{}
	gate := projectGate{
		projects: localProject(),
		programs: programs{trees: treeAt("/tree"), local: gateRunner(calls, model.CommandResult{Stdout: "abc123"}, model.CommandResult{Stdout: long, Stderr: " and more", ExitCode: 2})},
	}

	got, err := gate.Run(t.Context(), "p", "1")

	require.NoError(t, err)
	assert.False(t, got.Passed)
	assert.Equal(t, "abc123", got.Head)
	assert.Len(t, got.Report, model.ReportLimit)
	assert.True(t, strings.HasSuffix(got.Report, "the findings and more"), "the tail holds the findings")
}

func TestProjectGate_ATreeThatCannotBeReadFailsTheGateBeforeItRuns(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	local := func(binary, _ string, _ time.Duration) proc.Runner {
		return recording{run: func(args []string) model.CommandResult {
			calls.binaries = append(calls.binaries, binary)

			if args[0] == "rev-parse" {
				return model.CommandResult{Stderr: "fatal: no tree\n", ExitCode: 128}
			}

			return model.CommandResult{Stdout: "abc123"}
		}}
	}
	gate := projectGate{projects: localProject(), programs: programs{trees: treeAt("/tree"), local: local}}

	_, err := gate.Run(t.Context(), "p", "1")

	require.ErrorContains(t, err, "git rev-parse in /tree: fatal: no tree")
	assert.NotContains(t, calls.binaries, "sh")
}

func TestProjectGate_AHostGateChecksOutTheHeadItReportsAndRunsNothingHere(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		state  model.JobState
		want   bool
		report string
	}{
		{"succeeded", model.JobSucceeded, true, ""},
		{"failed", model.JobFailed, false, "the gate job failed on cluster. Read its log on the host."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			hosted := &fakeHost{state: test.state}
			calls := &gateCalls{}
			gate := projectGate{
				projects: clusterProject(),
				programs: programs{
					trees: treeAt("/tree"),
					local: gateRunner(calls, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
					hosts: func(string) (host.Host, error) { return hosted, nil },
				},
				forge: forgeAt("https://example.test/repo.git"),
			}

			got, err := gate.Run(t.Context(), "p", "1")

			require.NoError(t, err)
			status := "fail"
			if test.want {
				status = "pass"
			}

			assert.Equal(t, flow.GateResult{Passed: test.want, Report: test.report, Head: "abc123", Tree: "tree-of-abc123", Steps: []flow.GateStep{{Name: "command", Status: status}}}, got)
			assert.NotContains(t, calls.binaries, "sh", "the gate runs on the host and never here")
			assert.Contains(t, hosted.spec.Command, "git clone -- https://example.test/repo.git tree")
			assert.Contains(t, hosted.spec.Command, "git checkout --detach abc123")
			assert.Contains(t, hosted.spec.Command, "sh -c 'make check'")
		})
	}
}

func TestProjectGate_TheCloneURLIsTheSourceOfTheProjectAndTheForgeIsNotAsked(t *testing.T) {
	t.Parallel()

	projects := clusterProject()
	found := projects["p"]
	found.Source = "https://example.test/shop.git"
	projects["p"] = found

	hosted := &fakeHost{state: model.JobSucceeded}
	gate := projectGate{
		projects: projects,
		programs: programs{
			trees: treeAt("/tree"),
			local: gateRunner(&gateCalls{}, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
			hosts: func(string) (host.Host, error) { return hosted, nil },
		},
		forge: func(model.Project) (forge.Forge, error) { return nil, errFake },
	}

	_, err := gate.Run(t.Context(), "p", "1")

	require.NoError(t, err)
	assert.Contains(t, hosted.spec.Command, "git clone -- https://example.test/shop.git tree")
	assert.Contains(t, hosted.spec.Command, "rm -rf /home/ob/obee-gate-p-abc123")
}

func TestProjectGate_AHostThatCannotRunTheJobEndsTheGateWithAnError(t *testing.T) {
	t.Parallel()

	gate := projectGate{
		projects: clusterProject(),
		programs: programs{
			trees: treeAt("/tree"),
			local: gateRunner(&gateCalls{}, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
			hosts: func(string) (host.Host, error) { return &fakeHost{state: model.JobCanceled}, nil },
		},
		forge: forgeAt("https://example.test/repo.git"),
	}

	_, err := gate.Run(t.Context(), "p", "1")

	require.ErrorIs(t, err, igate.ErrNoVerdict)
}

func TestProjectGate_RefusesWhatItCannotRunWithoutRunningTheGate(t *testing.T) {
	t.Parallel()

	failedForge := func(model.Project) (forge.Forge, error) { return nil, errFake }
	tests := []struct {
		name     string
		projects map[string]config.Project
		project  string
		trees    treeFinder
		head     model.CommandResult
		forge    func(model.Project) (forge.Forge, error)
		wantErr  string
	}{
		{"unknown project", localProject(), "zzz", treeAt("/tree"), model.CommandResult{Stdout: "abc"}, nil, errUnknownProject.Error()},
		{"tree error", localProject(), "p", treeFails{}, model.CommandResult{Stdout: "abc"}, nil, errFake.Error()},
		{"head error", localProject(), "p", treeAt("/tree"), model.CommandResult{Stderr: " fatal: no repository\n", ExitCode: 128}, nil, "git rev-parse in /tree: fatal: no repository"},
		{"forge error", clusterProject(), "p", treeAt("/tree"), model.CommandResult{Stdout: "abc"}, failedForge, errFake.Error()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			calls := &gateCalls{}
			gate := projectGate{projects: test.projects, programs: programs{trees: test.trees, local: gateRunner(calls, test.head, model.CommandResult{})}, forge: test.forge}

			_, err := gate.Run(t.Context(), test.project, "1")

			require.ErrorContains(t, err, test.wantErr)
			assert.NotContains(t, calls.binaries, "sh", "a gate that cannot start never runs")
		})
	}
}

func TestProjectGate_ALocalGateThatTheTimeoutKillsIsNotAnExitCode(t *testing.T) {
	t.Parallel()

	local := func(binary, dir string, timeout time.Duration) proc.Runner {
		return proc.ExecRunner{Binary: binary, Dir: dir, Timeout: timeout}
	}
	gate := projectGate{programs: programs{local: local}, timeout: 200 * time.Millisecond}

	started := time.Now()
	_, code := gate.shell(t.Context(), t.TempDir(), "echo partial; exec sleep 30")

	assert.Equal(t, igate.ExitTimedOut, code, "a real kill by the local runner")
	assert.Less(t, time.Since(started), 20*time.Second)

	_, own := gate.shell(t.Context(), t.TempDir(), "exit 124")
	assert.Equal(t, 124, own, "a command that exits 124 on its own is a failure, not a timeout")
}

func TestProjectGate_AStatusThatGitCannotReadEndsTheGate(t *testing.T) {
	t.Parallel()

	runner := func(binary, _ string, _ time.Duration) proc.Runner {
		return recording{run: func(args []string) model.CommandResult {
			if binary == "git" && args[0] == "status" {
				return model.CommandResult{Stderr: "fatal: not a repository\n", ExitCode: 128}
			}

			return model.CommandResult{Stdout: "abc\ntree-of-abc\n"}
		}}
	}
	gate := projectGate{projects: localProject(), programs: programs{trees: treeAt("/tree"), local: runner}}

	_, err := gate.Run(t.Context(), "p", "1")

	require.ErrorContains(t, err, "git status in /tree: fatal: not a repository")
}
