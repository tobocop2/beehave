package cli_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
)

var errFake = errors.New("fake failure")

const (
	stateDirVariable = "OBEE_STATE_DIR"
	validRunners     = "[[runner]]\nname = \"a\"\nagent = \"claude\"\n"
	validProject     = "repo = \"o/n\"\ntracker = \"github\"\nforge = \"github\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\ngate = \"make check\"\n"
)

type fakeRunner struct {
	launchErr, runErr error
	block             chan struct{}
	mu                sync.Mutex
	items             []string
	shutdowns         int
}

func (r *fakeRunner) Launch() error { return r.launchErr }

func (r *fakeRunner) Run(item, project string) (flow.ItemResult, error) {
	r.mu.Lock()
	r.items = append(r.items, item+"@"+project)
	r.mu.Unlock()

	if r.block != nil {
		<-r.block
	}

	return flow.ItemResult{Item: item, FixRounds: 2}, r.runErr
}

// Shutdown releases a blocked Run, as a real engine shutdown ends the run it holds.
func (r *fakeRunner) Shutdown() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.shutdowns++
	if r.block != nil {
		close(r.block)
	}

	return nil
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.shutdowns
}

type fakeSteering struct {
	infos   []flow.RunInfo
	err     error
	calls   []string
	closeOK bool
}

func (s *fakeSteering) Status() ([]flow.RunInfo, error) { return s.infos, s.err }

func (s *fakeSteering) Answer(item, text string) error {
	s.calls = append(s.calls, "answer "+item+" "+text)

	return s.err
}

func (s *fakeSteering) Resume(item string) error {
	s.calls = append(s.calls, "resume "+item)

	return s.err
}

func (s *fakeSteering) Cancel(item string) error {
	s.calls = append(s.calls, "cancel "+item)

	return s.err
}

func (s *fakeSteering) Close() error {
	s.closeOK = true

	return nil
}

type flowRig struct {
	runner   *fakeRunner
	steering *fakeSteering
	engineOf func() error
	clientOf func() error
	options  flow.Options
	config   config.Config
	dir      string
	env      map[string]string
}

func newRig(t *testing.T) *flowRig {
	t.Helper()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.toml", validRunners)
	writeConfig(t, dir, "projects/p.toml", validProject)

	return &flowRig{
		runner: &fakeRunner{}, steering: &fakeSteering{}, dir: dir,
		env: map[string]string{stateDirVariable: t.TempDir() + "/state"},
	}
}

func (rig *flowRig) factory() cli.Flow {
	return cli.Flow{
		Engine: func(_ context.Context, cfg config.Config, opts flow.Options) (cli.Runner, error) {
			rig.config, rig.options = cfg, opts
			if rig.engineOf != nil {
				return nil, rig.engineOf()
			}

			return rig.runner, nil
		},
		Client: func(_ context.Context, opts flow.Options) (cli.Steering, error) {
			rig.options = opts
			if rig.clientOf != nil {
				return nil, rig.clientOf()
			}

			return rig.steering, nil
		},
	}
}

func (rig *flowRig) execute(ctx context.Context, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer

	code := cli.Execute(ctx, append(args, "--config", rig.dir), cli.BuildInfo{Version: "v9", Flow: rig.factory()}, rig.env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestRun_TakesTheItemToTheEndAndPassesTheSettingsOfTheConfig(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished after 2 fix rounds\n", stdout)
	assert.Equal(t, []string{"12@p"}, rig.runner.items)
	assert.Equal(t, 1, rig.runner.count())
	assert.Equal(t, "v9", rig.options.Version)
	assert.Equal(t, 2, rig.options.MaxConcurrent)
	assert.Equal(t, rig.env[stateDirVariable], rig.options.StateDir)
	assert.Contains(t, rig.config.Projects, "p")
}

func TestRun_TheProjectIsNamedOrTheOnlyOneAndNeverGuessed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		extra     string // a second project file, when set
		args      []string
		wantItems []string
		wantErr   []string
	}{
		{"the_name_picks_one_of_two", "q", []string{"run", "12", "q"}, []string{"12@q"}, nil},
		{"one_project_may_be_omitted", "", []string{"run", "12"}, []string{"12@p"}, nil},
		{"two_projects_without_a_name_are_refused_and_listed", "q", []string{"run", "12"}, nil, []string{"2 projects", "p, q"}},
		{"an_unknown_name_is_refused_and_the_projects_listed", "", []string{"run", "12", "zzz"}, nil, []string{`no project "zzz"`, "use one of p"}},
		{"a_third_argument_is_refused", "", []string{"run", "12", "p", "x"}, nil, []string{"accepts between 1 and 2"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)

			if test.extra != "" {
				writeConfig(t, rig.dir, "projects/"+test.extra+".toml", validProject)
			}

			code, _, stderr := rig.execute(t.Context(), test.args...)

			if test.wantErr == nil {
				require.Equal(t, 0, code, stderr)
				assert.Equal(t, test.wantItems, rig.runner.items)

				return
			}

			assert.Equal(t, 1, code)
			assert.Empty(t, rig.runner.items)

			for _, want := range test.wantErr {
				assert.Contains(t, stderr, want)
			}
		})
	}
}

func TestRun_RefusesAConfigWithoutAProject(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.dir = t.TempDir()
	writeConfig(t, rig.dir, "runners.toml", validRunners)

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	assert.Equal(t, 1, code)
	assert.Empty(t, rig.runner.items)
	assert.Contains(t, stderr, "holds no project")
}

func TestRun_ReportsEachFailureAndStillShutsTheEngineDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		set    func(*flowRig)
		want   string
		wantSD int
	}{
		{"engine_cannot_open", func(r *flowRig) { r.engineOf = func() error { return errFake } }, "fake failure", 0},
		{"launch_fails", func(r *flowRig) { r.runner.launchErr = errFake }, "fake failure", 1},
		{"the_item_fails", func(r *flowRig) { r.runner.runErr = errFake }, "fake failure", 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)
			test.set(rig)

			code, _, stderr := rig.execute(t.Context(), "run", "12")

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr, test.want)
			assert.Equal(t, test.wantSD, rig.runner.count())
		})
	}
}

func TestRun_StopsOnInterruptAndTellsHowToContinue(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.runner.block = make(chan struct{})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	code, _, stderr := rig.execute(ctx, "run", "12")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "run obee run again to continue it")
	assert.Equal(t, 1, rig.runner.count())
}

func TestCommands_RefuseABadItemBeforeOpeningAnything(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"run", "resume", "cancel", "answer"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)
			rig.engineOf = func() error { return errFake }
			rig.clientOf = func() error { return errFake }

			args := []string{command, "#12"}
			if command == "answer" {
				args = append(args, "yes")
			}

			code, _, stderr := rig.execute(t.Context(), args...)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr, "use a forge number or a beads id")
			assert.NotContains(t, stderr, "fake failure")
		})
	}
}

func TestCommands_RefuseAMissingItem(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"run", "resume", "cancel", "answer"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			code, _, stderr := newRig(t).execute(t.Context(), command)

			assert.Equal(t, 1, code)
			assert.NotEmpty(t, stderr)
		})
	}
}

func TestCommands_RefuseAnUnusableConfigAndAnUnknownStateFolder(t *testing.T) {
	t.Parallel()

	for _, command := range [][]string{{"run", "12"}, {"status"}, {"resume", "12"}, {"cancel", "12"}, {"answer", "12", "yes"}} {
		t.Run(command[0], func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)
			writeConfig(t, rig.dir, "runners.toml", "[agents]\nmax_concurrent = 0\n"+validRunners)

			code, _, stderr := rig.execute(t.Context(), command...)

			assert.Equal(t, 2, code)
			assert.Contains(t, stderr, "agents.max_concurrent must be 1 or more")
		})

		t.Run(command[0]+"_without_a_state_folder", func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)
			rig.env = map[string]string{stateDirVariable: "relative/state"}

			code, _, stderr := rig.execute(t.Context(), command...)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr, "OBEE_STATE_DIR")
		})
	}
}

func TestStatus_ShowsEachRunWithItsQuestionOrItsVersion(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{
		{ID: "item-1-r1", State: "done"},
		{ID: "item-2-r1", State: "running", Question: "pick\n  option a or b"},
		{ID: "item-3-r1", State: "running", Version: "v1", Stranded: true},
	}

	code, stdout, stderr := rig.execute(t.Context(), "status")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item-1-r1\tdone\nitem-2-r1\trunning\tpaused: pick option a or b\n"+
		"item-3-r1\trunning\tstranded: start obee version v1 to finish it\n", stdout)
	assert.True(t, rig.steering.closeOK)
}

func TestStatus_SaysWhenThereAreNoRunsAndReportsAnError(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	_, stdout, _ := rig.execute(t.Context(), "status")
	assert.Equal(t, "no runs\n", stdout)

	rig.steering.err = errFake
	code, _, stderr := rig.execute(t.Context(), "status")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")

	rig.clientOf = func() error { return errFake }
	code, _, _ = rig.execute(t.Context(), "status")
	assert.Equal(t, 1, code)
}

func TestSteeringCommands_PassTheItemAndTheJoinedAnswerToTheClient(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	for _, args := range [][]string{{"answer", "12", "use", "option", "b"}, {"resume", "12"}, {"cancel", "12"}} {
		code, _, stderr := rig.execute(t.Context(), args...)
		require.Equal(t, 0, code, stderr)
	}

	assert.Equal(t, []string{"answer 12 use option b", "resume 12", "cancel 12"}, rig.steering.calls)
}

func TestFlowCommands_AreAbsentWithoutTheirFactories(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(t.Context(), []string{"status"}, cli.BuildInfo{Version: "x"}, map[string]string{}, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "unknown command")
}

func TestFlowCommands_FailWhenThereIsNoConfigFolderToFind(t *testing.T) {
	t.Setenv("HOME", "")

	var stdout, stderr bytes.Buffer

	code := cli.Execute(t.Context(), []string{"status"}, cli.BuildInfo{Version: "x", Flow: newRig(t).factory()}, map[string]string{}, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "cannot find the home folder")
}
