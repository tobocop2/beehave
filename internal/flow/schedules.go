package flow

import (
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// ScheduleInfo is one scheduled workflow of the store, as obee status shows it.
// LastTick is nil when the schedule has not fired. It is the time that the workflow library recorded after the last firing, which can be a few seconds after the tick.
// Firing is true when an engine runs and the schedule is not paused: only a running engine fires a schedule.
// The next due time is not shown. The workflow library counts a fixed delay from the launch of the engine, and obee records no launch time.
type ScheduleInfo struct {
	Name     string     `json:"name"`
	Workflow string     `json:"workflow"`
	Schedule string     `json:"schedule"`
	Paused   bool       `json:"paused,omitempty"`
	Firing   bool       `json:"firing"`
	LastTick *time.Time `json:"last_tick,omitempty"`
}

// Schedules lists every schedule of the store, in the order of the workflow library.
// It reads the last tick that the library records for each schedule and whether an engine runs, and adds no record of its own.
func (c *Client) Schedules() ([]ScheduleInfo, error) {
	rows, err := dbos.ListSchedules(c.flow)
	if err != nil {
		return nil, fmt.Errorf("list the schedules: %w", err)
	}

	running, err := engineRuns(c.dir)
	if err != nil {
		return nil, err
	}

	infos := make([]ScheduleInfo, 0, len(rows))

	for _, row := range rows {
		infos = append(infos, scheduleInfo(row, running))
	}

	return infos, nil
}

// scheduleInfo describes one schedule. A paused schedule does not fire, and no schedule fires while no engine runs.
func scheduleInfo(row dbos.WorkflowSchedule, engineRuns bool) ScheduleInfo {
	paused := row.Status == dbos.ScheduleStatusPaused

	return ScheduleInfo{
		Name: row.ScheduleName, Workflow: row.WorkflowName, Schedule: row.Schedule,
		Paused: paused, Firing: engineRuns && !paused, LastTick: utc(row.LastFiredAt),
	}
}

// utc returns a copy of a time in UTC, or nil for nil.
func utc(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}

	moved := at.UTC()

	return &moved
}
