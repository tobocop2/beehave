package cli_test

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/review"
)

var errFake = errors.New("fake failure")

const (
	stateDirVariable = "BEEHAVE_STATE_DIR"
	validRunners     = "runner(name = \"a\", agent = \"claude\")\n"
	validProject     = "project(repo = \"o/n\", tracker = \"github\", forge = \"github\", claims = \"beads\", beads_prefix = \"p\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n"
)

type fakeRunner struct {
	launchErr, runErr error
	sentBack          string
	closed            string
	stopped           *flow.Stopped
	block             chan struct{}
	mu                sync.Mutex
	items             []string
	files             []string
	shutdowns         int
	// launched is closed by Launch. started is closed by the first Shutdown. slow holds Shutdown until it is closed.
	launched, started, slow chan struct{}
	// shutdownErr is what Shutdown returns.
	shutdownErr error
}

func (r *fakeRunner) Launch() error {
	if r.launched != nil {
		close(r.launched)
	}

	return r.launchErr
}

func (r *fakeRunner) RunFile(item, project, file string) (flow.ItemResult, error) {
	r.mu.Lock()
	r.items = append(r.items, item+"@"+project)
	r.files = append(r.files, item+"@"+project+" "+file)
	r.mu.Unlock()

	if r.block != nil {
		<-r.block
	}

	return flow.ItemResult{Item: item, SentBack: r.sentBack, Closed: r.closed, Stopped: r.stopped}, r.runErr
}

// Shutdown releases a blocked Run, as a real engine shutdown ends the run it holds.
func (r *fakeRunner) Shutdown() error {
	r.mu.Lock()

	r.shutdowns++
	if r.block != nil {
		close(r.block)
	}

	if r.started != nil && r.shutdowns == 1 {
		close(r.started)
	}

	r.mu.Unlock()

	if r.slow != nil {
		<-r.slow
	}

	return r.shutdownErr
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.shutdowns
}

type fakeSteering struct {
	poll      *flow.PollOutput
	pollErr   error
	notSched  string
	notSchedE error
	// schedules and schedulesErr are what Schedules returns.
	schedules    []flow.ScheduleInfo
	schedulesErr error
	// days and daysErr are what Days returns.
	days      []flow.DayFigures
	daysErr   error
	infos     []flow.RunInfo
	view      flow.RunSteps
	lanes     flow.ItemLanes
	err       error
	pinned    string
	pinnedErr error
	calls     []string
	closeOK   bool
	// answerErrs makes the answer of one item fail, whatever s.err holds.
	answerErrs map[string]error
	// running is what EngineRuns returns, and runningErr its error.
	running    bool
	runningErr error
	// engine is what Engine returns while running is true, and engineErr its error.
	engine    flow.EngineInfo
	engineErr error
	// joinResult and joinErr are what JoinRun returns. joined lists its calls.
	joinResult flow.ItemResult
	joinErr    error
	joined     []string
	// joinBlock holds JoinRun until the context ends.
	joinBlock bool
	// closeErr is what Close returns.
	closeErr error
}

func (s *fakeSteering) EngineRuns() (bool, error) { return s.running, s.runningErr }

func (s *fakeSteering) Engine() (flow.EngineInfo, bool, error) {
	if s.engineErr != nil {
		return flow.EngineInfo{}, false, s.engineErr
	}

	return s.engine, s.running, s.runningErr
}

func (s *fakeSteering) JoinRun(ctx context.Context, item, project, file string) (flow.ItemResult, error) {
	s.joined = append(s.joined, item+"@"+project+" "+file)

	if s.joinBlock {
		<-ctx.Done()

		return flow.ItemResult{}, ctx.Err()
	}

	result := s.joinResult
	result.Item = item

	return result, s.joinErr
}

func (s *fakeSteering) PinnedFile(string) (string, error) { return s.pinned, s.pinnedErr }

func (s *fakeSteering) Status() ([]flow.RunInfo, error) { return s.infos, s.err }

func (s *fakeSteering) Lanes(item string) (flow.ItemLanes, error) {
	s.calls = append(s.calls, "lanes "+item)

	return s.lanes, s.err
}

func (s *fakeSteering) Steps(id string) (flow.RunSteps, error) {
	s.calls = append(s.calls, "steps "+id)

	return s.view, s.err
}

func (s *fakeSteering) Poll() (flow.PollOutput, bool, error) {
	if s.poll == nil {
		return flow.PollOutput{}, false, s.pollErr
	}

	return *s.poll, true, s.pollErr
}

func (s *fakeSteering) PollNotScheduled() (string, error) { return s.notSched, s.notSchedE }

func (s *fakeSteering) Days(since time.Time) ([]flow.DayFigures, error) {
	s.calls = append(s.calls, "days "+since.Format(time.RFC3339))

	return s.days, s.daysErr
}

func (s *fakeSteering) Schedules() ([]flow.ScheduleInfo, error) {
	return s.schedules, s.schedulesErr
}

func (s *fakeSteering) Answer(item, text string) error {
	s.calls = append(s.calls, "answer "+item+" "+text)

	return cmp.Or(s.answerErrs[item], s.err)
}

func (s *fakeSteering) AnswerAs(item, from, text string) error {
	s.calls = append(s.calls, "answer-as "+from+" "+item+" "+text)

	return cmp.Or(s.answerErrs[item], s.err)
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

	return s.closeErr
}

type flowRig struct {
	runner   *fakeRunner
	steering *fakeSteering
	engineOf func() error
	clientOf func() error
	options  flow.Options
	// progress is the line that the fake engine writes to the terminal of the run.
	progress string
	config   config.Config
	dir      string
	engines  int // how many times the run command asked for an engine
	env      map[string]string
	// file is a valid workflow file. execute adds it to beehave run as --workflow when useFile is set and the arguments name none.
	file    string
	useFile bool
}

func newRig(t *testing.T) *flowRig {
	t.Helper()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.star", validRunners)
	writeConfig(t, dir, "projects/p.star", validProject)

	flowDir := t.TempDir()
	writeConfig(t, flowDir, "flow.star", oneCommandFile)

	return &flowRig{
		file: filepath.Join(flowDir, "flow.star"), useFile: true,
		runner: &fakeRunner{}, steering: &fakeSteering{}, dir: dir,
		env: map[string]string{stateDirVariable: t.TempDir() + "/state"},
	}
}

func (rig *flowRig) factory() cli.Flow {
	return cli.Flow{
		Now: func() time.Time { return statusNow },
		Engine: func(_ context.Context, cfg config.Config, opts flow.Options) (cli.Runner, error) {
			rig.config, rig.options = cfg, opts
			rig.engines++

			if rig.progress != "" && opts.Progress != nil {
				_, err := io.WriteString(opts.Progress, rig.progress)
				if err != nil {
					return nil, err
				}
			}

			if rig.engineOf != nil {
				return nil, rig.engineOf()
			}

			return rig.runner, nil
		},
		Client: func(_ context.Context, _ config.Config, opts flow.Options) (cli.Steering, error) {
			rig.options = opts
			if rig.clientOf != nil {
				if err := rig.clientOf(); err != nil {
					return nil, err
				}
			}

			return rig.steering, nil
		},
	}
}

func (rig *flowRig) execute(ctx context.Context, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer

	if len(args) > 0 && args[0] == "run" && !slices.Contains(args, "--workflow") && rig.useFile {
		args = append(args, "--workflow", rig.file)
	}

	code := cli.Execute(ctx, append(args, "--config", rig.dir), cli.BuildInfo{Version: "v9", Flow: rig.factory()}, rig.env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestRun_ASentBackItemSaysItWentBackToDraft(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.runner.sentBack = "a review asks for changes"

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 went back to draft: a review asks for changes\n", stdout)
}

func TestRun_AClosedItemSaysThePullRequestWasClosed(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.runner.closed = "the pull request\n  was closed"

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 ended: the pull request was closed\n", stdout)
}

func TestRun_AnItemThatAStepStoppedNamesTheStepTheCodeAndTheOutput(t *testing.T) {
	t.Parallel()

	for output, want := range map[string]string{
		"refused: no scope\n": "item 12 stopped at step check with exit code 2\nrefused: no scope\n",
		"refused":             "item 12 stopped at step check with exit code 2\nrefused\n",
		"":                    "item 12 stopped at step check with exit code 2\n",
	} {
		rig := newRig(t)
		rig.runner.stopped = &flow.Stopped{Step: "check", RC: 2, Output: output}

		code, stdout, stderr := rig.execute(t.Context(), "run", "12")

		require.Equal(t, 1, code, "a stop ends the run without done, so the exit code is not 0")
		assert.Empty(t, stderr)
		assert.Equal(t, want, stdout)
	}
}

func TestRun_TakesTheItemToTheEndAndPassesTheSettingsOfTheConfig(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item 12 finished\n", stdout)
	assert.Equal(t, []string{"12@p"}, rig.runner.items)
	assert.Equal(t, 1, rig.runner.count())
	assert.Equal(t, "v9", rig.options.Version)
	assert.Equal(t, 2, rig.options.MaxConcurrent)
	assert.Zero(t, rig.options.ReviewPoll, "the poll is off unless the config sets it")
	assert.Equal(t, rig.env[stateDirVariable], rig.options.StateDir)
	assert.Contains(t, rig.config.Projects, "p")
}

func TestRun_OverCapGoesToTheEngineAndIsOffByDefault(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	code, _, stderr := rig.execute(t.Context(), "run", "12")
	require.Equal(t, 0, code, stderr)
	assert.False(t, rig.options.OverCap)

	rig = newRig(t)
	code, _, stderr = rig.execute(t.Context(), "run", "12", "--over-cap")
	require.Equal(t, 0, code, stderr)
	assert.True(t, rig.options.OverCap)
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
				writeConfig(t, rig.dir, "projects/"+test.extra+".star", validProject)
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
	writeConfig(t, rig.dir, "runners.star", validRunners)

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
	assert.Contains(t, stderr, "run beehave run again to continue it")
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
			assert.Contains(t, stderr, "use an item id such as 12 or fix-login")
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
			writeConfig(t, rig.dir, "runners.star", "agents(max_concurrent = 0)\n"+validRunners)

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
			assert.Contains(t, stderr, "BEEHAVE_STATE_DIR")
		})
	}
}

func TestCommands_RefuseAnUnknownIssueStoreAtStartAndNameTheKnownOnes(t *testing.T) {
	t.Parallel()

	for _, command := range [][]string{{"run", "12"}, {"status"}, {"resume", "12"}, {"cancel", "12"}, {"answer", "12", "yes"}} {
		t.Run(command[0], func(t *testing.T) {
			t.Parallel()

			rig := newRig(t)
			writeConfig(t, rig.dir, "projects/p.star", "project(repo = \"o/n\", tracker = \"jira\", forge = \"github\", base_branch = \"main\", gate = \"g\")\n")

			code, _, stderr := rig.execute(t.Context(), command...)

			assert.Equal(t, 2, code)
			assert.Contains(t, stderr, `unknown tracker "jira": use one of local, beads, github`)
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
		{ID: "item-4-r1", State: "done", SentBack: "a review asks\nfor changes"},
		{ID: "item-5-r1", State: "running", Merge: "owner_merge"},
		{ID: "item-6-r1", State: "stopped", Stopped: &flow.Stopped{Step: "check", RC: 2, Output: "refused"}},
		{ID: "item-7-r1", State: "running", Version: "v1", Stranded: true, OlderSteps: true},
		{ID: "item-8-r1", State: "done", Closed: "the pull request was closed"},
		{ID: "item-9-r1", State: "running", Others: []string{"ann: 3,\n4", "bob: 7"}},
	}

	code, stdout, stderr := rig.execute(t.Context(), "status")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item-1-r1\tdone\nitem-2-r1\trunning\tpaused: pick option a or b\n"+
		"item-3-r1\trunning\tstranded: start beehave version v1 to finish it\n"+
		"item-4-r1\tdone\tsent back to draft: a review asks for changes\n"+
		"item-5-r1\trunning\tmerge: owner_merge\n"+
		"item-6-r1\tstopped\tstopped at check rc 2: refused\n"+
		"item-7-r1\trunning\tstranded: made by an older step format. Start the beehave build that made it to finish it\n"+
		"item-8-r1\tdone\tended: the pull request was closed\n"+
		"item-9-r1\trunning\tother orchestrators: ann: 3, 4; bob: 7\n\nno engine runs\n", stdout)
	assert.True(t, rig.steering.closeOK)
}

func TestStatus_ShowsTheCostTheStepTheGateAndTheRoundsOfEachRun(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{{
		ID: "a-r1", State: "running",
		RunProgress: flow.RunProgress{
			Spent: agent.Spend{Cost: 3.5}, Cap: 20, Now: "agent:fix#2", Gate: &flow.GateSeen{Passed: false, Head: "abc123"},
			Fix: flow.Rounds{Used: 2, Cap: 5}, Review: flow.Rounds{Used: 3, Cap: 6},
		},
	}, {
		ID: "b-r1", State: "running", Question: "go on?",
		RunProgress: flow.RunProgress{Deadline: statusNow.Add(90 * time.Minute)},
	}}

	code, stdout, stderr := rig.execute(t.Context(), "status")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "a-r1\trunning\tspent $3.5 of $20\tnow agent:fix#2\tgate fail at abc123\tfix 2 of 5\treview 3 of 6\n"+
		"b-r1\trunning\tpaused: go on?\tanswer by 2026-11-03T12:00:00Z, 1h30m left\n\nno engine runs\n", stdout)
}

func TestStatus_CutsLongStopOutputAndOmitsEmptyOutput(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{
		{ID: "a-r1", State: "stopped", Stopped: &flow.Stopped{Step: "check", RC: 3}},
		{ID: "b-r1", State: "stopped", Stopped: &flow.Stopped{Step: "check", RC: 3, Output: strings.Repeat("x", 200)}},
	}

	code, stdout, stderr := rig.execute(t.Context(), "status")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "a-r1\tstopped\tstopped at check rc 3\n"+
		"b-r1\tstopped\tstopped at check rc 3: "+strings.Repeat("x", 120)+"…\n\nno engine runs\n", stdout)
}

func TestStatus_SaysWhenThereAreNoRunsAndReportsAnError(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	_, stdout, _ := rig.execute(t.Context(), "status")
	assert.Equal(t, "no runs\n\nno engine runs\n", stdout)

	rig.steering.err = errFake
	code, _, stderr := rig.execute(t.Context(), "status")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")

	rig.clientOf = func() error { return errFake }
	code, _, _ = rig.execute(t.Context(), "status")
	assert.Equal(t, 1, code)
}

func TestStatus_ShowsTheNewestReviewPollInItsOwnSection(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.poll = &flow.PollOutput{At: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC), Projects: []flow.PollProject{
		{Project: "alpha", Rows: []review.Row{{Kind: review.KindCIRed, Number: 7, Failed: 2}}},
		{Project: "beta", Unread: "could not read beta: cannot list\nour pull requests"},
		{Project: "gamma"},
		{Project: "delta", NoForge: true},
	}}

	code, stdout, stderr := rig.execute(t.Context(), "status")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "no runs\n\nno engine runs\n\nreview poll at 2026-10-31T09:00:00Z\n"+
		"alpha\tCI-RED     #7 failed=2 not_started=0\n"+
		"beta\tcould not read beta: cannot list our pull requests\n"+
		"gamma\tnothing to review\n"+
		"delta: no forge, not polled\n", stdout)
}

func TestStatus_ShowsAReviewPollThatGaveUpAsAFailureWithItsStatus(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.poll = &flow.PollOutput{At: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC), Failure: "MAX_RECOVERY_ATTEMPTS_EXCEEDED"}

	code, stdout, stderr := rig.execute(t.Context(), "status")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "no runs\n\nno engine runs\n\nthe review poll failed at 2026-10-31T09:00:00Z: MAX_RECOVERY_ATTEMPTS_EXCEEDED\n", stdout)
}

func TestStatus_ShowsWhyTheReviewPollIsNotScheduled(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.notSched = "the store\nis locked"

	code, stdout, stderr := rig.execute(t.Context(), "status")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "no runs\n\nno engine runs\n\nthe review poll is not scheduled: the store is locked\n", stdout)

	rig.steering.notSched, rig.steering.notSchedE = "", errFake
	code, _, stderr = rig.execute(t.Context(), "status")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")
}

func TestStatus_ShowsAFailedReviewPollWithItsCause(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.poll = &flow.PollOutput{At: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC), Failure: "the store\nis locked"}

	code, stdout, stderr := rig.execute(t.Context(), "status")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "no runs\n\nno engine runs\n\nthe review poll failed at 2026-10-31T09:00:00Z: the store is locked\n", stdout)
}

func TestOptions_TheReviewPollMinutesBecomeTheIntervalOfTheEngine(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	writeConfig(t, rig.dir, "runners.star", "agents(review_poll_minutes = 45)\n"+validRunners)

	code, _, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, 45*time.Minute, rig.options.ReviewPoll)
}

func TestOptions_TheRetentionDaysBecomeTheAgeOfTheEngineAndZeroKeepsTheRuns(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "run", "12")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, 7*24*time.Hour, rig.options.Retention, "the default is the 7 days that the engine always used")
	assert.False(t, rig.options.KeepRuns)

	rig = newRig(t)
	writeConfig(t, rig.dir, "runners.star", "agents(retention_days = 30)\n"+validRunners)

	code, _, stderr = rig.execute(t.Context(), "run", "12")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, 30*24*time.Hour, rig.options.Retention)

	rig = newRig(t)
	writeConfig(t, rig.dir, "runners.star", "agents(retention_days = 0)\n"+validRunners)

	code, _, stderr = rig.execute(t.Context(), "run", "12")
	require.Equal(t, 0, code, stderr)
	assert.True(t, rig.options.KeepRuns)
}

func TestStatus_ReportsAReviewPollThatCannotBeRead(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.pollErr = errFake

	code, _, stderr := rig.execute(t.Context(), "status")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")
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

func TestAnswerCommand_TheAsFlagNamesThePersonWhoAnswers(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "answer", "12", "--as", "ana", "yes", "please")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, []string{"answer-as ana 12 yes please"}, rig.steering.calls)
}

func TestStatus_ShowsThePeopleTheChoicesAndWhoAnswered(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{{
		ID: "run-1", State: "running", Question: "Ship it?",
		Asked: &flow.Asked{From: []string{"ana", "bo", "cy"}, Quorum: 2, Choices: []string{"yes", "no\nway"}, Heard: []string{"ana"}},
	}}

	code, stdout, stderr := rig.execute(t.Context(), "status")
	require.Equal(t, 0, code, stderr)

	assert.Contains(t, stdout, "paused: Ship it?\tasks ana, bo, cy, 1 of 2 answered (ana); choices yes, no way")
}

func TestResumeCommand_TheOnThisBuildFlagMovesTheRunToThisBuild(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "resume", "12", "--on-this-build")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, []string{"resume-here 12"}, rig.steering.calls)
}

func (s *fakeSteering) ResumeOnThisBuild(item string) error {
	s.calls = append(s.calls, "resume-here "+item)

	return s.err
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
