package flow

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/model"
)

// gatePlaceName is the step that records the build host of a gate that names none, when the gate leaves the exclusive queue.
const gatePlaceName = "gate:place"

// placement is the host that gate:place chose and the gate slot count of that host.
type placement struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

// placeCandidates are the declared build hosts that a gate may go to, or nil when the gate keeps its host.
// A gate is placed when its project declares more than one host, the run recorded none in host_up, the gate is no program gate and the build has a host keeper.
func (e *Engine) placeCandidates(in ExclusiveInput, recorded string) []string {
	hosts := e.facts(in.Project).BuildHosts
	if e.deps.Hosts == nil || recorded != "" || len(hosts) < 2 || (in.Kind == kindGate && len(in.Request.Run) > 0) {
		return nil
	}

	return slices.Clone(hosts)
}

// withHost is in with host as the build host of its call.
func (in ExclusiveInput) withHost(host string) ExclusiveInput {
	if in.Kind == kindBatchGate {
		in.BuildHost = host
	} else {
		in.Request.BuildHost = host
	}

	in.HostKey = hostKeyPrefix + host

	return in
}

// place gives the gate of in its build host in a recorded step, so a replay reads the host that the first run chose. A gate with no candidates is returned as it is.
func (e *Engine) place(ctx dbos.Context, in ExclusiveInput) (ExclusiveInput, error) {
	if len(in.Hosts) == 0 {
		return in, nil
	}

	chosen, err := runStep(e, ctx, gatePlaceName, func(c context.Context) (placement, error) { return e.rankHosts(c, in, e.store().List) })
	if err != nil {
		return in, err
	}

	in.Hosts, in.SlotCount = nil, chosen.Count

	return in.withHost(chosen.Host), nil
}

// rankHosts picks the ready candidate with the most free gate slots. A candidate that is not ready, or that cannot be read, is skipped.
// A negative count of free slots is a backlog, so the shortest backlog wins when every host is full. A tie goes to the host declared first.
// No ready candidate gives the host that the gate was queued with.
func (e *Engine) rankHosts(ctx context.Context, in ExclusiveInput, list lister) (placement, error) {
	best := placement{Host: strings.TrimPrefix(in.HostKey, hostKeyPrefix), Count: in.SlotCount}
	bestFree, found := 0, false

	for _, host := range in.Hosts {
		count, free, ok, err := e.hostFree(ctx, in, host, list)
		if err != nil {
			return placement{}, err
		}

		if ok && (!found || free > bestFree) {
			best, bestFree, found = placement{Host: host, Count: count}, free, true
		}
	}

	return best, nil
}

// hostFree is the gate slot count of host and the free slots of it, and whether the host is a candidate: it is ready. A host that cannot be read is no candidate.
func (e *Engine) hostFree(ctx context.Context, in ExclusiveInput, host string, list lister) (count, free int, ok bool, err error) {
	ready, err := e.deps.Hosts.Ready(ctx, in.Project, host)
	if err != nil {
		e.logger.WarnContext(ctx, "the build host is skipped for this gate", "project", in.Project, "host", host, "error", err)

		return 0, 0, false, nil
	}

	if !ready {
		return 0, 0, false, nil
	}

	count, err = e.gateSlots(in.Project, host)
	if err != nil {
		return 0, 0, false, err
	}

	taken, err := gateJobs(list, count, host)

	return count, count - taken, err == nil, err
}

// gateJobs is the number of build jobs of the class gate that wait or run on host, read from the slot queue of that count.
func gateJobs(list lister, count int, host string) (int, error) {
	rows, err := list(dbos.WithFilterName(hostJobName), dbos.WithFilterQueueName(slotQueueName(model.GateClass, count)),
		dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued, dbos.WorkflowStatusPending))
	if err != nil {
		return 0, fmt.Errorf("count the gates that wait or run on host %s: %w", host, err)
	}

	key := hostKeyPrefix + host

	return len(slices.DeleteFunc(rows, func(row dbos.WorkflowStatus) bool { return row.QueuePartitionKey != key })), nil
}
