// Package workflow reads a workflow file into steps in file order and checks its structure.
// It does not evaluate expressions and it does not run a step.
package workflow

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/tobocop2/obee/internal/hclfile"
)

const (
	workflowBlock = "workflow"
	topBlocks     = "workflow, agent, command, gate, loop, wait"
	loopBlocks    = "agent, command, gate, loop, wait"
)

// Load reads the workflow file at path from fsys. The steps are in file order, and each loop holds
// the steps in its body. The error holds one hclfile.Diagnostic for each structural problem.
func Load(fsys fs.FS, path string) (*Workflow, error) {
	body, err := hclfile.Parse(fsys, path)
	if err != nil {
		return nil, err
	}

	flow, _, err := build(path, body)
	if err != nil {
		return nil, err
	}

	return flow, nil
}

// build makes the workflow from a parsed file. The workflow holds the steps that loaded, even
// when the error is set. The map holds the name of every step in the file, loaded or not.
func build(path string, body *hclsyntax.Body) (*Workflow, map[string]hcl.Range, error) {
	b := &builder{path: path, names: map[string]hcl.Range{}, flow: &Workflow{File: path}}

	b.file(body)
	b.requireModel()

	if len(b.found) > 0 {
		return b.flow, b.names, join(b.found)
	}

	return b.flow, b.names, nil
}

// builder collects the workflow and every problem found while it walks the file.
type builder struct {
	path  string
	names map[string]hcl.Range
	flow  *Workflow
	found []hclfile.Diagnostic
}

func (b *builder) file(body *hclsyntax.Body) {
	for _, attr := range body.Attributes {
		b.add(attr.NameRange, fmt.Sprintf("unknown argument `%s`. Put it inside a block.", attr.Name))
	}

	rest, headers := b.headers(body.Blocks)

	if headers == 0 {
		b.found = append(b.found, hclfile.Diagnostic{File: b.path, Text: "the file has no `workflow` block. Add `workflow \"name\" { }`."})
	}

	b.flow.Steps = b.steps(rest, topBlocks)
}

// headers reads the workflow block. It returns the other blocks in file order and the count of workflow blocks.
func (b *builder) headers(blocks []*hclsyntax.Block) ([]*hclsyntax.Block, int) {
	var rest []*hclsyntax.Block

	headers := 0

	for _, blk := range blocks {
		if blk.Type != workflowBlock {
			rest = append(rest, blk)

			continue
		}

		headers++

		b.header(blk, headers)
	}

	return rest, headers
}

func (b *builder) header(blk *hclsyntax.Block, count int) {
	if count > 1 {
		b.add(blk.DefRange(), "`workflow` must appear once. Keep one.")

		return
	}

	if !b.named(blk) {
		return
	}

	var head header

	if err := hclfile.DecodeBody(blk.Body, &head); err != nil {
		b.addAll(err)

		return
	}

	b.flow.Name, b.flow.MethodVersion, b.flow.RequireModel = blk.Labels[0], head.MethodVersion, head.RequireModel
}

func (b *builder) steps(blocks []*hclsyntax.Block, valid string) []Step {
	var out []Step

	for _, blk := range blocks {
		out = b.appendStep(out, blk, valid)
	}

	return out
}

// appendStep adds the step that blk makes to out. It reports a block of an unknown type instead.
func (b *builder) appendStep(out []Step, blk *hclsyntax.Block, valid string) []Step {
	switch Kind(blk.Type) {
	case KindAgent:
		return b.keep(out, &Agent{}, blk)
	case KindCommand:
		return b.keep(out, &Command{}, blk)
	case KindGate:
		return b.keep(out, &Gate{}, blk)
	case KindLoop:
		return b.keep(out, &Loop{}, blk)
	case KindWait:
		return b.keep(out, &Wait{}, blk)
	}

	b.add(blk.TypeRange, fmt.Sprintf("unknown block `%s`. Use one of: %s.", blk.Type, valid))

	return out
}

func (b *builder) keep(out []Step, step Step, blk *hclsyntax.Block) []Step {
	if b.step(step, blk) {
		out = append(out, step)
	}

	return out
}

// step decodes one block into step. It returns false when the block cannot make a usable step.
func (b *builder) step(step Step, blk *hclsyntax.Block) bool {
	if !b.named(blk) {
		return false
	}

	b.claim(blk)
	step.place(blk.Labels[0], blk.DefRange())
	b.required(step.Kind(), blk)

	loop, isLoop := step.(*Loop)
	if !isLoop {
		return b.decode(step, blk)
	}

	loop.Steps = b.steps(blk.Body.Blocks, loopBlocks)

	return b.decodeLoop(loop, blk)
}

// decodeLoop decodes a loop and reports whether it is usable. A loop that fails to decode has no condition.
func (b *builder) decodeLoop(loop *Loop, blk *hclsyntax.Block) bool {
	if !b.decode(loop, blk) {
		return false
	}

	if loop.Max < 1 {
		b.add(blk.Body.Attributes["max"].Expr.Range(), "`max` must be at least 1. Set a number from 1 up.")
	}

	return true
}

// decode fills step from the arguments of the block and reports whether it succeeded.
func (b *builder) decode(step Step, blk *hclsyntax.Block) bool {
	err := hclfile.DecodeBody(ownAttributes(blk.Body), step)
	b.addAll(err)

	return err == nil
}

// required reports a missing argument that the decoder cannot enforce: it fills an absent
// argument of type hcl.Expression with null. The other required arguments, such as max, are typed.
func (b *builder) required(kind Kind, blk *hclsyntax.Block) {
	var key string

	switch kind {
	case KindAgent:
		key = "prompt"
	case KindCommand:
		key = "run"
	case KindLoop:
		key = "until"
	case KindGate, KindWait:
		return
	}

	if _, set := blk.Body.Attributes[key]; !set {
		b.add(blk.DefRange(), fmt.Sprintf("`%s` is required. Add it.", key))
	}
}

// ownAttributes returns a body with the attributes of body and none of its blocks. A loop holds
// its steps as blocks, and the decoder must see only the arguments of the loop itself.
func ownAttributes(body *hclsyntax.Body) *hclsyntax.Body {
	return &hclsyntax.Body{Attributes: body.Attributes, SrcRange: body.SrcRange, EndRange: body.EndRange}
}

// named reports whether the block has the one label that every block needs.
func (b *builder) named(blk *hclsyntax.Block) bool {
	if len(blk.Labels) == 1 {
		return true
	}

	b.add(blk.DefRange(), fmt.Sprintf("`%s` needs one name. Write `%s \"name\" { }`.", blk.Type, blk.Type))

	return false
}

// claim records the name of a step and reports a name that an earlier step already uses.
func (b *builder) claim(blk *hclsyntax.Block) {
	name := blk.Labels[0]

	if first, used := b.names[name]; used {
		b.add(blk.LabelRanges[0], fmt.Sprintf("step name `%s` is used twice; the first use is on line %d. Rename one.", name, first.Start.Line))

		return
	}

	b.names[name] = blk.LabelRanges[0]
}

func (b *builder) requireModel() {
	if !b.flow.RequireModel {
		return
	}

	b.flow.Walk(func(step Step, _ int) {
		agent, ok := step.(*Agent)
		if ok && unset(agent.Model) {
			b.add(agent.Pos(), fmt.Sprintf("agent `%s` has no `model`, and `require_model` is set. Add `model`.", agent.Name))
		}
	})
}

// unset reports whether an expression is null without a variable, as an absent argument is.
func unset(expr hcl.Expression) bool {
	value, diags := expr.Value(nil)

	return !diags.HasErrors() && value.IsNull()
}

func (b *builder) add(at hcl.Range, text string) {
	b.found = append(b.found, hclfile.Diagnostic{File: at.Filename, Line: at.Start.Line, Col: at.Start.Column, Text: text})
}

// addAll adds each problem inside an error that hclfile returned.
func (b *builder) addAll(err error) {
	b.found = append(b.found, diagnostics(err)...)
}

// diagnostics returns each problem inside an error that hclfile or join returned.
func diagnostics(err error) []hclfile.Diagnostic {
	var many interface{ Unwrap() []error }

	if errors.As(err, &many) {
		var found []hclfile.Diagnostic

		for _, one := range many.Unwrap() {
			found = append(found, diagnostics(one)...)
		}

		return found
	}

	var one hclfile.Diagnostic

	if errors.As(err, &one) {
		return []hclfile.Diagnostic{one}
	}

	return nil
}

// sortByPosition orders the problems by line and column and keeps the order of equal positions.
func sortByPosition(found []hclfile.Diagnostic) {
	slices.SortStableFunc(found, func(a, c hclfile.Diagnostic) int {
		return cmp.Or(cmp.Compare(a.Line, c.Line), cmp.Compare(a.Col, c.Col))
	})
}

// join sorts the problems by position and returns them as one error.
func join(found []hclfile.Diagnostic) error {
	sortByPosition(found)

	errs := make([]error, len(found))
	for i, d := range found {
		errs[i] = d
	}

	return errors.Join(errs...)
}
