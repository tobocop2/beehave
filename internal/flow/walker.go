package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"

	"github.com/tobocop2/obee/internal/hclfile"
	"github.com/tobocop2/obee/internal/workflow"
)

// CommandRequest is one program that a command step or a gate step runs. Host is empty for the item folder.
type CommandRequest struct {
	Project string
	Item    string
	Run     []string
	Host    string
	Timeout time.Duration
}

// CommandOutput is what a program printed and its exit code. A non-zero RC is data, not an error.
type CommandOutput struct {
	Stdout string
	RC     int
}

// commands runs the programs of command steps.
type commands interface {
	Run(ctx context.Context, request CommandRequest) (CommandOutput, error)
}

// items reads the fields of an item, which are the `item` object of a workflow file.
type items interface {
	Fields(ctx context.Context, project, item string) (map[string]string, error)
}

// stepResult is what a step records. A skipped step has no outputs. Note carries a value that a later part of the same node reads on replay.
type stepResult struct {
	Skipped bool           `json:"skipped,omitempty"`
	Outputs map[string]any `json:"outputs,omitempty"`
	Note    string         `json:"note,omitempty"`
}

// executor runs the steps of one kind: it evaluates the `when` of the step, then runs the step or records it as skipped.
// Order: the walker calls an executor once for each node and round, in file order, on the workflow goroutine.
// An executor does its side effects inside call.durably, which is the DBOS step of the node, so a replay gets the recorded result.
// An executor keeps no state between calls. It names its outputs from workflow.KindOutputs.
type executor func(c *call) (stepResult, error)

// executorFor is the one table of executors by kind. A loop has none: the walker runs it.
func executorFor(kind workflow.Kind) executor {
	switch kind {
	case workflow.KindCommand:
		return bind(func(s *workflow.Command) hcl.Expression { return s.When }, runCommand)
	case workflow.KindGate:
		return bind(func(*workflow.Gate) hcl.Expression { return nil }, runGate)
	case workflow.KindWait:
		return bind(func(s *workflow.Wait) hcl.Expression { return s.When }, runWait)
	case workflow.KindAgent:
		return bind(func(s *workflow.Agent) hcl.Expression { return s.When }, runAgent)
	case workflow.KindLoop:
		// The walker runs a loop itself.
	}

	return nil
}

// bind makes an executor from the steps of one type: the skip test and the run.
func bind[S workflow.Step](when func(S) hcl.Expression, run func(*call, S) (stepResult, error)) executor {
	return func(c *call) (stepResult, error) {
		step, ok := c.step.(S)
		if !ok {
			return stepResult{}, fmt.Errorf("step %s is a %s step, and this executor does not run it", c.step.Label(), c.step.Kind())
		}

		on, err := c.enabled(when(step))
		if err != nil {
			return stepResult{}, err
		}

		if !on {
			return c.durably(func(context.Context) (stepResult, error) { return stepResult{Skipped: true}, nil })
		}

		return run(c, step)
	}
}

// call is one node in one round.
type call struct {
	w     *walker
	step  workflow.Step
	round int
	name  string
}

// durably runs fn as the DBOS step of the node.
func (c *call) durably(fn func(context.Context) (stepResult, error)) (stepResult, error) {
	return runStep(c.w.e, c.w.ctx, c.name, fn)
}

func (c *call) value(expr hcl.Expression) (cty.Value, error) { return c.w.scope.Value(expr, c.round) }

// enabled evaluates the `when` of a step. A step without a `when` runs.
func (c *call) enabled(cond hcl.Expression) (bool, error) {
	if absent(cond) {
		return true, nil
	}

	return c.w.scope.Bool(cond, c.round)
}

// text evaluates an optional argument that is a string. An absent argument is empty.
func (c *call) text(expr hcl.Expression) (string, error) {
	if absent(expr) {
		return "", nil
	}

	value, err := c.value(expr)
	if err != nil {
		return "", err
	}

	if value.IsNull() || value.Type() != cty.String {
		return "", hclfile.At(expr.Range(), "this argument must be a string. Write text in quotes.")
	}

	return value.AsString(), nil
}

// words evaluates an optional argument that is a list of strings. An absent argument is empty.
func (c *call) words(expr hcl.Expression) ([]string, error) {
	if absent(expr) {
		return nil, nil
	}

	value, err := c.value(expr)
	if err != nil {
		return nil, err
	}

	list, convErr := convert.Convert(value, cty.List(cty.String))
	if convErr != nil || list.IsNull() {
		return nil, hclfile.At(expr.Range(), "this argument must be a list of strings. Write each word of the command as one string.")
	}

	return elements(list), nil
}

// elements returns the elements of a list of strings.
func elements(list cty.Value) []string {
	var out []string

	for it := list.ElementIterator(); it.Next(); {
		_, word := it.Element()
		out = append(out, word.AsString())
	}

	return out
}

// duration evaluates an optional timeout argument. An absent argument is zero.
func (c *call) duration(expr hcl.Expression) (time.Duration, error) {
	limit, err := c.text(expr)
	if err != nil || limit == "" {
		return 0, err
	}

	d, err := time.ParseDuration(limit)
	if err != nil {
		return 0, hclfile.At(expr.Range(), fmt.Sprintf("%q is not a duration. Write a duration such as \"90s\" or \"10m\".", limit))
	}

	return d, nil
}

// request evaluates the arguments that command steps and gate steps share.
func (c *call) request(run, host, timeout hcl.Expression) (CommandRequest, error) {
	argv, err := c.words(run)
	if err != nil {
		return CommandRequest{}, err
	}

	where, err := c.text(host)
	if err != nil {
		return CommandRequest{}, err
	}

	limit, err := c.duration(timeout)
	if err != nil {
		return CommandRequest{}, err
	}

	return CommandRequest{Project: c.w.in.Project, Item: c.w.in.Item, Run: argv, Host: where, Timeout: limit}, nil
}

// absent reports whether an argument is not set.
func absent(expr hcl.Expression) bool {
	if expr == nil {
		return true
	}

	value, diags := expr.Value(nil)

	return !diags.HasErrors() && value.IsNull()
}

func runCommand(c *call, step *workflow.Command) (stepResult, error) {
	request, err := c.request(step.Run, step.Host, step.Timeout)
	if err != nil {
		return stepResult{}, err
	}

	return c.durably(func(ctx context.Context) (stepResult, error) {
		out, err := c.w.e.deps.Commands.Run(ctx, request)
		if err != nil {
			return stepResult{}, err
		}

		return stepResult{Outputs: map[string]any{"stdout": out.Stdout, "rc": out.RC}}, nil
	})
}

func runGate(c *call, step *workflow.Gate) (stepResult, error) {
	request, err := c.request(step.Run, step.Host, step.Timeout)
	if err != nil {
		return stepResult{}, err
	}

	head, err := c.text(step.Head)
	if err != nil {
		return stepResult{}, err
	}

	return c.durably(func(ctx context.Context) (stepResult, error) {
		outputs, err := c.gateOutputs(ctx, request, head)
		if err != nil {
			return stepResult{}, fmt.Errorf("gate %q could not run: %w", c.step.Label(), err)
		}

		return stepResult{Outputs: outputs}, nil
	})
}

// gateOutputs runs the project gate when the request names no program, and the program otherwise.
func (c *call) gateOutputs(ctx context.Context, request CommandRequest, head string) (map[string]any, error) {
	if len(request.Run) == 0 {
		result, err := c.w.e.deps.Gate.Run(ctx, request.Project, request.Item)
		if err != nil {
			return nil, err
		}

		return map[string]any{"passed": result.Passed, "head": result.Head, "report": result.Report}, nil
	}

	return c.programOutputs(ctx, request, head)
}

// programOutputs runs the program of a gate step. The gate passes when the program exits with zero.
func (c *call) programOutputs(ctx context.Context, request CommandRequest, head string) (map[string]any, error) {
	out, err := c.w.e.deps.Commands.Run(ctx, request)
	if err != nil {
		return nil, err
	}

	outputs := map[string]any{"passed": out.RC == 0, "report": out.Stdout}
	if head != "" {
		outputs["head"] = head
	}

	return outputs, nil
}

// ask records the question of a wait step, so a replay asks the same question.
func (c *call) ask(question hcl.Expression) (stepResult, error) {
	text, err := c.text(question)
	if err != nil {
		return stepResult{}, err
	}

	return c.durably(func(context.Context) (stepResult, error) { return stepResult{Note: text}, nil })
}

func runWait(c *call, step *workflow.Wait) (stepResult, error) {
	asked, err := c.ask(step.Question)
	if err != nil {
		return stepResult{}, err
	}

	answer, err := c.w.e.waitOwner(c.w.ctx, c.w.in.Item, orDefault(asked.Note, "The workflow waits for your answer."))
	if err != nil {
		return stepResult{}, err
	}

	return stepResult{Outputs: map[string]any{"answer": answer}}, nil
}

func orDefault(text, fallback string) string {
	if text == "" {
		return fallback
	}

	return text
}

// walker runs the steps of one workflow file for one item. Its scope holds recorded results only.
type walker struct {
	e     *Engine
	ctx   dbos.Context
	in    ItemInput
	scope *workflow.Scope
}

// readFile reads the workflow file once and returns its bytes and their SHA-256, in hex.
func readFile(path string) ([]byte, string, error) {
	data, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		return nil, "", fmt.Errorf("read the workflow file: %w. Restore the file, or cancel the run and start again", err)
	}

	sum := sha256.Sum256(data)

	return data, hex.EncodeToString(sum[:]), nil
}

// hashFile returns the SHA-256 of the file, in hex.
func hashFile(path string) (string, error) {
	_, hash, err := readFile(path)

	return hash, err
}

// loadPinned loads the workflow file of the run. The file must hash to the value the run started with.
// It reads the file once, so the steps come from the bytes that were hashed.
func loadPinned(in ItemInput) (*workflow.Workflow, error) {
	data, hash, err := readFile(in.File)
	if err != nil {
		return nil, err
	}

	if hash != in.Hash {
		return nil, fmt.Errorf("%s changed since the run started: it had hash %s and has hash %s. Restore the file, or cancel the run and start again", in.File, in.Hash, hash)
	}

	return workflow.LoadBytes(in.File, data)
}

// walk runs the workflow file of the item, depth first.
func (e *Engine) walk(ctx dbos.Context, in ItemInput) (ItemResult, error) {
	file, err := loadPinned(in)
	if err != nil {
		return ItemResult{}, err
	}

	scope, err := e.recordScope(ctx, in, file)
	if err != nil {
		return ItemResult{}, err
	}

	w := &walker{e: e, ctx: ctx, in: in, scope: scope}

	return ItemResult{Item: in.Item}, w.steps(file.Steps, nil)
}

// recordScope records the environment the file reads and the fields of the item, as the first two steps, and builds the scope from the records.
func (e *Engine) recordScope(ctx dbos.Context, in ItemInput, file *workflow.Workflow) (*workflow.Scope, error) {
	env, envErr := runStep(e, ctx, "env", func(context.Context) (map[string]string, error) {
		return pick(e.env, file.EnvNames()), nil
	})

	fields, fieldsErr := runStep(e, ctx, "item", func(c context.Context) (map[string]string, error) {
		return e.deps.Items.Fields(c, in.Project, in.Item)
	})

	if err := errors.Join(envErr, fieldsErr); err != nil {
		return nil, fmt.Errorf("read the fields of item %s and record the environment: %w", in.Item, err)
	}

	return workflow.NewScope(fields, env), nil
}

// pick returns the variables of env that have one of the names.
func pick(env map[string]string, names []string) map[string]string {
	out := map[string]string{}

	for _, name := range names {
		if value, ok := env[name]; ok {
			out[name] = value
		}
	}

	return out
}

// rounds holds the round of each loop around a step, outermost first. It is empty outside a loop.
type rounds []int

// innermost returns the round of the closest loop, or 0 outside a loop. This is `loop.round`.
func (r rounds) innermost() int {
	if len(r) == 0 {
		return 0
	}

	return r[len(r)-1]
}

// suffix returns the part of a step name after the `#`: every round, outermost first, or 1 outside a loop.
func (r rounds) suffix() string {
	if len(r) == 0 {
		return "1"
	}

	parts := make([]string, len(r))
	for i, round := range r {
		parts[i] = strconv.Itoa(round)
	}

	return strings.Join(parts, ".")
}

// in returns the rounds of a loop body in the given round. It does not change r.
func (r rounds) in(round int) rounds { return append(slices.Clone(r), round) }

func (w *walker) steps(steps []workflow.Step, at rounds) error {
	for _, step := range steps {
		if err := w.one(step, at); err != nil {
			return err
		}
	}

	return nil
}

func (w *walker) one(step workflow.Step, at rounds) error {
	if loop, ok := step.(*workflow.Loop); ok {
		return w.loop(loop, at)
	}

	return w.node(step, at)
}

// node runs one step in one round and records its result in the scope.
func (w *walker) node(step workflow.Step, at rounds) error {
	c := &call{w: w, step: step, round: at.innermost(), name: step.Label() + "#" + at.suffix()}

	result, err := executorFor(step.Kind())(c)
	if err != nil {
		return fmt.Errorf("step %s: %w", c.name, err)
	}

	return w.record(step, result)
}

func (w *walker) record(step workflow.Step, result stepResult) error {
	if result.Skipped {
		w.scope.Skip(step)

		return nil
	}

	values, err := typed(step, result.Outputs)
	if err != nil {
		return fmt.Errorf("step %s: %w", step.Label(), err)
	}

	w.scope.Record(step, values)

	return nil
}

// loop runs the body up to Max rounds. Until is checked right after the last step it names.
func (w *walker) loop(loop *workflow.Loop, outer rounds) error {
	last := lastNamed(loop)
	exhausted := false

	for round := 1; round <= loop.Max; round++ {
		stopped, err := w.round(loop, last, outer.in(round))
		if err != nil {
			return err
		}

		if stopped {
			break
		}

		exhausted = round == loop.Max
	}

	w.scope.Record(loop, map[string]cty.Value{"exhausted": cty.BoolVal(exhausted)})

	return nil
}

// round runs one round of a loop and reports whether until held.
func (w *walker) round(loop *workflow.Loop, last int, at rounds) (bool, error) {
	for i, step := range loop.Steps {
		if err := w.one(step, at); err != nil {
			return false, err
		}

		if i != last {
			continue
		}

		stopped, err := w.scope.Bool(loop.Until, at.innermost())
		if err != nil || stopped {
			return stopped, err
		}
	}

	return false, nil
}

// lastNamed returns the index of the last step of the body that until names, or the last step.
func lastNamed(loop *workflow.Loop) int {
	found := -1

	for _, tr := range loop.Until.Variables() {
		split := tr.SimpleSplit()
		if len(split.Rel) == 0 {
			continue
		}

		if attr, ok := split.Rel[0].(hcl.TraverseAttr); ok {
			found = max(found, stepIndex(loop.Steps, split.Abs.RootName(), attr.Name))
		}
	}

	if found < 0 {
		return len(loop.Steps) - 1
	}

	return found
}

// stepIndex returns the index of the step of this kind and name, or -1.
func stepIndex(steps []workflow.Step, kind, name string) int {
	for i, step := range steps {
		if string(step.Kind()) == kind && step.Label() == name {
			return i
		}
	}

	return -1
}

// typed converts recorded outputs to cty values by the types that workflow.StepOutputs gives: those of workflow.KindOutputs, and the names an agent declares.
// A name that the step does not declare is an error.
func typed(step workflow.Step, raw map[string]any) (map[string]cty.Value, error) {
	declared := declaredTypes(step)
	out := map[string]cty.Value{}

	for name, v := range raw {
		want, ok := declared[name]
		if !ok {
			return nil, fmt.Errorf("a %s step has no output %q", step.Kind(), name)
		}

		value, err := toValue(want, v)
		if err != nil {
			return nil, fmt.Errorf("output %q of a %s step: %w", name, step.Kind(), err)
		}

		out[name] = value
	}

	return out, nil
}

// declaredTypes returns the type of each output of a step, by name.
func declaredTypes(step workflow.Step) map[string]cty.Type {
	declared := map[string]cty.Type{}
	for _, o := range workflow.StepOutputs(step) {
		declared[o.Name] = o.Type
	}

	return declared
}

// toValue converts one recorded value, as it comes back from JSON, to a cty value of the declared type.
func toValue(want cty.Type, v any) (cty.Value, error) {
	if v == nil {
		return cty.NullVal(want), nil
	}

	got, ok := scalar(v)
	if !ok || !got.Type().Equals(want) {
		return cty.NilVal, fmt.Errorf("the value %v is not a %s", v, want.FriendlyName())
	}

	return got, nil
}

// scalar converts a string, a bool or a number. Any other value has no cty value.
func scalar(v any) (cty.Value, bool) {
	switch x := v.(type) {
	case string:
		return cty.StringVal(x), true
	case bool:
		return cty.BoolVal(x), true
	case int:
		return cty.NumberIntVal(int64(x)), true
	case float64:
		return cty.NumberFloatVal(x), true
	}

	return cty.NilVal, false
}
