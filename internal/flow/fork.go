package flow

import (
	"cmp"
	"context"
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

// forkPushedPrefix starts the name of the attribute that holds the branches that the runs before a forked run pushed.
const forkPushedPrefix = "fork-pushed:"

// headShaField is the output of an agent call that holds the head of the item folder after the call.
const headShaField = "head_sha"

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

	records, start, err := c.forkReady(req, in, attempt)
	if err != nil {
		return "", err
	}

	id := runID(in.Item, attempt)

	if err := c.forkAs(id, in, req, records, start); err != nil {
		return "", err
	}

	return id, nil
}

// forkReady reads the records of the source run, finds the start that the request names, and puts the worktree of the item at the head of the steps before it.
func (c *Client) forkReady(req ForkRequest, in ItemInput, attempt int) ([]dbos.StepInfo, uint, error) {
	records, err := c.records(req.Run)
	if err != nil {
		return nil, 0, err
	}

	start, err := forkStart(records, req)
	if err != nil {
		return nil, 0, err
	}

	return records, start, c.forkTree(in, attempt, records, start)
}

// forkAs takes the claim of the run id and forks the source run into it. A fork that fails gives the claim back.
func (c *Client) forkAs(id string, in ItemInput, req ForkRequest, records []dbos.StepInfo, start uint) error {
	overlay, err := forkOverlayOf(c.store(), req.Run, records)
	if err != nil {
		return err
	}

	owner, err := c.forkClaim(in, id, records, start, "beehave fork "+req.Run+" --from "+req.Step)
	if err != nil {
		return err
	}

	overlay.vars = mergedVars(overlay.vars, req.Vars)

	if err := c.forkRun(req.Run, id, start, overlay); err != nil {
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

// callStart is the id of the first record of the call named name. DBOS copies the records before this id and runs the call and every later one again.
// The grouping puts the sub-step that an agent call records after its read, the commit, at the head of the next call. The start skips it, so the commit
// of the agent stays a copied record of the agent call and the new run does not run it again.
func callStart(group []dbos.StepInfo, name string) uint {
	for _, record := range group {
		if base, _, split := strings.Cut(record.StepName, "/"); !split || base == name {
			return uint(record.StepID)
		}
	}

	return uint(group[0].StepID)
}

// forkStart returns the id of the first record of the call that the request names. The error lists the calls of the run, as beehave status shows them.
func forkStart(records []dbos.StepInfo, req ForkRequest) (uint, error) {
	calls := namedCalls(records)

	if at := slices.IndexFunc(calls, func(call namedCall) bool { return call.name == req.Step }); at >= 0 {
		return callStart(calls[at].records, req.Step), nil
	}

	names := make([]string, len(calls))
	for i, call := range calls {
		names[i] = call.name
	}

	return 0, fmt.Errorf("%w: run %s has no step %q. Its steps are %s", errBadFork, req.Run, req.Step, strings.Join(names, ", "))
}

// namedCall is the records of one call and its name as beehave status shows it.
type namedCall struct {
	name    string
	records []dbos.StepInfo
}

// namedCalls lists the finished calls of the run, then the call that the run stopped in or failed in, when there is one.
func namedCalls(records []dbos.StepInfo) []namedCall {
	groups, pending := callGroups(records)
	calls := make([]namedCall, 0, len(groups)+1)

	for _, group := range groups {
		calls = append(calls, namedCall{name: callLine(group, group[len(group)-1].StepName).Name, records: group})
	}

	if len(pending) > 0 {
		calls = append(calls, namedCall{name: openName(pending), records: pending})
	}

	return calls
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
func (c *Client) forkRun(source, id string, start uint, overlay forkOverlay) error {
	attributes := map[string]any{forkAttribute(id): overlay.vars, forkPushedAttribute(id): overlay.pushed}
	if err := dbos.SetWorkflowAttributes(c.flow, source, attributes, dbos.WithMergeAttributes()); err != nil {
		return fmt.Errorf("store the vars of run %s: %w", id, err)
	}

	_, err := dbos.ForkWorkflow[ItemResult](c.flow, dbos.ForkWorkflowInput{
		OriginalWorkflowID: source, ForkedWorkflowID: id, StartStep: start, ApplicationVersion: c.version, QueueName: rootsQueueName,
	})
	if err != nil {
		return errors.Join(fmt.Errorf("fork run %s as %s: %w", source, id, err), c.dropOverlay(source, id))
	}

	return nil
}

// dropOverlay takes the keys of a fork that failed off the row of the source run and keeps every other attribute. The store merges keys and cannot delete one,
// so the call writes the attributes of the row again without them.
func (c *Client) dropOverlay(source, id string) error {
	rows, err := c.store().List(dbos.WithFilterWorkflowIDs(source))
	if err != nil {
		return fmt.Errorf("read the attributes of run %s: %w", source, err)
	}

	kept := map[string]any{}

	for _, row := range rows {
		maps.Copy(kept, row.Attributes)
	}

	delete(kept, forkAttribute(id))
	delete(kept, forkPushedAttribute(id))

	if err := dbos.SetWorkflowAttributes(c.flow, source, kept); err != nil {
		return fmt.Errorf("remove the vars of run %s from run %s: %w", id, source, err)
	}

	return nil
}

// forkAttribute is the name of the attribute that holds the vars overlay of the run id.
func forkAttribute(id string) string { return forkAttributePrefix + id }

// forkPushedAttribute is the name of the attribute that holds the branches that the runs before the run id pushed. The run id may not push to them.
func forkPushedAttribute(id string) string { return forkPushedPrefix + id }

// forkOverlay is what a fork stores for the new run: the vars that replace the vars of the source run, and the branches that the source run and its ancestors pushed.
type forkOverlay struct {
	vars   map[string]any
	pushed []string
}

// forkAttributeOf reads the attribute key of the run id from its row, or nil when the run has none.
func forkAttributeOf(v storeView, id, key string) (any, error) {
	rows, err := v.List(dbos.WithFilterWorkflowIDs(id))
	if err != nil {
		return nil, fmt.Errorf("read the vars of run %s: %w", id, err)
	}

	var value any

	for _, row := range rows {
		value = row.Attributes[key]
	}

	return value, nil
}

// forkVarsOf reads the vars overlay of the run id from its row. A run that no fork made has none.
func forkVarsOf(v storeView, id string) (map[string]any, error) {
	value, err := forkAttributeOf(v, id, forkAttribute(id))

	return objectOf(value), err
}

// forkPushedOf reads the branches that the runs before the run id pushed. A run that no fork made has none.
func forkPushedOf(v storeView, id string) ([]string, error) {
	value, err := forkAttributeOf(v, id, forkPushedAttribute(id))
	if err != nil {
		return nil, err
	}

	var pushed []string

	items, _ := value.([]any) //nolint:errcheck // a run that no fork made has no list
	for _, item := range items {
		pushed = append(pushed, textOf(item))
	}

	return pushed, nil
}

// forkOverlayOf is the overlay that the source run passes on: its own overlay, and the branches that its pushes named.
func forkOverlayOf(v storeView, source string, records []dbos.StepInfo) (forkOverlay, error) {
	vars, err := forkVarsOf(v, source)
	if err != nil {
		return forkOverlay{}, err
	}

	pushed, err := forkPushedOf(v, source)

	return forkOverlay{vars: vars, pushed: pushedBranches(records, pushed)}, err
}

// pushedBranches adds the branch of each push step in records to pushed, once, in order.
func pushedBranches(records []dbos.StepInfo, pushed []string) []string {
	branches := slices.Clone(pushed)

	for _, record := range records {
		branch := textOf(recordedOutputs(record)[branchKeyword])
		if strings.HasPrefix(record.StepName, pushKind+":") && branch != "" && !slices.Contains(branches, branch) {
			branches = append(branches, branch)
		}
	}

	return branches
}

// textOf returns value when it is text, and an empty text for any other value.
func textOf(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}

	return text
}

// recordedOutputs returns the outputs of a star step that finished. The store keeps the output of a step as JSON text. Any other step has none.
func recordedOutputs(step dbos.StepInfo) map[string]any {
	text, ok := step.Output.(string)
	if !ok || step.Error != nil {
		return nil
	}

	var done stepResult
	if json.Unmarshal([]byte(text), &done) != nil {
		return nil
	}

	return done.Outputs
}

// forkTree puts the worktree of the item at the head that the steps before the start recorded, as the issue asks, before the fork makes a run. A tracker item
// gets a branch of its own for the attempt, so the old branch is never moved. A head that cannot be found refuses the fork, and nothing is stored or claimed.
func (c *Client) forkTree(in ItemInput, attempt int, records []dbos.StepInfo, start uint) error {
	head := forkHead(records, start)
	if head == "" || c.give.Place == nil || (c.give.Repository != nil && !c.give.Repository(in.Project)) {
		return nil
	}

	ctx, cancel := c.give.limit(context.Background())
	defer cancel()

	if err := c.give.Place.Place(ctx, in.Project, in.Item, head, forkBranch(in.Item, attempt)); err != nil {
		return fmt.Errorf("%w: the worktree of item %s cannot start at commit %s, the head that the copied steps recorded: %w", errBadFork, in.Item, head, err)
	}

	return nil
}

// forkBranch is the branch that the worktree of a tracker item uses in attempt. A round item has a detached worktree and no branch.
func forkBranch(item string, attempt int) string {
	if model.ItemKind(item) == model.KindRound {
		return ""
	}

	return fmt.Sprintf("%s-r%d", model.ItemBranch(item), attempt)
}

// forkHead is the commit that the worktree of the fork starts at: the head that the last step before start recorded, else the commit that the worktree of a round
// item was made at, else the head that the first agent call found. It is empty when the run recorded none.
func forkHead(records []dbos.StepInfo, start uint) string {
	var kept, tree, baseline string

	for _, record := range records {
		outputs := recordedOutputs(record)
		baseline = cmp.Or(baseline, textOf(outputs[recordedStartHead]))

		if uint(record.StepID) >= start {
			continue
		}

		kept = cmp.Or(textOf(outputs[headShaField]), kept)

		if record.StepName == treeMakeStepName {
			tree = recordedText(record)
		}
	}

	return cmp.Or(kept, tree, baseline)
}

// recordedText returns the text that a step with a plain text output returned.
func recordedText(step dbos.StepInfo) string {
	var text string
	if json.Unmarshal([]byte(textOf(step.Output)), &text) != nil {
		return ""
	}

	return text
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

// forkMayPush refuses a push to a branch that a run before this fork pushed to. A fork gets its own branch from the vars, or its push stops the item.
func (s *script) forkMayPush(branch string) error {
	id := workflowID(s.ctx)

	pushed, err := forkPushedOf(s.e.store(), id)
	if err != nil {
		return err
	}

	if slices.Contains(pushed, branch) {
		return fmt.Errorf("%w: run %s is a fork, and a run before it pushed to branch %s. Push the fork to another branch", errBadFork, id, branch)
	}

	return nil
}

// mergedVars returns the vars of the base with each top-level key of the overlay put over it.
func mergedVars(base, overlay map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(overlay))

	maps.Copy(merged, base)
	maps.Copy(merged, overlay)

	return merged
}
