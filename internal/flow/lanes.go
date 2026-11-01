package flow

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// LaneLine is one lane of an item: the agent step of a run, with the run that holds it.
type LaneLine struct {
	Run string `json:"run"`
	StepLine
}

// ItemLanes is the per-item view: every lane of the item across its runs, oldest attempt first, and in the order of each run.
// An item has one pull request, so one line for each lane is one row for each lane for each pull request.
type ItemLanes struct {
	Item  string     `json:"item"`
	Lanes []LaneLine `json:"lanes"`
}

// Lanes lists the lanes of an item across all its runs. It reads each run with Steps and keeps the lines that have a kind, so it keeps no table of its own.
func (c *Client) Lanes(item string) (ItemLanes, error) {
	ids, err := c.attemptsOf(item)
	if err != nil {
		return ItemLanes{}, err
	}

	return lanesRead(item, ids, c.Steps)
}

// lanesRead reads the steps of each run id in order, stops at the first run that cannot be read, and keeps the lines that have a kind.
func lanesRead(item string, ids []string, read func(string) (RunSteps, error)) (ItemLanes, error) {
	views := make([]RunSteps, 0, len(ids))

	for _, id := range ids {
		view, err := read(id)
		if err != nil {
			return ItemLanes{}, err
		}

		views = append(views, view)
	}

	return lanesOf(item, views), nil
}

// attemptsOf lists the run ids of an item, lowest attempt first. An item with no run is refused.
func (c *Client) attemptsOf(item string) ([]string, error) {
	prefix := runPrefix + item + attemptMarker

	rows, err := dbos.ListWorkflows(c.flow, dbos.WithFilterWorkflowIDPrefix(prefix), dbos.WithFilterName(itemName))
	if err != nil {
		return nil, fmt.Errorf("list the runs of item %s: %w", item, err)
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: item %q has no run. Run obee status to see the run ids", errNoSuchRun, item)
	}

	return attemptOrder(rows, prefix), nil
}

// attemptOrder returns the run ids of the rows, lowest attempt first. A row whose id has no attempt number is left out.
func attemptOrder(rows []dbos.WorkflowStatus, prefix string) []string {
	var ids []string

	number := func(id string) int { return atoiOr(strings.TrimPrefix(id, prefix)) }

	for _, row := range rows {
		if number(row.ID) > 0 {
			ids = append(ids, row.ID)
		}
	}

	slices.SortFunc(ids, func(a, b string) int { return cmp.Compare(number(a), number(b)) })

	return ids
}

// atoiOr reads a whole number, or gives 0 for text that is not one.
func atoiOr(text string) int {
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}

	return n
}

// lanesOf keeps the lines of the views that are lanes: the agent steps, which have a kind.
func lanesOf(item string, views []RunSteps) ItemLanes {
	out := ItemLanes{Item: item, Lanes: []LaneLine{}}

	for _, view := range views {
		for _, step := range view.Steps {
			if step.Kind != "" {
				out.Lanes = append(out.Lanes, LaneLine{Run: view.Run, StepLine: step})
			}
		}
	}

	return out
}
