package flow

import (
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// refreshPlaces gives each gate and each build job of a gate that waits in a queue the place that the current rules give it. It runs in Launch before the queues
// start, so no run starts with a stale place and nothing races the write. A run that a crash cut short keeps its place in the store, and the rules can differ now:
// the severity of its item, or a person who waits on its pull request. A merge and the host job of a command call keep the place they have.
// A place that a person set with beehest gate priority stays: writePlaces skips the run that a person marked.
func (e *Engine) refreshPlaces() error {
	waiting, err := e.store().Open("", dbos.WithFilterLoadInput(true))
	if err != nil {
		return fmt.Errorf("list the runs that wait in a queue: %w", err)
	}

	places := e.currentPlaces(waiting)
	if len(places) == 0 {
		return nil
	}

	_, err = writePlaces(e.dir, false, places)

	return err
}

// currentPlaces is the place of the current rules for each gate and each build job of a gate among the open runs that wait in a queue.
func (e *Engine) currentPlaces(open []dbos.WorkflowStatus) map[string]placeWrite {
	places := map[string]placeWrite{}

	for _, row := range open {
		if row.Status != dbos.WorkflowStatusEnqueued || (row.Name != exclusiveName && row.Name != hostJobName) {
			continue
		}

		if in, ok := gateInputOf(row.Input); ok {
			standing, read := e.gateStanding(e.ctx, in)
			places[row.ID] = placeWrite{place: priorityOf(in, standing, read), parent: row.ParentWorkflowID}
		}
	}

	return places
}
