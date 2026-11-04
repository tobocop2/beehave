package flow

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
)

// dayLayout is the form of a day in the per-day figures.
const dayLayout = "2006-01-02"

// DayFigures is one local day of the status view: the runs that started, the runs that ended by outcome, and the spend of those runs.
// A run counts as started on the day of its start and as ended on the day of its end, so a run that spans midnight counts on both days.
// Its spend counts once, on the day it ended, or on the day it started while it is open.
type DayFigures struct {
	Day      string      `json:"day"`
	Started  int         `json:"started"`
	Done     int         `json:"done"`
	Failed   int         `json:"failed"`
	Canceled int         `json:"canceled"`
	Spent    agent.Spend `json:"spent"`
}

// DayRun is what the per-day figures need of one run.
type DayRun struct {
	// Started is the creation time of the run. Ended is the zero time for an open run.
	Started, Ended time.Time
	State          RunState
	Spent          agent.Spend
}

// DaysSince returns the start of the local day that is days - 1 days before the day of now. The window of the per-day figures starts there.
func DaysSince(now time.Time, days int) time.Time {
	year, month, day := now.Date()

	return time.Date(year, month, day-(days-1), 0, 0, 0, 0, now.Location())
}

// TallyDays sums the runs by local day, in the time zone of since, oldest day first. A day with no run is left out.
// A start before since is not counted: the window shows only the days from since on.
func TallyDays(runs []DayRun, since time.Time) []DayFigures {
	t := dayTally{since: since, byDay: map[string]*DayFigures{}}

	for _, r := range runs {
		t.add(r)
	}

	days := make([]DayFigures, 0, len(t.byDay))
	for _, day := range t.byDay {
		days = append(days, *day)
	}

	sort.Slice(days, func(i, j int) bool { return days[i].Day < days[j].Day })

	return days
}

// dayTally holds the days that the runs so far touch, keyed by the local day.
type dayTally struct {
	since time.Time
	byDay map[string]*DayFigures
}

// figures returns the figures of the local day of a time, and makes them when no run touched the day yet.
func (t dayTally) figures(at time.Time) *DayFigures {
	day := at.In(t.since.Location()).Format(dayLayout)
	if t.byDay[day] == nil {
		t.byDay[day] = &DayFigures{Day: day}
	}

	return t.byDay[day]
}

// add counts one run: its start, its end by outcome and its spend, each only when its day is in the window.
func (t dayTally) add(r DayRun) {
	spendDay := r.Started

	if !r.Started.Before(t.since) {
		t.figures(r.Started).Started++
	}

	if !r.Ended.IsZero() && !r.Ended.Before(t.since) {
		t.figures(r.Ended).take(r.State)
		spendDay = r.Ended
	}

	if !spendDay.Before(t.since) {
		day := t.figures(spendDay)
		day.Spent = day.Spent.Plus(r.Spent)
	}
}

// take counts one ended run by its outcome.
func (d *DayFigures) take(state RunState) {
	switch state {
	case runDone:
		d.Done++
	case runFailed:
		d.Failed++
	case runCancelled:
		d.Canceled++
	case runRunning, runQueued, runStopped:
		// An open run has no outcome. A stopped run is a done run until its result is read, so no row has this state here.
	}
}

// Text is the line of the day: the day, the runs that started, the outcomes and the spend with its marks. A day that spent nothing has no spend part.
func (d DayFigures) Text() string {
	parts := []string{d.Day, fmt.Sprintf("started %d", d.Started), fmt.Sprintf("done %d, failed %d, canceled %d", d.Done, d.Failed, d.Canceled)}

	return strings.Join(append(parts, RunProgress{Spent: d.Spent}.Notes()...), "\t")
}

// Days reads the runs that started or ended since the given time and sums them by local day. It reads only that window of the store, and the spend
// of each run from its step records, the same figure that the status line of the run shows.
func (c *Client) Days(since time.Time) ([]DayFigures, error) {
	rows, err := c.windowRows(since)
	if err != nil {
		return nil, err
	}

	runs := make([]DayRun, 0, len(rows))

	var readErr error

	for _, row := range rows {
		run, err := c.dayRun(row)
		readErr = errors.Join(readErr, err)
		runs = append(runs, run)
	}

	// A caller that gets an error ignores the figures.
	return TallyDays(runs, since), readErr
}

// windowRows lists the runs that started since the time, and the runs that ended since it, once each.
func (c *Client) windowRows(since time.Time) (map[string]dbos.WorkflowStatus, error) {
	rows := map[string]dbos.WorkflowStatus{}

	for _, window := range []dbos.ListWorkflowsOption{dbos.WithFilterCreatedAfter(since), dbos.WithFilterCompletedAfter(since)} {
		found, err := dbos.ListWorkflows(c.flow, dbos.WithFilterWorkflowIDPrefix(runPrefix), dbos.WithFilterName(itemName), window)
		if err != nil {
			return nil, fmt.Errorf("list the runs of the window: %w", err)
		}

		for _, row := range found {
			rows[row.ID] = row
		}
	}

	return rows, nil
}

// dayRun reads the times, the outcome and the spend of one run.
func (c *Client) dayRun(row dbos.WorkflowStatus) (DayRun, error) {
	state := stateOf(row.Status)
	open := !run{Status: row}.terminal()

	records, err := c.records(row.ID)
	if err != nil {
		return DayRun{}, err
	}

	out := DayRun{Started: row.CreatedAt, State: state}
	out.Spent = progressOf(stepLines(records, state, open, time.Now()), 0, open).Spent

	if !open {
		out.Ended = endOf(row)
	}

	return out, nil
}
