package flow

import (
	"errors"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/model"
)

// slotQueuePrefix starts the name of the queue of a slot class.
const slotQueuePrefix = "slots:"

// errNoSlots is the refusal of a command call with `slots` when the build has no slot classes.
var errNoSlots = errors.New("the build has no slot classes, so a command call cannot name `slots`")

// errGateClass is the refusal of a command call that names the slot class of the gates.
var errGateClass = errors.New("the slot class \"gate\" belongs to the gates, so a command call cannot name it. Use another class in `slots`")

// SlotGrant is what a host gives to a slot class: the host that runs the job, and how many jobs of the class it runs at once.
type SlotGrant struct {
	Host  string
	Count int
}

// queueSlotCommand runs the command of request as a host job and waits for it. At most Count jobs of the class of the request run on the host at once:
// the jobs of one class share one queue, and the host is the partition of that queue, so a job of another class or of no class never waits for them.
// The engine knows the class by its name only. The host block of the project gives the count.
func (e *Engine) queueSlotCommand(run dbos.Context, request CommandRequest) (CommandOutput, error) {
	grant, queue, err := e.grantOf(request)
	if err != nil {
		return CommandOutput{}, err
	}

	in := ExclusiveInput{Kind: kindCommand, Run: workflowID(run), RunItem: request.Item, Project: request.Project, Item: request.Item, HostKey: hostKeyPrefix + grant.Host, SlotCount: grant.Count, Request: request}

	var out ExclusiveOutput

	if err = e.runJob(run, in, queue, &out); err != nil {
		return CommandOutput{}, err
	}

	return out.Command, nil
}

// grantOf asks the build for the host and the count of the slot class of request, and returns the queue of the class.
func (e *Engine) grantOf(request CommandRequest) (SlotGrant, dbos.Queue, error) { //nolint:ireturn // the queue type of the workflow library
	if e.deps.Slots == nil {
		return SlotGrant{}, nil, errNoSlots
	}

	if request.SlotClass == model.GateClass {
		return SlotGrant{}, nil, errGateClass
	}

	grant, err := e.deps.Slots(request.Project, request.BuildHost, request.SlotClass)
	if err != nil {
		return SlotGrant{}, nil, err
	}

	queue, err := e.slotQueue(request.SlotClass, grant.Count)

	return grant, queue, err
}

// slotQueueName is the name of the queue of a slot class with a count. A queue has one partition concurrency for all its partitions, so the count is part of the
// name: two hosts with different counts for one class use two queues.
func slotQueueName(class string, count int) string {
	return fmt.Sprintf("%s%s:%d", slotQueuePrefix, class, count)
}

// slotQueue registers the queue of a slot class with its count, and returns it. The queue allows count jobs at once for each host.
// A count that the host block changed names another queue, so a job already queued keeps its place on the queue that it chose.
func (e *Engine) slotQueue(class string, count int) (dbos.Queue, error) { //nolint:ireturn // the queue type of the workflow library
	options := []dbos.QueueOption{dbos.WithPartitionConcurrency(count)}
	if poll := e.deps.Clock.Poll; poll > 0 {
		options = append(options, dbos.WithQueueBasePollingInterval(poll))
	}

	queue, err := dbos.RegisterQueue(e.ctx, slotQueueName(class, count), options...)
	if err != nil {
		return nil, fmt.Errorf("register the queue of the slot class %q: %w", class, err)
	}

	return queue, nil
}
