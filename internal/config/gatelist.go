package config

import (
	"fmt"
	"regexp"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/model"
)

// stepName is the name of a gate step. It is a plain word, so the host job can write it into a marker line.
var stepName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// reservedStep is the name that the test report step of a gate takes. A gate list may not use it.
const reservedStep = "tests"

// gateList is the gate_steps argument: a dict of step names to command lines. The dict keeps its order, and the steps run in that order.
type gateList []model.GateSpec

// Unpack stores a dict of strings in the order of the dict. It refuses any other value.
func (l *gateList) Unpack(v starlark.Value) error {
	dict, ok := v.(*starlark.Dict)
	if !ok {
		return fmt.Errorf("got %s, want a dict of step names to commands", v.Type())
	}

	*l = nil

	for _, item := range dict.Items() {
		name, nameOK := starlark.AsString(item[0])
		command, commandOK := starlark.AsString(item[1])

		if !nameOK || !commandOK {
			return fmt.Errorf("the entry %s holds a %s and a %s, want a step name and a command", item[0].String(), item[0].Type(), item[1].Type())
		}

		*l = append(*l, model.GateSpec{Name: name, Command: command})
	}

	return nil
}

// gateKeys checks the gate of a project: exactly one of gate and gate_steps, and a valid list when it is gate_steps.
func gateKeys(raw rawProject) error {
	if len(raw.GateSteps) == 0 {
		return requireText(raw.At, "gate", raw.Gate)
	}

	if raw.Gate != "" {
		return refuseAt(raw.At, "`gate` and `gate_steps` are both set. Set one of them: `gate_steps` lists the steps of the gate, each with its own command.")
	}

	for _, spec := range raw.GateSteps {
		if err := checkSpec(raw, spec); err != nil {
			return err
		}
	}

	return nil
}

// checkSpec refuses a step with a name that is not a plain word, the reserved name, or a blank command. The dict has no repeated name.
func checkSpec(raw rawProject, spec model.GateSpec) error {
	switch {
	case !stepName.MatchString(spec.Name):
		return refuseAt(raw.At, "`gate_steps` has the step name %q. Use letters, digits, dot, underscore and hyphen, and start with a letter or a digit.", spec.Name)
	case spec.Name == reservedStep:
		return refuseAt(raw.At, "`gate_steps` names a step %q. The test report of `test_report` takes that name. Use another name.", reservedStep)
	case blank(spec.Command):
		return refuseAt(raw.At, "`gate_steps` step %q has a blank command. Write the command or remove the step.", spec.Name)
	}

	return nil
}
