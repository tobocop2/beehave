package config

import (
	"fmt"
	"regexp"
	"slices"
	"time"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/model"
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
		spec, err := stepSpec(item[0], item[1])
		if err != nil {
			return err
		}

		*l = append(*l, spec)
	}

	return nil
}

// stepSpec reads one entry of the dict: a step name and a command, or a step name and a dict with the keys `command` and `timeout`.
func stepSpec(key, value starlark.Value) (model.GateSpec, error) {
	name, ok := starlark.AsString(key)
	if !ok {
		return model.GateSpec{}, fmt.Errorf("the entry %s holds a %s as its name, want a step name", key.String(), key.Type())
	}

	if command, isText := starlark.AsString(value); isText {
		return model.GateSpec{Name: name, Command: command}, nil
	}

	dict, isDict := value.(*starlark.Dict)
	if !isDict {
		return model.GateSpec{}, fmt.Errorf("the step %q holds a %s, want a command or a dict with `command` and `timeout`", name, value.Type())
	}

	return dictSpec(name, dict)
}

// dictSpec reads the dict form of a step. It refuses a key other than `command` and `timeout`, and a dict with no `command`.
func dictSpec(name string, dict *starlark.Dict) (model.GateSpec, error) {
	fields, err := dictFields(name, dict)
	if err != nil {
		return model.GateSpec{}, err
	}

	text, ok := starlark.AsString(fields.command)
	if !ok {
		return model.GateSpec{}, fmt.Errorf("the step %q needs a `command` that is a string", name)
	}

	limit, err := stepTimeout(name, fields.timeout)

	return model.GateSpec{Name: name, Command: text, Timeout: limit}, err
}

// stepFields holds the values of the keys `command` and `timeout` of a step dict. A key that is not there leaves its value nil.
type stepFields struct {
	command, timeout starlark.Value
}

// dictFields reads the keys of a step dict and refuses any key other than `command` and `timeout`.
func dictFields(name string, dict *starlark.Dict) (stepFields, error) {
	var fields stepFields

	for _, item := range dict.Items() {
		switch key, _ := starlark.AsString(item[0]); key {
		case "command":
			fields.command = item[1]
		case "timeout":
			fields.timeout = item[1]
		default:
			return stepFields{}, fmt.Errorf("the step %q has the key %s, want only `command` and `timeout`", name, item[0].String())
		}
	}

	return fields, nil
}

// stepTimeout reads the `timeout` of a step: a duration of at least one second, such as "10m". A step with no `timeout` has no limit of its own.
func stepTimeout(name string, value starlark.Value) (time.Duration, error) {
	if value == nil {
		return 0, nil
	}

	text, ok := starlark.AsString(value)
	if !ok {
		return 0, fmt.Errorf("the `timeout` of the step %q holds a %s, want a duration string such as \"10m\"", name, value.Type())
	}

	parsed, err := time.ParseDuration(text)
	if err != nil || parsed < time.Second {
		return 0, fmt.Errorf("the `timeout` of the step %q must be a duration of at least 1s, such as \"10m\"; got `%s`", name, text)
	}

	return parsed, nil
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
	case slices.Contains(model.ReservedGateSteps(), spec.Name):
		return refuseAt(raw.At, "`gate_steps` names a step %q. The gate job of a candidate head takes that name. Rename the step.", spec.Name)
	case blank(spec.Command):
		return refuseAt(raw.At, "`gate_steps` step %q has a blank command. Write the command or remove the step.", spec.Name)
	}

	return nil
}
