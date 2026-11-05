package cli_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/review"
)

// statusNow is the clock that the rig gives the status view.
var statusNow = time.Date(2026, 11, 3, 10, 30, 0, 0, time.UTC)

func waitingRun() flow.RunInfo {
	return flow.RunInfo{
		ID: "item-12-r1", State: "running", Item: "12", Project: "p", Question: "Ship it?",
		Asked:       &flow.Asked{From: []string{"ana", "bo", "cy"}, Quorum: 2, Choices: []string{"yes", "no"}, Heard: []string{"ana"}},
		RunProgress: flow.RunProgress{Deadline: statusNow.Add(90 * time.Minute)},
	}
}

func TestStatus_AWaitingRunShowsItsItemItsProjectAndTheTimeLeft(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{waitingRun()}

	code, stdout, stderr := rig.execute(t.Context(), "status")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, "item-12-r1\trunning\tpaused: Ship it?\tasks ana, bo, cy, 1 of 2 answered (ana); choices yes, no\t"+
		"answer by 2026-11-03T12:00:00Z, 1h30m left\titem 12 of p\n\nno engine runs\n", stdout)
}

func TestStatus_AWaitWithRemindersShowsTheNextOneInTextAndJSON(t *testing.T) {
	t.Parallel()

	run := waitingRun()
	run.Asked = &flow.Asked{From: []string{"ana"}, Quorum: 1, Reminders: 2, Reminded: 1, NextRemind: statusNow.Add(20 * time.Minute)}

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{run}

	_, stdout, _ := rig.execute(t.Context(), "status")
	assert.Contains(t, stdout, "asks ana, 0 of 1 answered; next reminder 2026-11-03T10:50:00Z (1 of 2 sent)")

	_, stdout, _ = rig.execute(t.Context(), "status", "--json")
	assert.Contains(t, stdout, `"reminders":2,"reminded":1,"next_remind":"2026-11-03T10:50:00Z"`)
}

func TestStatus_AWaitWhoseRemindersAreAllSentShowsNoNextReminder(t *testing.T) {
	t.Parallel()

	run := waitingRun()
	run.Asked = &flow.Asked{From: []string{"ana"}, Quorum: 1, Reminders: 2, Reminded: 2}

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{run}

	_, stdout, _ := rig.execute(t.Context(), "status")
	assert.NotContains(t, stdout, "reminder")
}

func TestStatus_ADeadlineThatPassedSaysSo(t *testing.T) {
	t.Parallel()

	run := waitingRun()
	run.Deadline = statusNow.Add(-time.Minute)

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{run}

	_, stdout, _ := rig.execute(t.Context(), "status")

	assert.Contains(t, stdout, "answer by 2026-11-03T10:29:00Z, deadline passed")
}

func TestStatus_ALastMinuteShowsSecondsAndALongWaitShowsDays(t *testing.T) {
	t.Parallel()

	short, minutes, long := waitingRun(), waitingRun(), waitingRun()
	short.Deadline = statusNow.Add(45 * time.Second)
	minutes.Deadline = statusNow.Add(12*time.Minute + 30*time.Second)
	long.Deadline = statusNow.Add(50 * time.Hour)

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{short, minutes, long}

	_, stdout, _ := rig.execute(t.Context(), "status")

	assert.Contains(t, stdout, ", 45s left")
	assert.Contains(t, stdout, ", 12m left")
	assert.Contains(t, stdout, ", 2d2h left")
}

func TestStatus_TheListWithJSONPrintsEveryRunWithItsWaitAndItsResult(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{
		waitingRun(),
		{ID: "item-13-r1", State: "stopped", Item: "13", Project: "p", Stopped: &flow.Stopped{Step: "check", RC: 2, Output: "refused"}},
		{ID: "item-14-r1", State: "done", Item: "14", Project: "q", Merge: "merged", SentBack: "redo"},
	}

	code, stdout, stderr := rig.execute(t.Context(), "status", "--json")
	require.Equal(t, 0, code, stderr)

	var list struct {
		Runs []runFields `json:"runs"`
	}

	require.NoError(t, json.Unmarshal([]byte(stdout), &list), stdout)
	require.Len(t, list.Runs, 3)

	got := list.Runs

	assert.Equal(t, "item-12-r1", got[0].ID)
	assert.Equal(t, "running", got[0].State)
	assert.Equal(t, "12", got[0].Item)
	assert.Equal(t, "p", got[0].Project)
	assert.Equal(t, "Ship it?", got[0].Question)
	assert.Equal(t, []string{"ana", "bo", "cy"}, got[0].Asked.From)
	assert.Equal(t, 2, got[0].Asked.Quorum)
	assert.Equal(t, []string{"ana"}, got[0].Asked.Heard)
	assert.Equal(t, []string{"yes", "no"}, got[0].Asked.Choices)
	assert.Equal(t, "2026-11-03T12:00:00Z", got[0].Deadline)
	require.NotNil(t, got[0].Left)
	assert.InDelta(t, 5400.0, *got[0].Left, 0)

	require.NotNil(t, got[1].Stopped)
	assert.Equal(t, "check", got[1].Stopped.Step)
	assert.Equal(t, 2, got[1].Stopped.RC)
	assert.Equal(t, "refused", got[1].Stopped.Output)
	assert.Nil(t, got[1].Left)

	assert.Equal(t, "merged", got[2].Merge)
	assert.Equal(t, "redo", got[2].SentBack)
}

// runFields is the part of one run of the JSON list that the test reads.
type runFields struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Item     string `json:"item"`
	Project  string `json:"project"`
	Question string `json:"question"`
	Asked    struct {
		From    []string `json:"from"`
		Quorum  int      `json:"quorum"`
		Choices []string `json:"choices"`
		Heard   []string `json:"heard"`
	} `json:"asked"`
	Deadline string   `json:"deadline"`
	Left     *float64 `json:"time_left_seconds"`
	Merge    string   `json:"merge"`
	SentBack string   `json:"sent_back"`
	Stopped  *struct {
		Step   string `json:"step"`
		RC     int    `json:"rc"`
		Output string `json:"output"`
	} `json:"stopped"`
}

func TestStatus_ARunWithAnItemAndNoProjectNamesTheItemAlone(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{{ID: "item-9-r1", State: "done", Item: "9"}}

	_, stdout, _ := rig.execute(t.Context(), "status")

	assert.Equal(t, "item-9-r1\tdone\titem 9\n\nno engine runs\n", stdout)
}

func TestStatus_ReadsTheSystemClockWhenTheBuildSetsNone(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	run := waitingRun()
	run.Deadline = time.Now().Add(48 * time.Hour)
	rig.steering.infos = []flow.RunInfo{run}

	factory := rig.factory()
	factory.Now = nil

	var stdout, stderr bytes.Buffer

	code := cli.Execute(t.Context(), []string{"status", "--config", rig.dir}, cli.BuildInfo{Version: "v9", Flow: factory}, rig.env, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.Regexp(t, `, 1d2[23]h left`, stdout.String())
}

func TestStatus_TheListWithJSONHasAnEmptyRunListAndNoSectionForNoRuns(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, stdout, stderr := rig.execute(t.Context(), "status", "--json")
	require.Equal(t, 0, code, stderr)

	assert.JSONEq(t, `{"runs":[]}`, stdout)
}

func TestStatus_TheScheduleShowsItsLastTickAndWhetherItFiresInTextAndJSON(t *testing.T) {
	t.Parallel()

	last := statusNow.Add(-20 * time.Minute)

	rig := newRig(t)
	rig.steering.schedules = []flow.ScheduleInfo{
		{Name: "review-poll", Workflow: "review_poll", Schedule: "@every 1h0m0s", LastTick: &last, Firing: true},
		{Name: "fresh", Workflow: "w", Schedule: "@every 5m"},
		{Name: "held", Workflow: "w", Schedule: "@every 5m", Paused: true},
	}

	code, stdout, stderr := rig.execute(t.Context(), "status")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, "no runs\n\nno engine runs\n\nschedules\n"+
		"review-poll\t@every 1h0m0s\tlast tick 2026-11-03T10:10:00Z\tfiring\n"+
		"fresh\t@every 5m\tno tick yet\tnot firing, no engine runs\n"+
		"held\t@every 5m\tno tick yet\tpaused\n", stdout)

	code, stdout, stderr = rig.execute(t.Context(), "status", "--json")
	require.Equal(t, 0, code, stderr)

	assert.JSONEq(t, `{"runs":[],"schedules":[`+
		`{"name":"review-poll","workflow":"review_poll","schedule":"@every 1h0m0s","firing":true,"last_tick":"2026-11-03T10:10:00Z"},`+
		`{"name":"fresh","workflow":"w","schedule":"@every 5m","firing":false},`+
		`{"name":"held","workflow":"w","schedule":"@every 5m","paused":true,"firing":false}]}`, stdout)
}

func TestStatus_AScheduleThatCannotBeReadFailsTheListInTextAndJSON(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.schedulesErr = errFake

	for _, args := range [][]string{{"status"}, {"status", "--json"}} {
		code, _, stderr := rig.execute(t.Context(), args...)

		assert.Equal(t, 1, code, args)
		assert.Contains(t, stderr, "fake failure", args)
	}
}

func TestStatus_ThePerDayFiguresAreInTheTextAndTheJSONList(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.days = []flow.DayFigures{
		{Day: "2026-11-02", Started: 2, Done: 1, Failed: 1, Spent: agent.Spend{Cost: 1.5, Estimated: true}},
		{Day: "2026-11-03", Started: 1, Canceled: 1},
	}

	code, stdout, stderr := rig.execute(t.Context(), "status")
	require.Equal(t, 0, code, stderr)

	assert.Equal(t, "no runs\n\nno engine runs\n\nper day\n"+
		"2026-11-02\tstarted 2\tdone 1, failed 1, canceled 0\tspent $1.5 (estimate)\n"+
		"2026-11-03\tstarted 1\tdone 0, failed 0, canceled 1\n", stdout)

	code, stdout, stderr = rig.execute(t.Context(), "status", "--json")
	require.Equal(t, 0, code, stderr)

	assert.JSONEq(t, `{"runs":[],"days":[`+
		`{"day":"2026-11-02","started":2,"done":1,"failed":1,"canceled":0,"spent":{"cost_usd":1.5,"cost_estimated":true}},`+
		`{"day":"2026-11-03","started":1,"done":0,"failed":0,"canceled":1,"spent":{}}]}`, stdout)
}

func TestStatus_TheDaysFlagSetsTheWindowFromTheLocalDayOfTheClock(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		args  []string
		since string
	}{
		{[]string{"status"}, "2026-10-28T00:00:00Z"},
		{[]string{"status", "--days", "1"}, "2026-11-03T00:00:00Z"},
		{[]string{"status", "--days", "30", "--json"}, "2026-10-05T00:00:00Z"},
	} {
		rig := newRig(t)

		code, _, stderr := rig.execute(t.Context(), test.args...)
		require.Equal(t, 0, code, stderr)

		assert.Contains(t, rig.steering.calls, "days "+test.since, test.args)
	}
}

func TestStatus_ADaysFlagBelowOneFailsBeforeAnyRead(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "status", "--days", "0")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "--days takes a number of days of 1 or more")
	assert.Empty(t, rig.steering.calls)
}

func TestStatus_ADaysReadThatFailsFailsTheList(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.daysErr = errFake

	code, _, stderr := rig.execute(t.Context(), "status")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")
}

func TestStatus_TheReviewPollSectionIsInTheJSONListAsInTheText(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.poll = &flow.PollOutput{At: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC), Projects: []flow.PollProject{
		{Project: "alpha", Rows: []review.Row{{Kind: review.KindCIRed, Number: 7, Failed: 2}}},
		{Project: "beta", Unread: "could not read beta"},
		{Project: "gamma", NoForge: true},
	}}

	code, stdout, stderr := rig.execute(t.Context(), "status", "--json")
	require.Equal(t, 0, code, stderr)

	var list struct {
		Poll struct {
			At       string `json:"at"`
			Projects []struct {
				Project string `json:"project"`
				Unread  string `json:"unread"`
				NoForge bool   `json:"no_forge"`
				Rows    []struct {
					Kind   string `json:"kind"`
					Number int    `json:"number"`
				} `json:"rows"`
			} `json:"projects"`
		} `json:"poll"`
	}

	require.NoError(t, json.Unmarshal([]byte(stdout), &list), stdout)
	assert.Equal(t, "2026-10-31T09:00:00Z", list.Poll.At)
	require.Len(t, list.Poll.Projects, 3)
	require.Len(t, list.Poll.Projects[0].Rows, 1)
	assert.Equal(t, 7, list.Poll.Projects[0].Rows[0].Number)
	assert.Equal(t, "could not read beta", list.Poll.Projects[1].Unread)
	assert.True(t, list.Poll.Projects[2].NoForge)
}

func TestStatus_AFailedReviewPollIsInTheJSONListWithItsCause(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.poll = &flow.PollOutput{At: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC), Projects: []flow.PollProject{}, Failure: "the store is locked"}

	code, stdout, stderr := rig.execute(t.Context(), "status", "--json")
	require.Equal(t, 0, code, stderr)

	assert.JSONEq(t, `{"runs":[],"poll":{"at":"2026-10-31T09:00:00Z","projects":[],"failure":"the store is locked"}}`, stdout)
}

func TestStatus_WhyThePollIsNotScheduledIsInTheJSONList(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.notSched = "the store\nis locked"

	code, stdout, stderr := rig.execute(t.Context(), "status", "--json")
	require.Equal(t, 0, code, stderr)

	assert.JSONEq(t, `{"runs":[],"poll_not_scheduled":"the store\nis locked"}`, stdout)
}

func TestStatus_AReviewPollThatCannotBeReadFailsTheJSONList(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.pollErr = errFake

	code, _, stderr := rig.execute(t.Context(), "status", "--json")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")

	rig.steering.pollErr, rig.steering.notSchedE = nil, errFake
	code, _, stderr = rig.execute(t.Context(), "status", "--json")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")
}

func TestStatus_TheListWithJSONReportsAReadError(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.err = errFake

	code, _, stderr := rig.execute(t.Context(), "status", "--json")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")
}

func TestStatus_TheListWithJSONKeepsFalseAndZeroApartFromAbsent(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.infos = []flow.RunInfo{{ID: "item-9-r1", State: "done", Item: "9"}}

	code, stdout, stderr := rig.execute(t.Context(), "status", "--json")
	require.Equal(t, 0, code, stderr)

	var list struct {
		Runs []map[string]any `json:"runs"`
	}

	require.NoError(t, json.Unmarshal([]byte(stdout), &list), stdout)
	require.Len(t, list.Runs, 1)

	got := list.Runs

	for key, want := range map[string]any{"stranded": false, "older_steps": false, "spent": map[string]any{}, "cap_usd": 0.0} {
		assert.Equal(t, want, got[0][key], key)
	}

	for _, key := range []string{"fix_rounds", "review_rounds"} {
		assert.Contains(t, got[0], key)
	}

	for _, key := range []string{"question", "asked", "stopped", "deadline", "time_left_seconds", "gate"} {
		assert.NotContains(t, got[0], key)
	}
}
