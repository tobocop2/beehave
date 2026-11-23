package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/model"
)

// errBadFork is returned when a fork request cannot start a run: the step, the vars or the source run do not fit.
var errBadFork = errors.New("cannot fork")

// forkAttributePrefix starts the name of the attribute that holds the vars overlay of a forked run. The attribute sits on the row of the source run, and the fork copies it to the new run.
const forkAttributePrefix = "fork:"

// ForkRequest asks for a new run of an item from a step of a run that ended. The new run keeps the recorded outputs of the steps before Step and runs Step and every later step again.
// Vars change the vars of the project for the new run: each top-level key replaces the key of the source run.
type ForkRequest struct {
	Run  string
	Step string
	Vars map[string]any
}

// ParseForkVars decodes the text of a vars file once, at the edge. A nil text holds no vars.
func ParseForkVars(text []byte) (map[string]any, error) {
	if text == nil {
		return nil, nil
	}

	var vars map[string]any
	if err := json.Unmarshal(text, &vars); err != nil {
		return nil, fmt.Errorf("%w: the vars file must hold one JSON object, such as {\"mode\": \"fast\"}: %w", errBadFork, err)
	}

	return vars, nil
}

// check refuses a request that no run can satisfy. A fork changes vars only, so a key that names a fact of the project is refused.
func (r ForkRequest) check() error {
	if r.Step == "" {
		return fmt.Errorf("%w: name the step to start from with --from", errBadFork)
	}

	facts := factsOutputs(model.Project{})

	for _, key := range slices.Sorted(maps.Keys(r.Vars)) {
		if _, isFact := facts[key]; isFact {
			return fmt.Errorf("%w: %q is a fact of the project, and a fork changes vars only", errBadFork, key)
		}
	}

	if _, err := json.Marshal(r.Vars); err != nil {
		return fmt.Errorf("%w: a var that a run cannot record: %w", errBadFork, err)
	}

	return nil
}

// Fork starts a new run of an item from a step of a run that ended, as the next attempt of the item, and returns its id. The new run waits in the queue: run beehave run for the item to continue it.
// It takes the fresh claim that the new run releases. The old run is not changed.
func (c *Client) Fork(req ForkRequest) (string, error) {
	if err := req.check(); err != nil {
		return "", err
	}

	var id string

	// probe.lock keeps a move and a cancel out while the fork reads the item and starts the run (see docs/design/durable-loop.md).
	err := c.underProbe(func() (err error) {
		id, err = c.forkHeld(req)

		return err
	})

	return id, err
}

// forkHeld is Fork while probe.lock is held.
func (c *Client) forkHeld(req ForkRequest) (string, error) {
	in, attempt, err := c.forkSource(req.Run)
	if err != nil {
		return "", err
	}

	records, err := c.records(req.Run)
	if err != nil {
		return "", err
	}

	start, err := forkStart(records, req)
	if err != nil {
		return "", err
	}

	id := runID(in.Item, attempt)

	if err := c.forkAs(id, in, req, records, start); err != nil {
		return "", err
	}

	return id, nil
}

// forkAs takes the claim of the run id and forks the source run into it. A fork that fails gives the claim back.
func (c *Client) forkAs(id string, in ItemInput, req ForkRequest, records []dbos.StepInfo, start uint) error {
	parent, err := forkVarsOf(c.store(), req.Run)
	if err != nil {
		return err
	}

	owner, err := c.forkClaim(in, id, records, start, "beehave fork "+req.Run+" --from "+req.Step)
	if err != nil {
		return err
	}

	if err := c.forkRun(req.Run, id, start, mergedVars(parent, req.Vars)); err != nil {
		return errors.Join(err, c.dropClaim(in.Project, in.Item, id, owner))
	}

	return nil
}

// forkSource checks that the run id names a root run of this build that ended, in an item that has no open run. It returns the input of the run and the number of the next attempt of the item.
func (c *Client) forkSource(id string) (ItemInput, int, error) {
	source, err := c.runWithID(id)
	if err != nil {
		return ItemInput{}, 0, err
	}

	in, err := c.forkable(source)
	if err != nil {
		return ItemInput{}, 0, err
	}

	newest, err := newestOf(c.store(), in.Item)
	if err != nil {
		return ItemInput{}, 0, err
	}

	if !source.terminal() || !newest.terminal() {
		return ItemInput{}, 0, fmt.Errorf("%w: item %s has an open run. Run beehave cancel %s first", ErrWrongState, in.Item, in.Item)
	}

	return in, newest.Attempt + 1, nil
}

// forkable returns the input of a source run that is a root run of this build. A pass run, a child run and a run of another build cannot be forked.
func (c *Client) forkable(source run) (ItemInput, error) {
	in, err := source.input()
	if err != nil {
		return ItemInput{}, err
	}

	if source.Status.Name != itemName || in.Depth > 0 {
		return ItemInput{}, fmt.Errorf("%w: %s is a pass run or a child run. Fork the root run of the item", errBadFork, source.Status.ID)
	}

	if source.Status.ApplicationVersion != c.version {
		return ItemInput{}, strandedError(source, c.version)
	}

	return in, nil
}

// forkStart returns the id of the first record of the call that the request names. The error lists the calls of the run, as beehave status shows them.
func forkStart(records []dbos.StepInfo, req ForkRequest) (uint, error) {
	calls, _ := callGroups(records)
	names := make([]string, 0, len(calls))

	for _, group := range calls {
		line := callLine(group, group[len(group)-1].StepName)
		if line.Name == req.Step {
			return uint(group[0].StepID), nil
		}

		names = append(names, line.Name)
	}

	return 0, fmt.Errorf("%w: run %s has no step %q. Its steps are %s", errBadFork, req.Run, req.Step, strings.Join(names, ", "))
}

// forkClaim takes a fresh claim for the new run id when the claim step of the source run stays recorded, that is, comes before the start. The new run replays that step and reads the saved token in place of the recorded one.
// A run that starts before its claim step claims by itself, so the owner is empty. again is the command that the error tells the owner to run once the claim is free.
func (c *Client) forkClaim(in ItemInput, id string, records []dbos.StepInfo, start uint, again string) (string, error) {
	kept := slices.ContainsFunc(records, func(record dbos.StepInfo) bool {
		return uint(record.StepID) < start && claimOutput(record) != ""
	})
	if !kept {
		return "", nil
	}

	return c.claimFresh(in.Project, in.Item, id, again)
}

// forkRun stores the vars overlay of the new run on the row of the source run, then forks. The fork copies the attribute in the transaction that creates the new run,
// so the run has its overlay from its first instant. The key is set on every fork, an empty overlay included, so a key that an earlier failed fork left under the same id is never read.
func (c *Client) forkRun(source, id string, start uint, vars map[string]any) error {
	if err := dbos.SetWorkflowAttributes(c.flow, source, map[string]any{forkAttribute(id): vars}, dbos.WithMergeAttributes()); err != nil {
		return fmt.Errorf("store the vars of run %s: %w", id, err)
	}

	_, err := dbos.ForkWorkflow[ItemResult](c.flow, dbos.ForkWorkflowInput{
		OriginalWorkflowID: source, ForkedWorkflowID: id, StartStep: start, ApplicationVersion: c.version, QueueName: rootsQueueName,
	})
	if err != nil {
		return fmt.Errorf("fork run %s as %s: %w", source, id, err)
	}

	return nil
}

// forkAttribute is the name of the attribute that holds the vars overlay of the run id.
func forkAttribute(id string) string { return forkAttributePrefix + id }

// forkVarsOf reads the vars overlay of the run id from its row. A run that no fork made has none.
func forkVarsOf(v storeView, id string) (map[string]any, error) {
	rows, err := v.List(dbos.WithFilterWorkflowIDs(id))
	if err != nil {
		return nil, fmt.Errorf("read the vars of run %s: %w", id, err)
	}

	var vars map[string]any

	for _, row := range rows {
		vars = objectOf(row.Attributes[forkAttribute(id)])
	}

	return vars, nil
}

// objectOf returns the JSON object that a store read decoded, or nil for a value that is not one. The store decodes a JSON object as a map.
func objectOf(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}

	return nil
}

// overlayVars puts the vars overlay of the run in this script over the vars of the recorded facts. A run that no fork made has no overlay.
func (s *script) overlayVars(facts map[string]any) error {
	overlay, err := forkVarsOf(s.e.store(), workflowID(s.ctx))
	if err != nil {
		return err
	}

	facts[varsField] = mergedVars(objectOf(facts[varsField]), overlay)

	return nil
}

// mergedVars returns the vars of the base with each top-level key of the overlay put over it.
func mergedVars(base, overlay map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(overlay))

	maps.Copy(merged, base)
	maps.Copy(merged, overlay)

	return merged
}
