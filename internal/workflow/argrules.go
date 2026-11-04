package workflow

import (
	"errors"
	"fmt"
	"slices"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/model"
)

// The functions of this file own the rules for the text and list arguments of the command, sandbox and item builtins.
// The run and the check both call them.

// NotEmpty returns an error that names the argument when its value is empty.
func NotEmpty(name, value string) error {
	if value == "" {
		return errors.New(name + " must not be empty")
	}

	return nil
}

// filledRule is the rule of a text keyword that must not be empty.
func filledRule(name string) func(starlark.Value) error {
	return func(v starlark.Value) error {
		text, _ := starlark.AsString(v) // the unpack check has refused a value that is not text

		return NotEmpty(name, text)
	}
}

// filled is a required text keyword that must not be empty.
func filled(name string) Param {
	return Param{Name: name, Kind: KindText, Required: true, Rule: filledRule(name)}
}

// EnvAssignments reads the env dict of a command or write call as NAME=value pairs, sorted by name. An absent dict is empty.
func EnvAssignments(v starlark.Value) ([]string, error) {
	dict, ok := Optional[*starlark.Dict](v)
	if !ok {
		return nil, errors.New("env must be a dict of strings. Write env = {\"NAME\": \"value\"}")
	}

	if dict == nil {
		return nil, nil
	}

	return envPairs(dict)
}

// envPairs makes the sorted NAME=value pairs of an env dict.
func envPairs(dict *starlark.Dict) ([]string, error) {
	var pairs []string

	for _, pair := range dict.Items() {
		assignment, err := envAssignment(pair[0], pair[1])
		if err != nil {
			return nil, err
		}

		pairs = append(pairs, assignment)
	}

	slices.Sort(pairs)

	return pairs, nil
}

// envAssignment makes one NAME=value pair of an env dict.
func envAssignment(key, val starlark.Value) (string, error) {
	name, nameOK := starlark.AsString(key)
	value, valueOK := starlark.AsString(val)

	switch {
	case !nameOK || !valueOK:
		return "", fmt.Errorf("env must hold strings only, and %s is not. Write the value in quotes", key)
	case ReservedEnvName(name):
		return "", fmt.Errorf("env cannot set `%s`. %s", name, ReservedEnvAdvice())
	}

	return name + "=" + value, nil
}

func envRule(v starlark.Value) error {
	_, err := EnvAssignments(v)

	return err
}

// NetworkDomains reads the network list of a sandbox call: domain names.
func NetworkDomains(v starlark.Value) ([]string, error) {
	domains, err := Words("network", v)
	if err != nil {
		return nil, err
	}

	return domains, model.CheckNetwork(domains)
}

// WritablePaths reads the allow_write list of a sandbox call: paths that the sandbox opens for writing.
func WritablePaths(v starlark.Value) ([]string, error) {
	paths, err := Words("allow_write", v)
	if err != nil {
		return nil, err
	}

	return paths, model.CheckAllowWrite(paths)
}

func networkRule(v starlark.Value) error {
	_, err := NetworkDomains(v)

	return err
}

func writableRule(v starlark.Value) error {
	_, err := WritablePaths(v)

	return err
}
