package cli

import (
	"encoding/json"
	"io"
	"time"

	"github.com/tobocop2/obee/internal/flow"
)

// runView is one run of the JSON list: the fields of the status line, and the seconds left to the deadline of an open wait.
// TimeLeft is nil for a run with no deadline. It is negative for a deadline that passed.
type runView struct {
	flow.RunInfo
	TimeLeft *float64 `json:"time_left_seconds,omitempty"`
}

// statusList is everything that the status list shows: the runs, the schedules, the newest review poll and why the poll is not scheduled.
// The text view and the JSON view both print this one value, so they cannot show different facts.
type statusList struct {
	Runs      []flow.RunInfo      `json:"-"`
	Schedules []flow.ScheduleInfo `json:"schedules,omitempty"`
	// Days is the per-day figures of the window. It is empty for a window with no run.
	Days []flow.DayFigures `json:"days,omitempty"`
	// Poll is nil when no review poll has finished.
	Poll *flow.PollOutput `json:"poll,omitempty"`
	// PollNotScheduled is the cause when the review poll does not run. It is empty when the poll is scheduled.
	PollNotScheduled string `json:"poll_not_scheduled,omitempty"`
}

// readStatusList reads the recorded state that the status list shows.
func readStatusList(client Steering, since time.Time) (statusList, error) {
	var (
		list statusList
		err  error
	)

	if list.Runs, err = client.Status(); err != nil {
		return statusList{}, err
	}

	if list.Schedules, err = client.Schedules(); err != nil {
		return statusList{}, err
	}

	if list.Days, err = client.Days(since); err != nil {
		return statusList{}, err
	}

	err = readPollSection(client, &list)

	return list, err
}

// readPollSection reads the newest review poll and the cause when it is not scheduled.
func readPollSection(client Steering, list *statusList) error {
	poll, found, err := client.Poll()
	if err != nil {
		return err
	}

	if found {
		list.Poll = &poll
	}

	list.PollNotScheduled, err = client.PollNotScheduled()

	return err
}

// listView is the JSON list: one object with the runs, then the sections that have content.
type listView struct {
	Runs []runView `json:"runs"`
	statusList
}

// printListJSON writes the list as one JSON object that a script reads. Every figure comes from the recorded state that the text list reads.
func printListJSON(stdout io.Writer, list statusList, now time.Time) error {
	views := make([]runView, 0, len(list.Runs))

	for _, info := range list.Runs {
		view := runView{RunInfo: info}

		if !info.Deadline.IsZero() {
			left := info.Deadline.Sub(now).Seconds()
			view.TimeLeft = &left
		}

		views = append(views, view)
	}

	return json.NewEncoder(stdout).Encode(listView{Runs: views, statusList: list})
}
