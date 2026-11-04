package flow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

const pollMe = "me"

var pollTick = time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)

// fakeSource is a forge that the poll reads. It has no write method. Our one open pull request has two open threads, so the poll shows an OWN row for it.
type fakeSource struct {
	own    []model.OwnPull
	ownErr error
}

func (s fakeSource) PRList(context.Context, int, ...string) ([]model.PullRequest, error) {
	return nil, nil
}

func (s fakeSource) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return model.PullRequest{}, nil
}

func (s fakeSource) Reviews(context.Context, int) ([]model.Review, error) { return nil, nil }

func (s fakeSource) Comments(context.Context, int) ([]model.Comment, error) { return nil, nil }

func (s fakeSource) ReviewComments(context.Context, int) ([]model.Comment, error) { return nil, nil }

func (s fakeSource) Search(context.Context, model.SearchRequest) ([]int, error) { return nil, nil }

func (s fakeSource) PRChecks(context.Context, int) ([]model.Check, error) { return nil, nil }

func (s fakeSource) Title(context.Context, int) (string, error) { return "", nil }

func (s fakeSource) CheckNotStarted(context.Context, string) (bool, error) { return false, nil }

func (s fakeSource) OwnPulls(context.Context, string) ([]model.OwnPull, error) {
	return s.own, s.ownErr
}

// fakePoll serves the sources of the projects it names. A project it does not name has no forge.
type fakePoll struct {
	names   []string
	sources map[string]fakeSource
	failed  map[string]error
}

func (p fakePoll) Projects() []string { return p.names }

func (p fakePoll) Source(_ context.Context, project string) (PollSource, string, error) {
	if err := p.failed[project]; err != nil {
		return nil, "", err
	}

	source, ok := p.sources[project]
	if !ok {
		return nil, "", ErrNoPollSource
	}

	return source, pollMe, nil
}

func ownThreads(number int) []model.OwnPull {
	return []model.OwnPull{{Number: number, ChangesKnown: true, ThreadsKnown: true, OpenThreads: 2}}
}

// pollOnce runs one tick of the workflow in the engine and returns what it recorded.
func pollOnce(t *testing.T, engine *Engine) PollOutput {
	t.Helper()

	handle, err := dbos.RunWorkflow(engine.ctx, engine.poll, dbos.ScheduledWorkflowInput{ScheduledTime: pollTick})
	require.NoError(t, err)

	out, err := handle.GetResult()
	require.NoError(t, err)

	return out
}

func TestPoll_ATickRecordsTheRowsOfEachProjectAndSkipsAProjectWithNoForge(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.poll = fakePoll{names: []string{"alpha", "bare"}, sources: map[string]fakeSource{"alpha": {own: ownThreads(5)}}}

	out := pollOnce(t, launchedWith(t, harness, func(*Deps) {}))

	assert.Equal(t, pollTick, out.At)
	require.Len(t, out.Projects, 2)
	assert.Equal(t, "alpha", out.Projects[0].Project)
	assert.Empty(t, out.Projects[0].Unread)
	require.Len(t, out.Projects[0].Rows, 1)
	assert.Equal(t, review.KindOwn, out.Projects[0].Rows[0].Kind)
	assert.Equal(t, 5, out.Projects[0].Rows[0].Number)
	assert.Equal(t, PollProject{Project: "bare", NoForge: true}, out.Projects[1], "a project with no forge shows, so it never looks like nothing to do")
}

func TestPoll_TheProjectListIsRecordedInItsOwnFirstStep(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.poll = fakePoll{names: []string{"alpha", "bare"}, sources: map[string]fakeSource{"alpha": {own: ownThreads(5)}}}
	engine := launchedWith(t, harness, func(*Deps) {})

	handle, err := dbos.RunWorkflow(engine.ctx, engine.poll, dbos.ScheduledWorkflowInput{ScheduledTime: pollTick})
	require.NoError(t, err)
	_, err = handle.GetResult()
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, handle.GetWorkflowID())
	require.NoError(t, err)

	names := make([]string, 0, len(steps))
	for _, step := range steps {
		names = append(names, step.StepName)
	}

	assert.Equal(t, []string{"poll:projects", "poll:alpha", "poll:bare"}, names)
}

func TestPoll_AnErrorOfTheEngineFailsThePollAndIsNotAnUnreadProject(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engineErr := &dbos.Error{Code: dbos.ErrorCodeUnexpectedStep, Message: "step mismatch on replay"}
	harness.poll = fakePoll{names: []string{"alpha"}, failed: map[string]error{"alpha": engineErr}}
	engine := launchedWith(t, harness, func(*Deps) {})

	handle, err := dbos.RunWorkflow(engine.ctx, engine.poll, dbos.ScheduledWorkflowInput{ScheduledTime: pollTick})
	require.NoError(t, err)

	_, err = handle.GetResult()

	var got *dbos.Error
	require.ErrorAs(t, err, &got)
	assert.Equal(t, dbos.ErrorCodeUnexpectedStep, got.Code)
}

func TestPoll_AProjectThatCannotBeReadIsReportedAsUnreadAndTheOthersStillShow(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.poll = fakePoll{
		names: []string{"alpha", "beta", "gamma"},
		sources: map[string]fakeSource{
			"alpha": {own: ownThreads(5)},
			"beta":  {ownErr: errFake},
		},
		failed: map[string]error{"gamma": errFake},
	}

	out := pollOnce(t, launchedWith(t, harness, func(*Deps) {}))

	require.Len(t, out.Projects, 3)
	assert.Empty(t, out.Projects[0].Unread)
	assert.Contains(t, out.Projects[1].Unread, "could not read beta: ")
	assert.Contains(t, out.Projects[1].Unread, "cannot read our open pull requests")
	assert.Empty(t, out.Projects[1].Rows, "an unreadable project has no rows, so it never looks empty")
	assert.Equal(t, "could not read gamma: "+errFake.Error(), out.Projects[2].Unread)
}

func TestPoll_ABuildWithoutAReaderRecordsNoProject(t *testing.T) {
	t.Parallel()

	out := pollOnce(t, launchedWith(t, newHarness(t), func(*Deps) {}))

	assert.Equal(t, PollOutput{At: pollTick, Projects: []PollProject{}}, out)
}

func TestLaunch_TheIntervalSetsTheSchedule(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.poll = fakePoll{}
	harness.opts.ReviewPoll = 90 * time.Minute

	engine := launchedWith(t, harness, func(*Deps) {})

	schedule, err := dbos.GetSchedule(engine.ctx, pollScheduleName)
	require.NoError(t, err)
	assert.Equal(t, "@every 1h30m0s", schedule.Schedule)
	assert.Equal(t, pollName, schedule.WorkflowName)
}

func TestLaunch_AnIntervalOfZeroDeletesTheScheduleOfAnEarlierLaunch(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.poll = fakePoll{}
	harness.opts.ReviewPoll = time.Hour

	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())
	require.NoError(t, first.Shutdown())

	harness.opts.ReviewPoll = 0

	second := launchedWith(t, harness, func(*Deps) {})

	_, err = dbos.GetSchedule(second.ctx, pollScheduleName)
	require.ErrorIs(t, err, dbos.ErrScheduleNotFound)
}

func TestLaunch_ABuildWithoutAReaderSetsNoSchedule(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.ReviewPoll = time.Hour

	engine := launchedWith(t, harness, func(*Deps) {})

	_, err := dbos.GetSchedule(engine.ctx, pollScheduleName)
	require.ErrorIs(t, err, dbos.ErrScheduleNotFound)
}

func TestLaunch_ASchedulePollThatFailsDoesNotStopTheEngineAndTheClientSeesTheCause(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.ReviewPoll = time.Hour
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)

	engine.setSchedule = func(*Engine) error { return errFake }

	require.NoError(t, engine.Launch(), "the poll is optional, so the engine goes on")
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	cause, err := harness.client(t, testVersion).PollNotScheduled()
	require.NoError(t, err)
	assert.Equal(t, errFake.Error(), cause)
}

func TestSchedulePoll_AStoreThatCannotChangeSchedulesReturnsTheError(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	require.Error(t, engine.schedulePoll(), "the delete fails")

	engine.reviewPoll, engine.deps.Poll = time.Hour, fakePoll{}
	require.Error(t, engine.schedulePoll(), "the apply fails")
}

func TestClient_PollReturnsTheNewestFinishedPoll(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.poll = fakePoll{names: []string{"alpha"}, sources: map[string]fakeSource{"alpha": {own: ownThreads(5)}}}
	engine := launchedWith(t, harness, func(*Deps) {})
	client := harness.client(t, testVersion)

	_, found, err := client.Poll()
	require.NoError(t, err)
	assert.False(t, found, "no poll has finished")

	pollOnce(t, engine)
	harness.poll = fakePoll{names: []string{"alpha"}, sources: map[string]fakeSource{"alpha": {ownErr: errFake}}}
	engine.deps.Poll = harness.poll

	later := dbos.ScheduledWorkflowInput{ScheduledTime: pollTick.Add(time.Hour)}
	handle, err := dbos.RunWorkflow(engine.ctx, engine.poll, later)
	require.NoError(t, err)
	_, err = handle.GetResult()
	require.NoError(t, err)

	got, found, err := client.Poll()
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, later.ScheduledTime, got.At, "the newest poll shows, not the first")
	require.Len(t, got.Projects, 1)
	assert.Contains(t, got.Projects[0].Unread, "could not read alpha")
}

func TestClient_PollReportsAStoreThatIsGone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, _, err = client.Poll()
	require.ErrorContains(t, err, "list the review polls")
}

// oddPoll finishes with an output that is not the result of a poll.
func oddPoll(_ dbos.Context, _ dbos.ScheduledWorkflowInput) (string, error) { return "odd", nil }

func TestClient_PollReportsAResultThatIsNotAPoll(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	state, err := openStore(harness.opts)
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.log.Close() })

	flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: applicationVersion(testVersion), ExecutorID: executorName,
	})
	require.NoError(t, err)

	dbos.RegisterWorkflow(flowCtx, oddPoll, dbos.WithWorkflowName(pollName))
	require.NoError(t, dbos.Launch(flowCtx))

	handle, err := dbos.RunWorkflow(flowCtx, oddPoll, dbos.ScheduledWorkflowInput{ScheduledTime: pollTick})
	require.NoError(t, err)
	_, err = handle.GetResult()
	require.NoError(t, err)
	require.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout))

	_, _, err = harness.client(t, testVersion).Poll()
	require.ErrorContains(t, err, "read the result of the review poll")
}

// brokenPoll fails as a whole.
func brokenPoll(_ dbos.Context, _ dbos.ScheduledWorkflowInput) (PollOutput, error) {
	return PollOutput{}, errFake
}

func TestClient_PollShowsAFailedPollAsTheNewestRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	state, err := openStore(harness.opts)
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.log.Close() })

	flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: applicationVersion(testVersion), ExecutorID: executorName,
	})
	require.NoError(t, err)

	dbos.RegisterWorkflow(flowCtx, brokenPoll, dbos.WithWorkflowName(pollName))
	require.NoError(t, dbos.Launch(flowCtx))

	handle, err := dbos.RunWorkflow(flowCtx, brokenPoll, dbos.ScheduledWorkflowInput{ScheduledTime: pollTick})
	require.NoError(t, err)
	_, err = handle.GetResult()
	require.Error(t, err)
	require.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout))

	got, found, err := harness.client(t, testVersion).Poll()
	require.NoError(t, err)
	require.True(t, found)
	assert.Contains(t, got.Failure, "fake failure")
	assert.False(t, got.At.IsZero(), "the failure carries the time of the run")
}

// waitingPoll runs until DBOS cancels it.
func waitingPoll(ctx dbos.Context, _ dbos.ScheduledWorkflowInput) (PollOutput, error) {
	<-ctx.Done()

	return PollOutput{}, ctx.Err()
}

func TestClient_PollShowsACancelledPollAsAFailureWithItsStatusAndTime(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	state, err := openStore(harness.opts)
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.log.Close() })

	flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: applicationVersion(testVersion), ExecutorID: executorName,
	})
	require.NoError(t, err)

	dbos.RegisterWorkflow(flowCtx, waitingPoll, dbos.WithWorkflowName(pollName))
	require.NoError(t, dbos.Launch(flowCtx))

	handle, err := dbos.RunWorkflow(flowCtx, waitingPoll, dbos.ScheduledWorkflowInput{ScheduledTime: pollTick})
	require.NoError(t, err)
	require.NoError(t, dbos.CancelWorkflow(flowCtx, handle.GetWorkflowID()))
	require.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout))

	got, found, err := harness.client(t, testVersion).Poll()
	require.NoError(t, err)
	require.True(t, found, "a canceled poll is the newest result")
	assert.Contains(t, got.Failure, string(dbos.WorkflowStatusCancelled))
	assert.False(t, got.At.IsZero())
}

func TestPollOf_AnExceededPollShowsAsAFailureWithItsStatusAndTheTimeOfTheLastUpdate(t *testing.T) {
	t.Parallel()

	updated := pollTick.Add(time.Minute)
	got, found, err := pollOf(dbos.WorkflowStatus{Status: dbos.WorkflowStatusMaxRecoveryAttemptsExceeded, UpdatedAt: updated})

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, PollOutput{At: updated.UTC(), Projects: []PollProject{}, Failure: string(dbos.WorkflowStatusMaxRecoveryAttemptsExceeded)}, got)

	done := pollTick.Add(2 * time.Minute)
	got, _, err = pollOf(dbos.WorkflowStatus{Status: dbos.WorkflowStatusCancelled, CompletedAt: done, UpdatedAt: updated, Error: errFake})

	require.NoError(t, err)
	assert.Equal(t, done.UTC(), got.At, "the completion time wins when it is set")
	assert.Equal(t, string(dbos.WorkflowStatusCancelled)+": "+errFake.Error(), got.Failure)
}

func TestRecordSchedule_TheClientReadsWhyThePollIsNotScheduled(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.ReviewPoll = time.Hour
	engine := idleEngine(t, harness)
	client := harness.client(t, testVersion)

	cause, err := client.PollNotScheduled()
	require.NoError(t, err)
	assert.Empty(t, cause, "an engine that never launched left no record")

	engine.recordSchedule(errFake)

	cause, err = client.PollNotScheduled()
	require.NoError(t, err)
	assert.Equal(t, errFake.Error(), cause)

	engine.recordSchedule(nil)

	cause, err = client.PollNotScheduled()
	require.NoError(t, err)
	assert.Empty(t, cause, "a launch that scheduled the poll clears the cause")
}

func TestClient_PollNotScheduledSaysWhenTheIntervalIsZero(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.ReviewPoll = 0
	client := harness.client(t, testVersion)

	cause, err := client.PollNotScheduled()
	require.NoError(t, err)
	assert.Equal(t, pollOff, cause, "no launch ran")

	launchedWith(t, harness, func(*Deps) {})

	cause, err = client.PollNotScheduled()
	require.NoError(t, err)
	assert.Equal(t, pollOff, cause, "a launch with 0 deletes the schedule and records an empty file")

	require.NoError(t, os.WriteFile(pollScheduleFile(client.dir), []byte(errFake.Error()), 0o600))

	cause, err = client.PollNotScheduled()
	require.NoError(t, err)
	assert.Equal(t, pollOff+" "+errFake.Error(), cause, "a recorded error is added to the text")
}

func TestClient_PollNotScheduledIsEmptyWhenTheIntervalIsSetAndNoErrorIsRecorded(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.ReviewPoll = time.Hour
	client := harness.client(t, testVersion)

	cause, err := client.PollNotScheduled()
	require.NoError(t, err)
	assert.Empty(t, cause)

	launchedWith(t, harness, func(*Deps) {})

	cause, err = client.PollNotScheduled()
	require.NoError(t, err)
	assert.Empty(t, cause, "a launch that scheduled the poll leaves an empty record")
}

func TestRecordSchedule_AFolderThatCannotBeWrittenIsLoggedAndNothingElse(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	engine.dir = filepath.Join(harness.opts.StateDir, "no-such-folder")

	assert.NotPanics(t, func() { engine.recordSchedule(errFake) })
}

func TestClient_PollNotScheduledReportsARecordThatCannotBeRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)
	require.NoError(t, os.Mkdir(pollScheduleFile(client.dir), 0o700))

	_, err := client.PollNotScheduled()
	require.Error(t, err)
}
