package flow

import (
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// debounceHold is how many windows a start may be pushed back in all. A start that gets a later start every window never runs without it, so the hold ends at this many windows after the first start.
const debounceHold = 10

// debounce holds the start of attempt back for the window of the input on the roots queue. The item is the key: a later debounced start of the item pushes the delay of the delayed run back and replaces its input, and the run keeps the id of the first start.
// The run enqueues for the version of this build, as a join does.
func (c *Client) debounce(in ItemInput, attempt int) (dbos.WorkflowHandle[ItemResult], error) {
	debouncer := dbos.NewDebouncerClient[ItemResult, ItemInput](itemName, c.flow, dbos.WithDebouncerQueue(rootsQueueName), dbos.WithDebouncerTimeout(debounceHold*in.Debounce))

	handle, err := debouncer.Debounce(in.Item, in.Debounce, in, dbos.WithWorkflowID(runID(in.Item, attempt)), dbos.WithApplicationVersion(c.version))
	if err != nil {
		return nil, fmt.Errorf("hold the start of item %s for %s: %w", in.Item, in.Debounce, err)
	}

	return handle, nil
}
