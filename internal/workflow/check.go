package workflow

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	"github.com/tobocop2/obee/internal/hclfile"
)

const (
	rootItem = "item"
	rootEnv  = "env"

	roundName = "round"

	// attrOutput is the place of the output name in a reference such as agent.<name>.<output>.
	attrOutput = 2
	// nearSlack is the edit distance that a short name may be from its nearest valid name.
	nearSlack = 2
)

// Check reads the workflow file at file from fsys and returns every problem in it, in position order.
// The second result is true when the file could not be read or parsed, and no other check ran.
// A file with structural problems still gets the reference, cycle and file checks on the steps that loaded.
// A reference to a step that failed to load is not reported again.
// fields lists the names that the tracker and the forge declare for an item. The field `kind` is always valid.
func Check(fsys fs.FS, file string, fields []string) ([]hclfile.Diagnostic, bool) {
	body, err := hclfile.Parse(fsys, file)
	if err != nil {
		return diagnostics(err), true
	}

	flow, names, err := build(file, body)

	valid := append(slices.Clone(fields), "kind")
	slices.Sort(valid)

	c := &checker{fsys: fsys, flow: flow, byName: map[string]*site{}, declared: names, fields: slices.Compact(valid), found: diagnostics(err)}

	c.index(flow.Steps, nil)
	c.references()
	c.cycles()
	c.prompts()
	c.merges()
	sortByPosition(c.found)

	return c.found, false
}

// site is one step with its place in the walk and the steps it depends on.
type site struct {
	step      Step
	index     int
	ancestors []Step
	edges     []*site
}

// inLoop reports whether `loop.round` has a meaning for the step. The condition of a loop is inside it.
func (s *site) inLoop() bool {
	return len(s.ancestors) > 0 || s.step.Kind() == KindLoop
}

func (s *site) depend(on *site) {
	if !slices.Contains(s.edges, on) {
		s.edges = append(s.edges, on)
	}
}

type checker struct {
	fsys   fs.FS
	flow   *Workflow
	sites  []*site
	byName map[string]*site
	// declared holds every step name in the file, including the steps that failed to load.
	declared map[string]hcl.Range
	// fields holds the sorted names that `item` may read.
	fields []string
	found  []hclfile.Diagnostic
}

// index lists the steps in walk order. ancestors holds the loops around steps.
func (c *checker) index(steps []Step, ancestors []Step) {
	for _, step := range steps {
		s := &site{step: step, index: len(c.sites), ancestors: ancestors}
		c.sites = append(c.sites, s)
		c.byName[step.Label()] = s

		if loop, ok := step.(*Loop); ok {
			c.index(loop.Steps, append(slices.Clone(ancestors), step))
		}
	}
}

func (c *checker) add(at hcl.Range, text string) {
	c.found = append(c.found, hclfile.At(at, text))
}

// references checks each name that an expression reads. It does not evaluate an expression.
func (c *checker) references() {
	for _, from := range c.sites {
		for _, arg := range from.step.arguments() {
			for _, tr := range arg.expr.Variables() {
				c.resolve(from, tr, arg.whole)
			}
		}
	}
}

func (c *checker) resolve(from *site, tr hcl.Traversal, whole bool) {
	root := tr.RootName()
	name, named := attribute(tr, 1)

	switch Kind(root) {
	case KindAgent, KindCommand, KindGate, KindMerge, KindWait:
		if named {
			c.step(from, tr, Kind(root), name, whole)
		} else {
			c.add(tr.SourceRange(), fmt.Sprintf("`%s` needs a step name. Write `%s.<name>.<output>`.", root, root))
		}
	case KindLoop:
		c.loop(from, tr, name, named, whole)
	default:
		c.plain(tr.SourceRange(), root, name, named)
	}
}

// plain checks a reference that does not name a step.
func (c *checker) plain(at hcl.Range, root, name string, named bool) {
	switch root {
	case rootItem:
		c.item(at, name, named)
	case rootEnv:
		if !named {
			c.add(at, "`env` needs a name. Write `env.<NAME>`.")
		}
	default:
		c.add(at, "unknown name `"+root+"`."+hint(root, roots()))
	}
}

// attribute returns the name at position i of a traversal, if that part is a plain name.
func attribute(tr hcl.Traversal, i int) (string, bool) {
	if len(tr) <= i {
		return "", false
	}

	attr, ok := tr[i].(hcl.TraverseAttr)

	return attr.Name, ok
}

func (c *checker) item(at hcl.Range, field string, named bool) {
	if !named {
		c.add(at, "`item` needs a field. Write `item.<field>`."+hint("", c.fields))

		return
	}

	if !slices.Contains(c.fields, field) {
		c.add(at, fmt.Sprintf("`item` has no field `%s`.%s", field, hint(field, c.fields)))
	}
}

func (c *checker) loop(from *site, tr hcl.Traversal, next string, named, whole bool) {
	at := tr.SourceRange()

	switch {
	case !named:
		c.add(at, "`loop` needs `round` or a loop name. Write `loop.round` or `loop.<name>.exhausted`.")
	case next == roundName && from.inLoop():
	case next == roundName:
		c.add(at, "`loop.round` is used outside a loop. Put the step in a `loop` block.")
	default:
		c.step(from, tr, KindLoop, next, whole)
	}
}

// step checks a reference to the step name of kind, and its output. When whole is true the
// reference names an agent and no output.
func (c *checker) step(from *site, tr hcl.Traversal, kind Kind, name string, whole bool) {
	at := tr.SourceRange()

	target := c.byName[name]
	if target == nil || target.step.Kind() != kind {
		c.unknown(at, kind, name)

		return
	}

	c.order(from, target, at)

	if whole && kind != KindAgent {
		c.add(at, fmt.Sprintf("this argument names an agent, and `%s.%s` is not one. Write `agent.<name>`.", kind, name))
	}

	if _, named := attribute(tr, attrOutput); !whole || named {
		c.output(tr, target)
	}
}

// unknown reports a reference to a step that does not exist. A step that failed to load does exist.
func (c *checker) unknown(at hcl.Range, kind Kind, name string) {
	if _, failed := c.declared[name]; failed && c.byName[name] == nil {
		return
	}

	c.unknownStep(at, kind, name)
}

// order records that from depends on target and reports a target that runs later. A loop that
// encloses from is no dependency, and the condition of a loop may read the steps in its body.
func (c *checker) order(from, target *site, at hcl.Range) {
	if !slices.Contains(from.ancestors, target.step) {
		from.depend(target)
	}

	if target.index > from.index && !slices.Contains(target.ancestors, from.step) {
		c.add(at, fmt.Sprintf("`%s.%s` runs later than this reference. Move the step up or remove the reference.", target.step.Kind(), target.step.Label()))
	}
}

func (c *checker) unknownStep(at hcl.Range, kind Kind, name string) {
	var valid []string

	for _, s := range c.sites {
		if s.step.Kind() == kind {
			valid = append(valid, s.step.Label())
		}
	}

	text := fmt.Sprintf("unknown %s `%s`.", kind, name)
	if len(valid) == 0 {
		text += fmt.Sprintf(" The file has no %s.", kind)
	}

	c.add(at, text+hint(name, valid))
}

func (c *checker) output(tr hcl.Traversal, target *site) {
	kind, name := target.step.Kind(), target.step.Label()

	output, named := attribute(tr, attrOutput)
	valid, open := outputs(target.step)

	if !named {
		c.add(tr.SourceRange(), fmt.Sprintf("`%s.%s` must name an output. Write `%s.%s.<output>`.%s", kind, name, kind, name, hint("", valid)))

		return
	}

	if !open && !slices.Contains(valid, output) {
		c.add(tr.SourceRange(), fmt.Sprintf("%s `%s` has no output `%s`.%s", kind, name, output, hint(output, valid)))
	}
}

// outputs returns the sorted output names of a step. The second result is true when the step
// declares its outputs with an expression that is not a list of strings, so any name may be valid.
func outputs(step Step) ([]string, bool) {
	all, open := stepOutputs(step)
	if open {
		return nil, true
	}

	names := make([]string, 0, len(all))
	for _, o := range all {
		names = append(names, o.Name)
	}

	slices.Sort(names)

	return names, false
}

// declaredOutputs reads the names in an `outputs` argument that is a list of strings.
func declaredOutputs(expr hcl.Expression) ([]string, bool) {
	if unset(expr) {
		return nil, false
	}

	items, diags := hcl.ExprList(expr)
	if diags.HasErrors() {
		return nil, true
	}

	return literalStrings(items)
}

// literalStrings returns the text of each expression. The second result is true when one is not a plain string.
func literalStrings(items []hcl.Expression) ([]string, bool) {
	var names []string

	for _, item := range items {
		name, ok := literalString(item)
		if !ok {
			return nil, true
		}

		names = append(names, name)
	}

	return names, false
}

// literalString returns the text of an expression that is a string without any reference.
func literalString(expr hcl.Expression) (string, bool) {
	value, diags := expr.Value(nil)
	if diags.HasErrors() || value.IsNull() || !value.IsKnown() || value.Type().FriendlyName() != "string" {
		return "", false
	}

	return value.AsString(), true
}

func roots() []string {
	return []string{"agent", "command", rootEnv, "gate", rootItem, "loop", "merge", "wait"}
}

// CommonItemFields returns the item fields that every tracker declares. A check without a project uses them.
func CommonItemFields() []string {
	return []string{"id", "title", "body"}
}

// hint says which name is nearest to name and lists the valid names. It is empty when valid is empty.
func hint(name string, valid []string) string {
	if len(valid) == 0 {
		return ""
	}

	text := ""
	if near := nearest(name, valid); near != "" {
		text = fmt.Sprintf(" Did you mean `%s`?", near)
	}

	return text + " Use one of: " + strings.Join(valid, ", ") + "."
}

// nearest returns the name in valid that is closest to name, or "" when none is close.
func nearest(name string, valid []string) string {
	best, bestDistance := "", nearSlack+1

	for _, candidate := range valid {
		if d := distance(name, candidate); d < bestDistance && d < len(name) {
			best, bestDistance = candidate, d
		}
	}

	return best
}

// distance is the edit distance between two words, in single-letter inserts, deletes and changes.
func distance(a, b string) int {
	row := make([]int, len(b)+1)
	for j := range row {
		row[j] = j
	}

	for i := range len(a) {
		diagonal := row[0]
		row[0] = i + 1

		for j := range len(b) {
			cost := 1
			if a[i] == b[j] {
				cost = 0
			}

			diagonal, row[j+1] = row[j+1], min(row[j+1]+1, row[j]+1, diagonal+cost)
		}
	}

	return row[len(b)]
}

const (
	onChain = iota + 1
	finished
)

// cycles finds each loop of references, with a depth-first walk over the steps in file order.
func (c *checker) cycles() {
	w := &walker{c: c, state: map[*site]int{}}

	for _, s := range c.sites {
		if w.state[s] == 0 {
			w.visit(s)
		}
	}
}

// walker holds the state of the depth-first walk: the step being visited and the steps above it.
type walker struct {
	c     *checker
	state map[*site]int
	chain []*site
}

func (w *walker) visit(s *site) {
	w.state[s] = onChain
	w.chain = append(w.chain, s)

	for _, next := range s.edges {
		switch w.state[next] {
		case 0:
			w.visit(next)
		case onChain:
			w.c.cycle(w.chain, next)
		}
	}

	w.chain = w.chain[:len(w.chain)-1]
	w.state[s] = finished
}

// cycle reports the part of chain that starts at entry, closed back to entry.
func (c *checker) cycle(chain []*site, entry *site) {
	names := []string{}

	for _, s := range chain[slices.Index(chain, entry):] {
		names = append(names, s.step.Label())
	}

	names = append(names, entry.step.Label())

	c.add(entry.step.Pos(), fmt.Sprintf("cycle `%s`. Remove one of the references.", strings.Join(names, " -> ")))
}

// merges refuses a merge step that sets `merge = true` as a literal. A computed value is refused when the step runs.
func (c *checker) merges() {
	for _, s := range c.sites {
		step, ok := s.step.(*Merge)
		if !ok || step.Merge == nil {
			continue
		}

		if value, diags := step.Merge.Value(nil); !diags.HasErrors() && value.IsKnown() && !value.IsNull() && value.Type() == cty.Bool && value.True() {
			c.add(step.Merge.Range(), "merging is not built yet: the round settings that it needs come in R1. Remove `merge = true`, and the owner merges the pull request.")
		}
	}
}

// prompts checks the prompt file of each agent. A prompt that an expression computes is not checked.
func (c *checker) prompts() {
	for _, s := range c.sites {
		if agent, ok := s.step.(*Agent); ok {
			if name, literal := literalString(agent.Prompt); literal {
				c.promptFile(agent.Prompt.Range(), name)
			}
		}
	}
}

// promptFile checks that the file exists next to the workflow file and parses as a template.
func (c *checker) promptFile(at hcl.Range, name string) {
	if !filepath.IsLocal(name) {
		c.add(at, fmt.Sprintf("prompt file `%s` must be a relative path inside the workflow folder. Keep the file in that folder or below it.", name))

		return
	}

	full := path.Join(path.Dir(c.flow.File), name)

	data, err := fs.ReadFile(c.fsys, full)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.add(at, fmt.Sprintf("prompt file `%s` is missing. Create it next to the workflow file or fix the path.", name))
	case err != nil:
		c.add(at, fmt.Sprintf("cannot read prompt file `%s`: %v. Check the path and the permissions.", name, err))
	default:
		if _, err := template.New(name).Parse(string(data)); err != nil {
			c.add(at, fmt.Sprintf("prompt file `%s` is not a valid template: %v. Fix the template.", name, err))
		}
	}
}
