package workflow

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
)

// localSite is one local with the place that checks its expression.
type localSite struct {
	local Local
	// owner is the loop that holds the local, or nil for a local of the file.
	owner Step
	// place stands for the expression of the local. It is no step: it has no place in the order.
	place *site
}

// indexLocals lists the locals of the file and then those of each loop, in file order.
func (c *checker) indexLocals(file []Local) {
	c.addLocals(file, nil, nil)

	for _, s := range c.sites {
		if loop, ok := s.step.(*Loop); ok {
			c.addLocals(loop.Locals, loop, append(slices.Clone(s.ancestors), loop))
		}
	}
}

func (c *checker) addLocals(locals []Local, owner Step, ancestors []Step) {
	for _, l := range locals {
		c.locals = append(c.locals, &localSite{local: l, owner: owner, place: &site{ancestors: ancestors, local: true}})
	}
}

// loopsOf returns the loops whose locals the expressions of the site can read: the loops around it and, for a loop, itself.
func loopsOf(s *site) []Step {
	if s.isLoop() {
		return append(slices.Clone(s.ancestors), s.step)
	}

	return s.ancestors
}

// sees reports whether an expression at from can read the local.
func (c *checker) sees(from *site, ls *localSite) bool {
	return ls.owner == nil || slices.Contains(loopsOf(from), ls.owner)
}

// lookup returns the local that a read at from names. When the name belongs only to a loop that does not enclose from, it returns that local second.
func (c *checker) lookup(from *site, name string) (visible, hidden *localSite) {
	for _, ls := range c.locals {
		switch {
		case ls.local.Name != name:
		case c.sees(from, ls):
			return ls, nil
		default:
			hidden = ls
		}
	}

	return nil, hidden
}

// visibleNames returns the names of the locals that a read at from can name.
func (c *checker) visibleNames(from *site) []string {
	var names []string

	for _, ls := range c.locals {
		if c.sees(from, ls) {
			names = append(names, ls.local.Name)
		}
	}

	slices.Sort(names)

	return names
}

// read checks a reference `local.<name>` at from.
func (c *checker) read(from *site, tr hcl.Traversal, name string, named bool) {
	at := tr.SourceRange()

	if !named {
		c.add(at, "`local` needs a name. Write `local.<name>`."+hint("", c.visibleNames(from)))

		return
	}

	visible, hidden := c.lookup(from, name)

	switch {
	case visible != nil:
		c.localOrder(from, visible, at)
	case hidden != nil:
		c.add(at, fmt.Sprintf("local `%s` belongs to loop `%s` and is read outside it. Read it inside that loop, or declare it at the top of the file.", name, hidden.owner.Label()))
	default:
		c.add(at, fmt.Sprintf("unknown local `%s`.%s", name, c.noLocal(from, name)))
	}
}

// noLocal is the end of the refusal of a name that names no local.
func (c *checker) noLocal(from *site, name string) string {
	names := c.visibleNames(from)
	if len(names) == 0 {
		return " No local is visible here. Declare it in a `locals` block."
	}

	return hint(name, names)
}

// localOrder refuses a read of a local, at from, that reads a step that runs later than from.
// It also records that from depends on the steps the local reads, so a cycle through a local shows.
func (c *checker) localOrder(from *site, ls *localSite, at hcl.Range) {
	var late []*site

	for _, target := range c.localSteps(ls, map[*localSite]bool{}) {
		if reaches(from, target) && !slices.Contains(late, target) {
			late = append(late, target)
			c.add(at, fmt.Sprintf("`local.%s` reads `%s.%s`, which runs later than this reference. Move the step up or remove the reference.", ls.local.Name, target.step.Kind(), target.step.Label()))
		}
	}
}

// localSteps returns the steps that the expression of a local reads, directly or through other locals.
func (c *checker) localSteps(ls *localSite, seen map[*localSite]bool) []*site {
	if seen[ls] {
		return nil
	}

	seen[ls] = true

	var found []*site

	for _, tr := range ls.local.Expr.Variables() {
		found = append(found, c.stepsOf(ls, tr, seen)...)
	}

	return found
}

// stepsOf returns the steps that one reference of the expression of ls reads: the step it names, or the steps of the local it names.
func (c *checker) stepsOf(ls *localSite, tr hcl.Traversal, seen map[*localSite]bool) []*site {
	if tr.RootName() == rootLocal {
		name, _ := attribute(tr, 1)

		if next, _ := c.lookup(ls.place, name); next != nil {
			return c.localSteps(next, seen)
		}

		return nil
	}

	kind, name, named := stepReference(tr)
	if target := c.byName[name]; named && target != nil && target.step.Kind() == kind {
		return []*site{target}
	}

	return nil
}

// localDeclarations checks the expression of each local the way the arguments of a step are checked, and then looks for a loop of locals.
func (c *checker) localDeclarations() {
	for _, ls := range c.locals {
		for _, tr := range ls.local.Expr.Variables() {
			c.resolve(ls.place, tr, false)
		}

		c.functions(ls.local.Expr)
	}

	findCycles(c.locals, c.localEdges, c.localCycle)
}

// localEdges returns the locals that the expression of ls reads.
func (c *checker) localEdges(ls *localSite) []*localSite {
	var next []*localSite

	for _, tr := range ls.local.Expr.Variables() {
		if tr.RootName() != rootLocal {
			continue
		}

		name, _ := attribute(tr, 1)
		if visible, _ := c.lookup(ls.place, name); visible != nil {
			next = append(next, visible)
		}
	}

	return next
}

// localCycle reports the part of chain that starts at entry, closed back to entry.
func (c *checker) localCycle(chain []*localSite, entry *localSite) {
	names := []string{}

	for _, ls := range chain[slices.Index(chain, entry):] {
		names = append(names, ls.local.Name)
	}

	names = append(names, entry.local.Name)

	c.add(entry.local.Range, fmt.Sprintf("cycle `%s`. Remove one of the references.", strings.Join(names, " -> ")))
}
