package workflow

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	"github.com/tobocop2/obee/internal/hclfile"
	"github.com/tobocop2/obee/internal/model"
)

const (
	rootItem  = "item"
	rootEnv   = "env"
	rootLocal = "local"

	roundName    = "round"
	previousName = "previous"
	previousRoot = "loop.previous."

	// attrOutput is the place of the output name in a reference such as agent.<name>.<output>.
	attrOutput = 2
	// The places of the parts of loop.previous.<kind>.<name>.<output>.
	attrPreviousKind   = 2
	attrPreviousName   = 3
	attrPreviousOutput = 4
	// nearSlack is the edit distance that a short name may be from its nearest valid name.
	nearSlack = 2
)

// Check reads the workflow file at file from fsys and returns every problem in it, in position order.
// The second result is true when the file could not be read or parsed, and no other check ran.
// A file with structural problems still gets the reference, cycle and file checks on the steps that loaded.
// A reference to a step that failed to load is not reported again.
// caps says what the project offers: the item fields and whether a forge exists. A step that needs a forge is refused when none exists.
func Check(fsys fs.FS, file string, caps Capabilities) ([]hclfile.Diagnostic, bool) {
	body, err := hclfile.Parse(fsys, file)
	if err != nil {
		return diagnostics(err), true
	}

	flow, names, err := build(file, body)

	valid := append(slices.Clone(caps.Fields), "kind")
	slices.Sort(valid)

	c := &checker{fsys: fsys, flow: flow, byName: map[string]*site{}, declared: names, caps: caps, fields: slices.Compact(valid), found: diagnostics(err)}

	c.run()

	return c.found, false
}

// run does every check and sorts the problems by position.
func (c *checker) run() {
	c.index(c.flow.Steps, nil)
	c.indexLocals(c.flow.Locals)
	c.references()
	c.localDeclarations()
	c.cycles()
	c.arguments()
	sortByPosition(c.found)
}

// Capabilities is what a project offers to a workflow.
type Capabilities struct {
	// Project is the name of the project, for the text of a refusal.
	Project string
	// Fields lists the names that the tracker and the forge declare for an item. The field `kind` is always valid.
	Fields []string
	// ForgeOnly lists the item fields that only a forge declares. A project with no forge names them in the hint of an unknown field.
	ForgeOnly []string
	// Forge is true when the project has a forge.
	Forge bool
}

// site is one step with its place in the walk and the steps it depends on.
type site struct {
	step      Step
	index     int
	ancestors []Step
	edges     []*site
	// local is true for the place of a local. It has no step, and its ancestors are the loops that can see the local, the loop that holds it last.
	local bool
}

// inLoop reports whether `loop.round` has a meaning for the step. The condition of a loop is inside it.
func (s *site) inLoop() bool {
	return len(s.ancestors) > 0 || s.isLoop()
}

// isLoop is true for the site of a loop block.
func (s *site) isLoop() bool { return !s.local && s.step.Kind() == KindLoop }

// container returns the name of the closest loop around the step, or "" for a step outside every loop. A step name is unique in the file.
func (s *site) container() string {
	if len(s.ancestors) == 0 {
		return ""
	}

	return s.ancestors[len(s.ancestors)-1].Label()
}

// home returns the name of the loop that `loop.previous` reads for the step: the loop itself for its condition, the closest loop around it otherwise.
func (s *site) home() string {
	if s.isLoop() {
		return s.step.Label()
	}

	return s.container()
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
	caps     Capabilities
	// locals holds every local of the file, in file order.
	locals []*localSite
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

			c.functions(arg.expr)
		}
	}
}

// functions reports each call of a function that the language does not offer.
func (c *checker) functions(expr hcl.Expression) {
	for _, d := range unknownFunctions(expr) {
		c.add(*d.Subject, d.Summary)
	}
}

func (c *checker) resolve(from *site, tr hcl.Traversal, whole bool) {
	root := tr.RootName()
	name, named := attribute(tr, 1)

	switch {
	case slices.Contains(stepKinds(), Kind(root)):
		if named {
			c.step(from, tr, Kind(root), name, whole)
		} else {
			c.add(tr.SourceRange(), fmt.Sprintf("`%s` needs a step name. Write `%s.<name>.<output>`.", root, root))
		}
	case Kind(root) == KindLoop:
		c.loop(from, tr, name, named, whole)
	case root == rootLocal:
		c.read(from, tr, name, named)
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
		c.add(at, fmt.Sprintf("`item` has no field `%s`.%s%s", field, c.noForgeHint(field), hint(field, c.fields)))
	}
}

// noForgeHint says that the project has no forge when the field is one that only a forge declares.
func (c *checker) noForgeHint(field string) string {
	if c.caps.Forge || !slices.Contains(c.caps.ForgeOnly, field) {
		return ""
	}

	return fmt.Sprintf(" The project has no forge, and only a forge declares `%s`.", field)
}

func (c *checker) loop(from *site, tr hcl.Traversal, next string, named, whole bool) {
	at := tr.SourceRange()

	switch {
	case !named:
		c.add(at, "`loop` needs `round`, `previous` or a loop name. Write `loop.round`, `loop.previous.<kind>.<name>.<output>` or `loop.<name>.exhausted`.")
	case next == roundName && from.inLoop():
	case next == roundName:
		c.add(at, "`loop.round` is used outside a loop. Put the step in a `loop` block.")
	case next == previousName:
		c.previous(from, tr, whole)
	default:
		c.step(from, tr, KindLoop, next, whole)
	}
}

// previous checks loop.previous.<kind>.<name>.<output>, the output that a step of the loop around the reference recorded in the round before.
// The read follows no order, and it is no dependency: the round before has run. So the step may be the reference itself or a later step, and no cycle follows.
func (c *checker) previous(from *site, tr hcl.Traversal, whole bool) {
	at := tr.SourceRange()
	kindName, _ := attribute(tr, attrPreviousKind)
	name, named := attribute(tr, attrPreviousName)
	kind := Kind(kindName)

	switch {
	case !from.inLoop():
		c.add(at, "`loop.previous` is used outside a loop. Put the step in a `loop` block.")
	case whole:
		c.add(at, "`resume_from` names an agent of this round. Write `agent.<name>`; `loop.previous` is no agent.")
	case !named || !isStepKind(kind):
		c.add(at, "`loop.previous` needs a step. Write `loop.previous.<kind>.<name>.<output>`.")
	default:
		c.previousStep(from, tr, kind, name)
	}
}

func (c *checker) previousStep(from *site, tr hcl.Traversal, kind Kind, name string) {
	at := tr.SourceRange()

	target := c.byName[name]
	if target == nil || target.step.Kind() != kind {
		c.unknown(at, kind, name)

		return
	}

	if target.container() != from.home() {
		c.add(at, fmt.Sprintf("`loop.previous` reads a step of the same loop, and `%s.%s` is not in this loop. Name a step of this loop.", kind, name))

		return
	}

	c.output(tr, target, attrPreviousOutput)
}

// isStepKind reports whether kind names a step that has outputs.
func isStepKind(kind Kind) bool {
	return kind == KindLoop || slices.Contains(stepKinds(), kind)
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
		c.output(tr, target, attrOutput)
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
	if reaches(from, target) {
		c.add(at, fmt.Sprintf("`%s.%s` runs later than this reference. Move the step up or remove the reference.", target.step.Kind(), target.step.Label()))
	}
}

// reaches records that from depends on target, and reports whether target runs later than from.
// A local has no place in the order. Each site that reads it is checked against the steps it reads.
func reaches(from, target *site) bool {
	if from.local {
		return false
	}

	if !slices.Contains(from.ancestors, target.step) {
		from.depend(target)
	}

	return target.index > from.index && !slices.Contains(target.ancestors, from.step)
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

// output checks the output name that a reference holds at place.
func (c *checker) output(tr hcl.Traversal, target *site, place int) {
	kind, name := target.step.Kind(), target.step.Label()

	output, named := attribute(tr, place)
	valid, open := outputs(target.step)

	if !named {
		reference := fmt.Sprintf("%s.%s", kind, name)
		if place == attrPreviousOutput {
			reference = previousRoot + reference
		}

		c.add(tr.SourceRange(), fmt.Sprintf("`%s` must name an output. Write `%s.<output>`.%s", reference, reference, hint("", valid)))

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
	return []string{"agent", "command", rootEnv, "gate", rootItem, rootLocal, "loop", "merge", "wait"}
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
	findCycles(c.sites, func(s *site) []*site { return s.edges }, c.cycle)
}

// findCycles walks nodes depth first, in order, and calls report with the chain above a node and the node it points back at.
func findCycles[T comparable](nodes []T, edges func(T) []T, report func(chain []T, entry T)) {
	w := &walker[T]{state: map[T]int{}, edges: edges, report: report}

	for _, n := range nodes {
		if w.state[n] == 0 {
			w.visit(n)
		}
	}
}

// walker holds the state of the depth-first walk: the node being visited and the nodes above it.
type walker[T comparable] struct {
	state  map[T]int
	chain  []T
	edges  func(T) []T
	report func(chain []T, entry T)
}

func (w *walker[T]) visit(n T) {
	w.state[n] = onChain
	w.chain = append(w.chain, n)

	for _, next := range w.edges(n) {
		switch w.state[next] {
		case 0:
			w.visit(next)
		case onChain:
			w.report(w.chain, next)
		}
	}

	w.chain = w.chain[:len(w.chain)-1]
	w.state[n] = finished
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

// merges checks the settings of each merge step. A setting that an expression computes is checked when the step runs.
func (c *checker) merges() {
	for _, s := range c.sites {
		step, ok := s.step.(*Merge)
		if !ok {
			continue
		}

		c.mergeNeeds(step)
		c.hold(step.Hold)
		c.forbidText(step.ForbidText)
		c.own(step.Own)
		c.approved(step.Approved)
		c.choice(step.Children, "children", []string{"refuse", "retarget"})
	}
}

// mergeNeeds refuses a step that sets `merge = true` as a literal and has no `repo`, `base` or `own`.
func (c *checker) mergeNeeds(step *Merge) {
	if !mergesLiterally(step) {
		return
	}

	for _, setting := range []struct {
		name string
		expr hcl.Expression
	}{{"repo", step.Repo}, {"base", step.Base}, {"own", step.Own}} {
		if unset(setting.expr) {
			c.add(step.Pos(), fmt.Sprintf("the merge step sets `merge = true` and has no `%s`. Add `%s`.", setting.name, setting.name))
		}
	}
}

// mergesLiterally is true when the step sets `merge = true` as a literal.
func mergesLiterally(step *Merge) bool {
	value, diags := step.Merge.Value(nil)

	return !diags.HasErrors() && value.IsKnown() && !value.IsNull() && value.Type() == cty.Bool && value.True()
}

// approved refuses a value that reads nothing, such as `approved = true`, and a value that reads no output of an agent step.
// The verdict must come from an agent step, such as agent.review.verdict == "approve".
func (c *checker) approved(expr hcl.Expression) {
	if unset(expr) {
		return
	}

	vars := expr.Variables()
	if len(vars) == 0 {
		c.add(expr.Range(), "approved reads nothing, so it approves every head. Read the verdict of another step, such as agent.review.verdict == \"approve\".")

		return
	}

	if !slices.ContainsFunc(vars, func(v hcl.Traversal) bool { return v.RootName() == "agent" }) {
		c.add(expr.Range(), "approved reads no agent output, so no reviewer decides it. Read the verdict of an agent step, such as agent.review.verdict == \"approve\".")
	}
}

// hold refuses a label with a comma, because the forge call joins labels with commas.
func (c *checker) hold(expr hcl.Expression) {
	if label, literal := literalString(expr); literal && strings.Contains(label, ",") {
		c.add(expr.Range(), fmt.Sprintf("hold `%s` has a comma, and the forge joins labels with commas. Name one label.", label))
	}
}

// forbidText checks `forbid_text`: a list, and each plain string in it a non-empty RE2 pattern.
func (c *checker) forbidText(expr hcl.Expression) {
	items, diags := hcl.ExprList(expr)
	if diags.HasErrors() {
		if value, err := expr.Value(nil); !err.HasErrors() && !value.IsNull() {
			c.add(expr.Range(), "forbid_text must be a list of patterns. Write forbid_text = [\"(?i)do not merge\"].")
		}

		return
	}

	for _, item := range items {
		if pattern, literal := literalString(item); literal {
			c.pattern(item.Range(), pattern)
		} else if notString(item) {
			c.add(item.Range(), "forbid_text must hold strings only. Write each item as a string pattern.")
		}
	}
}

// notString is true when the expression is a literal that is not a string and not null.
func notString(expr hcl.Expression) bool {
	value, diags := expr.Value(nil)

	return !diags.HasErrors() && value.IsKnown() && !value.IsNull() && value.Type() != cty.String
}

// own checks `own`: an object with the keys `logins` and `emails`, each a list of strings, and not both empty.
// A value that an expression computes is checked when the step runs.
func (c *checker) own(expr hcl.Expression) {
	if unset(expr) {
		return
	}

	pairs, diags := hcl.ExprMap(expr)
	if diags.HasErrors() {
		if value, err := expr.Value(nil); !err.HasErrors() {
			c.add(expr.Range(), fmt.Sprintf("own is %s. Write own = { logins = [\"name\"], emails = [\"name@example.org\"] }.", describe(value)))
		}

		return
	}

	sizes, typo := c.ownPairs(pairs)

	// A typo already names the key it should be, so the key it replaced is not reported again.
	if !typo {
		c.ownComplete(expr, sizes)
	}
}

// ownKeys returns the keys of `own`.
func ownKeys() []string { return []string{"logins", "emails"} }

// ownPairs checks each key of `own` and returns the length of the list under each known key. It says whether a key is unknown.
func (c *checker) ownPairs(pairs []hcl.KeyValuePair) (map[string]int, bool) {
	sizes := map[string]int{}
	typo := false

	for _, pair := range pairs {
		key, literal := literalString(pair.Key)

		switch {
		case !literal: // A computed key is read when the step runs.
		case slices.Contains(ownKeys(), key):
			sizes[key] = c.identities(key, pair.Value)
		default:
			c.add(pair.Key.Range(), fmt.Sprintf("own has the unknown key `%s`.%s", key, hint(key, ownKeys())))

			typo = true
		}
	}

	return sizes, typo
}

// ownComplete refuses an `own` that lacks a key, or has two empty lists.
func (c *checker) ownComplete(expr hcl.Expression, sizes map[string]int) {
	empty := 0

	for _, key := range ownKeys() {
		size, ok := sizes[key]
		if !ok {
			c.add(expr.Range(), fmt.Sprintf("own has no `%s`. Add `%s = [...]`.", key, key))
		}

		if ok && size == 0 {
			empty++
		}
	}

	if empty == len(ownKeys()) {
		c.add(expr.Range(), "own has two empty lists, so it names nobody. Add a login and an address.")
	}
}

// identities checks the list under one key of `own` and returns its length, or -1 when an expression computes the list.
func (c *checker) identities(key string, expr hcl.Expression) int {
	items, diags := hcl.ExprList(expr)
	if diags.HasErrors() {
		if value, err := expr.Value(nil); !err.HasErrors() && !value.IsNull() {
			c.add(expr.Range(), fmt.Sprintf("`%s` of own must be a list of strings. Write %s = [\"name\"].", key, key))
		}

		return -1
	}

	for _, item := range items {
		if notString(item) {
			c.add(item.Range(), fmt.Sprintf("`%s` of own must hold strings only.", key))
		}
	}

	return len(items)
}

// describe names the type of a literal value for a message.
func describe(value cty.Value) string {
	return "a " + value.Type().FriendlyName() + ", not an object"
}

// pattern refuses a `forbid_text` pattern that is empty or not RE2.
func (c *checker) pattern(at hcl.Range, pattern string) {
	if pattern == "" {
		c.add(at, "forbid_text has an empty pattern, which matches every line. Remove it or write a pattern.")

		return
	}

	if _, err := regexp.Compile(pattern); err != nil {
		c.add(at, fmt.Sprintf("forbid_text has the pattern `%s`, which is not valid RE2: %v. Fix the pattern.", pattern, err))
	}
}

// choice checks that a plain string setting is one of valid.
func (c *checker) choice(expr hcl.Expression, name string, valid []string) {
	if value, literal := literalString(expr); literal && !slices.Contains(valid, value) {
		c.add(expr.Range(), fmt.Sprintf("%s has the unknown value `%s`.%s", name, value, hint(value, valid)))
	}
}

// arguments checks the arguments of each step that no reference check covers: prompt files, forbidden kinds and the merge settings.
func (c *checker) arguments() {
	c.prompts()
	c.forbids()
	c.forges()
	c.commands()
	c.merges()
}

// forges refuses each step that needs a forge when the project has none. The refusal comes before the settings of the same step.
func (c *checker) forges() {
	if c.caps.Forge {
		return
	}

	for _, s := range c.sites {
		if s.step.NeedsForge() {
			c.add(s.step.Pos(), fmt.Sprintf("step `%s.%s` needs a forge, and project `%s` has none. Set `forge = \"github\"` in the project file, or remove the step.", s.step.Kind(), s.step.Label(), c.caps.Project))
		}
	}
}

// forbids checks the kinds that each agent lists in `forbid_commands`. A kind that an expression computes is checked when the step runs.
func (c *checker) forbids() {
	valid := model.FindingKindNames()

	for _, s := range c.sites {
		if agent, ok := s.step.(*Agent); ok && !unset(agent.ForbidCommands) {
			c.forbid(agent.ForbidCommands, valid)
		}
	}
}

// forbid checks one `forbid_commands` argument: it must be a list, and each plain string in it must be a kind.
func (c *checker) forbid(expr hcl.Expression, valid []string) {
	items, diags := hcl.ExprList(expr)
	if diags.HasErrors() {
		if value, err := expr.Value(nil); !err.HasErrors() && !value.IsNull() {
			c.add(expr.Range(), "forbid_commands must be a list of kinds."+hint("", valid)+" Write forbid_commands = [\"bulk-staging\"].")
		}

		return
	}

	for _, item := range items {
		if name, literal := literalString(item); literal && !slices.Contains(valid, name) {
			c.add(item.Range(), fmt.Sprintf("unknown kind `%s`.%s", name, hint(name, valid)))
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
