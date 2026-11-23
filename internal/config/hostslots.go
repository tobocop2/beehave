package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

// className is the name of a slot class. It is a plain word, because the engine names the queue of the class with it.
var className = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// slotTable is the slots argument of the host block: a dict of slot class names to counts. The range of the counts is checked later, with the position of the block.
type slotTable map[string]starlark.Int

// Unpack stores a dict of class names to whole numbers. It refuses any other value, and a count that is not a whole number names its class.
func (t *slotTable) Unpack(v starlark.Value) error {
	dict, ok := v.(*starlark.Dict)
	if !ok {
		return fmt.Errorf("`host.slots`: got %s, want a dict of class names to counts", v.Type())
	}

	*t = slotTable{}

	for _, item := range dict.Items() {
		class, count, err := slotEntry(item)
		if err != nil {
			return err
		}

		(*t)[class] = count
	}

	return nil
}

// slotEntry reads one entry of the dict: a class name and a whole number.
func slotEntry(item starlark.Tuple) (string, starlark.Int, error) {
	class, isText := starlark.AsString(item[0])
	if !isText {
		return "", starlark.Int{}, fmt.Errorf("`host.slots` has the key %s, want a class name", item[0].String())
	}

	count, isInt := item[1].(starlark.Int)
	if !isInt {
		return "", starlark.Int{}, fmt.Errorf("`host.slots` class %q holds a %s, want a whole number above zero", class, item[1].Type())
	}

	return class, count, nil
}

// slotsOf checks the slot classes of a host block and returns them. A block that names none gives nil.
func slotsOf(at syntax.Position, table slotTable) (map[string]int, error) {
	if len(table) == 0 {
		return nil, nil //nolint:nilnil // a host that names no class has no slot map
	}

	slots := make(map[string]int, len(table))

	for _, class := range slices.Sorted(maps.Keys(table)) {
		if err := checkClass(at, class); err != nil {
			return nil, err
		}

		count, fits := table[class].Int64()
		if !fits || count < 1 || count > maxCount {
			return nil, refuseAt(at, "`host.slots` class %q must be above zero and at most %d; got %s. Fix the value.", class, maxCount, table[class])
		}

		slots[class] = int(count)
	}

	return slots, nil
}

// checkClass refuses the name of the class of the gates and a name that is not a plain word.
func checkClass(at syntax.Position, class string) error {
	if class == model.GateClass {
		return refuseAt(at, "`host.slots` has the class %q, which belongs to the gates. Set the number of gates in `host.gate_slots`.", class)
	}

	if !className.MatchString(class) {
		return refuseAt(at, "`host.slots` has the class %q. Use letters, digits, dot, underscore and hyphen, and start with a letter or a digit.", class)
	}

	return nil
}

// SlotCount returns the number of jobs of one slot class that a build host runs at once, and whether any project names the class on the host.
// Projects share a host by name. The config load refuses two projects that give different counts for one class on one host, so every project that names the class gives the same count.
func SlotCount(projects map[string]Project, cluster, class string) (int, bool) {
	for _, name := range slices.Sorted(maps.Keys(projects)) {
		project := projects[name]
		if project.Host == nil {
			continue
		}

		if _, err := project.HostFor(cluster); err == nil && withGate(project.Host)[class] > 0 {
			return withGate(project.Host)[class], true
		}
	}

	return 0, false
}

// slotProblems refuses two projects that name one host and give different counts for one slot class. A host has one capacity, so the projects cannot
// each set it. Equal counts are fine.
func (c Config) slotProblems() Problems {
	var problems Problems

	seen := map[hostClass]slotClaim{}

	for _, name := range slices.Sorted(maps.Keys(c.Projects)) {
		project := c.Projects[name]
		if project.Host == nil {
			continue
		}

		for _, host := range declaredOrDefault(project) {
			problems = append(problems, claimSlots(seen, name, host, withGate(project.Host))...)
		}
	}

	return problems
}

// withGate is the slot counts of a host block with the gate slots added under the reserved class. A block with no gate slots adds none.
func withGate(host *model.HostConfig) map[string]int {
	counts := maps.Clone(host.Slots)

	if host.GateSlots < 1 {
		return counts
	}

	if counts == nil {
		counts = map[string]int{}
	}

	counts[model.GateClass] = host.GateSlots

	return counts
}

// declaredOrDefault is every build host that a project declares: its list, or its one host.
func declaredOrDefault(project Project) []string {
	if len(project.BuildHosts) == 0 {
		return []string{project.BuildHost}
	}

	return project.BuildHosts
}

// hostClass is one slot class on one build host.
type hostClass struct{ host, class string }

// slotClaim is the count that a project gives to a hostClass.
type slotClaim struct {
	project string
	count   int
}

// claimSlots records the counts that a project gives on a host in seen, and refuses a count that differs from the count of an earlier project.
func claimSlots(seen map[hostClass]slotClaim, project, host string, slots map[string]int) Problems {
	var problems Problems

	for _, class := range slices.Sorted(maps.Keys(slots)) {
		key := hostClass{host, class}

		first, known := seen[key]
		if !known {
			seen[key] = slotClaim{project, slots[class]}

			continue
		}

		if first.count != slots[class] {
			problems = append(problems, refuse("projects %s and %s both use host %s and give a different count for the slot class %q: %d and %d. A host has one count for each class. Give both projects the same count in `slots` of the `host` block.",
				first.project, project, host, class, first.count, slots[class]))
		}
	}

	return problems
}
