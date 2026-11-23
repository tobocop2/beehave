package flow

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// FailedFilter narrows the runs that FailedRuns lists. A zero Since or Until sets no bound, an empty Project sets no project, and a Limit of 0 sets no limit.
type FailedFilter struct {
	// Since is the earliest end time of a run, inclusive.
	Since time.Time
	// Until is the latest end time of a run, inclusive.
	Until time.Time
	// Project keeps only the runs whose input names this project.
	Project string
	// Limit keeps at most this many runs, the ones that ended first.
	Limit int
}

// FailedRun is one run that failed and that beehave resume reopens.
type FailedRun struct {
	Item  string
	ID    string
	Ended time.Time
}

// errBadFailedFilter is returned when a failed-run filter cannot pick any run.
var errBadFailedFilter = errors.New("the filter of the failed runs is not valid")

// check refuses a negative limit and a range that ends before it starts.
func (f FailedFilter) check() error {
	if f.Limit < 0 {
		return fmt.Errorf("%w: the limit is %d. Give 0 for no limit, or a count above 0", errBadFailedFilter, f.Limit)
	}

	if !f.Since.IsZero() && !f.Until.IsZero() && f.Until.Before(f.Since) {
		return fmt.Errorf("%w: the end of the range is before its start. Swap them", errBadFailedFilter)
	}

	return nil
}

// within reports whether an end time is inside the closed range.
func (f FailedFilter) within(ended time.Time) bool {
	return (f.Since.IsZero() || !ended.Before(f.Since)) && (f.Until.IsZero() || !ended.After(f.Until))
}

// FailedRuns lists the runs that failed and that beehave resume can reopen: the newest attempt of an item, ended with an error or with too many recovery attempts.
// A run that a move has canceled is not failed, so it is never listed. The list is ordered by end time, then run id. It reads the store and changes nothing.
func (c *Client) FailedRuns(filter FailedFilter) ([]FailedRun, error) {
	if err := filter.check(); err != nil {
		return nil, err
	}

	failed, err := c.pickAll(filter)
	if err != nil {
		return nil, err
	}

	slices.SortFunc(failed, func(a, b FailedRun) int {
		return cmp.Or(a.Ended.Compare(b.Ended), strings.Compare(a.ID, b.ID))
	})

	if filter.Limit > 0 && len(failed) > filter.Limit {
		failed = failed[:filter.Limit]
	}

	return failed, nil
}

// pickAll returns the runs of the failed rows that the filter picks, in the order of the rows.
func (c *Client) pickAll(filter FailedFilter) ([]FailedRun, error) {
	rows, err := c.store().List(dbos.WithFilterWorkflowIDPrefix(runPrefix), dbos.WithFilterName(itemName), dbos.WithFilterLoadInput(true),
		dbos.WithFilterStatus(dbos.WorkflowStatusError, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded))
	if err != nil {
		return nil, fmt.Errorf("list the failed runs: %w", err)
	}

	var failed []FailedRun

	for _, row := range rows {
		picked, found, err := c.pickFailed(row, filter)
		if err != nil {
			return nil, err
		}

		if found {
			failed = append(failed, picked)
		}
	}

	return failed, nil
}

// pickFailed returns the run of a failed row when the filter picks it: its end time is in the range, its project fits, and it is the newest attempt of its item.
func (c *Client) pickFailed(row dbos.WorkflowStatus, filter FailedFilter) (FailedRun, bool, error) {
	ended := endOf(row)
	if !filter.within(ended) {
		return FailedRun{}, false, nil
	}

	in, err := run{Status: row}.input()
	if err != nil || (filter.Project != "" && in.Project != filter.Project) {
		return FailedRun{}, false, err
	}

	last, _, err := latestRun(c.store(), in.Item)
	if err != nil || last.Status.ID != row.ID {
		return FailedRun{}, false, err
	}

	return FailedRun{Item: in.Item, ID: row.ID, Ended: ended}, true, nil
}
