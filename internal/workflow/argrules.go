package workflow

import (
	"errors"
	"fmt"
	"regexp"
	"slices"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/model"
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
	case name == model.SSHAgentVar:
		return "", agentSocketError(name)
	}

	return name + "=" + value, nil
}

// agentSocketError is the refusal of the SSH agent variable in the env of a call. No step gets the agent of the owner.
func agentSocketError(name string) error {
	return fmt.Errorf("env cannot set `%s`. No step gets the SSH agent of the owner. Remove it", name)
}

// AgentSocketRead returns the refusal of a read of the SSH agent variable by the env builtin, and nil for any other name.
// With it no workflow value can carry the path of the socket.
func AgentSocketRead(name string) error {
	if name != model.SSHAgentVar {
		return nil
	}

	return fmt.Errorf("env cannot read `%s`. No step gets the SSH agent of the owner. Remove the call", name)
}

// patchNamePattern is the text of a patch name: the name ends in the step name DBOS.patch-<name>, so it holds no character that a step name uses as a separator.
var patchNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)

// CheckPatchName refuses a patch name that is empty or holds a character other than a letter, a digit, a dot, a dash or an underscore.
func CheckPatchName(name string) error {
	if err := NotEmpty(nameKeyword, name); err != nil {
		return err
	}

	if !patchNamePattern.MatchString(name) {
		return fmt.Errorf("name %q holds a character other than a letter, a digit, a dot, a dash or an underscore", name)
	}

	return nil
}

// patchNameRule is the rule of the name argument of the patched builtin.
func patchNameRule(v starlark.Value) error {
	name, _ := starlark.AsString(v) // the unpack check has refused a value that is not text

	return CheckPatchName(name)
}

// envReadRule is the rule of the name argument of the env builtin.
func envReadRule(v starlark.Value) error {
	name, _ := starlark.AsString(v) // the unpack check has refused a value that is not text

	return AgentSocketRead(name)
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

func networkRule(v starlark.Value) error {
	_, err := NetworkDomains(v)

	return err
}
