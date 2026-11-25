package flow

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

const sleepFlow = "def main(item):\n    s = sleep(\"300ms\", name = \"nap\")\n    fail(\"SLEPT %s\" % s.slept)\n"

func TestSleep_ReturnsAfterTheDurationWithTheSecondsSlept(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, sleepFlow)

	started := time.Now()
	outcome := receive(t, runInBackground(h.engine(t), testItem))

	require.ErrorContains(t, outcome.err, "SLEPT 0.")
	assert.GreaterOrEqual(t, time.Since(started), 300*time.Millisecond, "the run slept the duration")
}

func TestSleep_ARunRecordsTheMarkerThenTheLibraryWakeTimeThenTheStep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, sleepFlow)
	engine := h.engine(t)

	receive(t, runInBackground(engine, testItem))

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var names []string

	for _, step := range steps {
		if strings.HasPrefix(step.StepName, "sleep") || step.StepName == sleepStep {
			names = append(names, step.StepName)
		}
	}

	assert.Equal(t, []string{"sleep:nap#1/start", sleepStep, "sleep:nap#1"}, names, "one marker, one record of the library, no second copy of the wake time")
}

func TestSleep_RefusesADurationThatIsNotAboveZero(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]struct{ call, want string }{
		"zero":         {`sleep("0s", name = "nap")`, "must be above zero"},
		"below_zero":   {`sleep("-5s", name = "nap")`, "must be above zero"},
		"not_a_time":   {`sleep("soon", name = "nap")`, "not a duration"},
		"no_duration":  {`sleep(name = "nap")`, "duration"},
		"empty_string": {`sleep("", name = "nap")`, "must be above zero"},
		"a_number":     {`sleep(5, name = "nap")`, "duration"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.flowIs(t, "def main(item):\n    "+call.call+"\n")

			outcome := receive(t, runInBackground(h.engine(t), testItem))

			require.ErrorContains(t, outcome.err, call.want)
		})
	}
}

// A run is stopped in the middle of a sleep and continues later: the second execution sleeps only the time that the first left.
func TestSleep_AResumeSleepsOnlyTheTimeLeft(t *testing.T) {
	t.Parallel()

	const (
		length = 6 * time.Second
		gone   = 3 * time.Second
	)

	h := newHarness(t)
	file := writeFlow(t, "def main(item):\n    sleep(\""+strconv.Itoa(int(length.Seconds()))+"s\", name = \"nap\")\n    fail(\"WOKE\")\n")

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	ended := make(chan error, 1)

	go func() {
		_, runErr := first.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	require.Eventually(t, func() bool {
		steps, stepErr := dbos.GetWorkflowSteps(first.ctx, testRun)

		return stepErr == nil && hasStep(steps, sleepStep)
	}, 3*time.Second, 20*time.Millisecond, "the library records the wake time of the sleep within seconds")

	started := time.Now()

	stop()
	require.NoError(t, first.Shutdown())
	require.Error(t, receive(t, ended))
	time.Sleep(gone)

	next := h.engine(t)
	resumed := time.Now()
	_, runErr := next.RunFile(testItem, testProject, file, modeOff())

	require.ErrorContains(t, runErr, "WOKE")
	assert.Less(t, time.Since(resumed), length-gone/2, "the resumed run slept the rest, not the full duration")
	assert.GreaterOrEqual(t, time.Since(started), length-time.Second, "the run did not wake before its recorded time")
}

func TestStatus_ARunInASleepShowsWhenItWakes(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    sleep(\"8s\", name = \"nap\")\n")
	engine := h.engine(t)
	client := h.client(t, testVersion)

	before := time.Now()
	done := runInBackground(engine, testItem)
	read := newStatusRead(client)

	waitOn(t, waiting[struct{}]{what: "the open run to show that it sleeps", run: runOfClient(t, client), last: read.last, read: func() (struct{}, bool, error) {
		infos := read.next()

		return struct{}{}, read.err == nil && len(infos) == 1 && infos[0].Sleeping, nil
	}})

	info := onlyRun(t, client)
	assert.WithinDuration(t, before.Add(8*time.Second), info.Deadline, 3*time.Second)
	assert.Contains(t, info.Notes(), "sleeps until "+info.Deadline.Format(time.RFC3339))
	assert.NotContains(t, info.Notes(), "answer by "+info.Deadline.Format(time.RFC3339))

	view, err := client.Steps(info.ID)
	require.NoError(t, err)
	assert.True(t, view.Sleeping, "the view of the steps of the run shows the sleep too")

	require.NoError(t, receive(t, done).err)
}

func TestSleepDeadline_ReadsTheWakeRecordOfTheSleepThatIsLast(t *testing.T) {
	t.Parallel()

	wake := time.UnixMilli(1_900_000_000_000)
	record := func(name string, out any) dbos.StepInfo { return dbos.StepInfo{StepName: name, Output: out} }

	for name, tc := range map[string]struct {
		records []dbos.StepInfo
		want    time.Time
	}{
		"sleeping":                            {[]dbos.StepInfo{record("sleep:nap#1/start", struct{}{}), record(sleepStep, "1900000000000")}, wake},
		"a_number_output":                     {[]dbos.StepInfo{record("sleep#2/start", nil), record(sleepStep, int64(1_900_000_000_000))}, wake},
		"before_the_library_records_the_wake": {[]dbos.StepInfo{record("sleep:nap#1/start", nil)}, time.Time{}},
		"the_sleep_ended":                     {[]dbos.StepInfo{record("sleep:nap#1/start", nil), record(sleepStep, "1900000000000"), record("sleep:nap#1", "{}")}, time.Time{}},
		"a_sleep_of_the_engine":               {[]dbos.StepInfo{record("agent:work#1", "{}"), record(sleepStep, "1900000000000")}, time.Time{}},
		"a_wake_that_is_no_time":              {[]dbos.StepInfo{record("sleep:nap#1/start", nil), record(sleepStep, "soon")}, time.Time{}},
		"a_marker_of_another_step_kind":       {[]dbos.StepInfo{record("wait:wake#1/start", nil), record(sleepStep, "1900000000000")}, time.Time{}},
		"a_sleep_step_that_is_no_marker":      {[]dbos.StepInfo{record("sleep:nap#1", "{}"), record(sleepStep, "1900000000000")}, time.Time{}},
		"no_record":                           {nil, time.Time{}},
		"only_a_sleep_record":                 {[]dbos.StepInfo{record(sleepStep, "1900000000000")}, time.Time{}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, sleepDeadline(tc.records))
		})
	}
}

func TestDeadlineNote_ASleepSaysSleepsUntil(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	progress := RunProgress{Deadline: now.Add(time.Hour), Sleeping: true}

	assert.Equal(t, []string{"sleeps until 2026-11-03T11:00:00Z, 1h0m left"}, progress.NotesAt(now))
}

func TestSleep_ACancelEndsTheRunInTheMiddleOfTheSleep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    sleep(\"1h\", name = \"nap\")\n")
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := runInBackground(engine, testItem)
	read := newStatusRead(client)

	waitOn(t, waiting[struct{}]{what: "the open run to show that it sleeps", run: runOfClient(t, client), last: read.last, read: func() (struct{}, bool, error) {
		infos := read.next()

		return struct{}{}, read.err == nil && len(infos) == 1 && infos[0].Sleeping, nil
	}})

	require.NoError(t, client.Cancel(testItem))
	h.tick(t)
	require.Error(t, receive(t, done).err, "the sleep of an hour ends at the cancel")
}

func TestSleep_AStoreThatRefusesTheWakeRecordEndsTheCall(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	ctx, stop := dbos.WithCancel(engine.ctx)
	stop()

	s := &script{e: engine, ctx: ctx, counts: map[string]int{}}
	_, err := s.sleep(starlark.NewBuiltin("sleep", nil), starlark.Tuple{starlark.String("1s")}, nil)

	require.Error(t, err)
}

func TestSleep_TheRunRefusesWhatTheCheckRefuses(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	s := &script{e: engine, ctx: engine.ctx, counts: map[string]int{}}
	call := starlark.NewBuiltin("sleep", nil)

	for name, tc := range map[string]struct {
		args   starlark.Tuple
		kwargs []starlark.Tuple
		want   string
	}{
		"a_number":    {starlark.Tuple{starlark.MakeInt(5)}, nil, "duration"},
		"zero":        {starlark.Tuple{starlark.String("0s")}, nil, "must be above zero"},
		"no_duration": {nil, nil, "duration"},
		"unknown":     {starlark.Tuple{starlark.String("1s")}, []starlark.Tuple{{starlark.String("until"), starlark.String("x")}}, "until"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := s.sleep(call, tc.args, tc.kwargs)

			require.ErrorContains(t, err, tc.want)
		})
	}
}
