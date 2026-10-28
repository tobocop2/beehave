package flow

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/hclfile"
	"github.com/tobocop2/obee/internal/workflow"
)

// commandInput evaluates everything a command step needs before its durable step: the request with stdin and environment, and the stop codes.
func (c *call) commandInput(step *workflow.Command) (CommandRequest, []int, error) {
	request, err := c.request(step.Run, step.Host, step.Timeout)
	if err != nil {
		return CommandRequest{}, nil, err
	}

	if request.Stdin, err = c.text(step.Stdin); err != nil {
		return CommandRequest{}, nil, err
	}

	if request.Env, err = c.environment(step, request); err != nil {
		return CommandRequest{}, nil, err
	}

	stops, err := c.stopCodes(step.StopOnRC)

	return request, stops, err
}

// environment returns the whole environment of the program: the allowed names of the caller, then the variables of the step.
// No other variable of the caller reaches the program. A step with a host gets none, because a host job takes a command line only.
func (c *call) environment(step *workflow.Command, request CommandRequest) ([]string, error) {
	own, err := c.variables(step.Env)
	if err != nil {
		return nil, err
	}

	if request.Host == "" {
		return append(agent.LoginEnv(c.w.e.env, agent.Profile{}, ""), own...), nil
	}

	if request.Stdin != "" || len(own) > 0 {
		return nil, hclfile.At(step.Run.Range(), "stdin and env cannot be used together with host. A host job takes a command line only. Remove them or host.")
	}

	return nil, nil
}

// variables evaluates an optional `env` argument to NAME=value pairs, sorted by name. An absent argument is empty.
func (c *call) variables(expr hcl.Expression) ([]string, error) {
	value, err := c.optional(expr)
	if err != nil || value.IsNull() {
		return nil, err
	}

	if kind := value.Type(); !kind.IsObjectType() && !kind.IsMapType() {
		return nil, hclfile.At(expr.Range(), "env must be a map of strings. Write env = { NAME = \"value\" }.")
	}

	return pairsOf(expr, value)
}

// pairsOf returns the NAME=value pairs of a map value, sorted by name.
func pairsOf(expr hcl.Expression, entries cty.Value) ([]string, error) {
	var pairs []string

	for it := entries.ElementIterator(); it.Next(); {
		key, item := it.Element()

		pair, err := variable(expr, key.AsString(), item)
		if err != nil {
			return nil, err
		}

		pairs = append(pairs, pair)
	}

	sort.Strings(pairs)

	return pairs, nil
}

// variable returns the NAME=value pair of one entry of `env`. It refuses a reserved name and a value that is not a string.
func variable(expr hcl.Expression, name string, item cty.Value) (string, error) {
	if workflow.ReservedEnvName(name) {
		return "", hclfile.At(expr.Range(), fmt.Sprintf("env cannot set `%s`. %s", name, workflow.ReservedEnvAdvice()))
	}

	if item.IsNull() || item.Type() != cty.String {
		return "", hclfile.At(expr.Range(), fmt.Sprintf("env must hold strings only, and `%s` is not a string. Write the value in quotes.", name))
	}

	return name + "=" + item.AsString(), nil
}

// optional evaluates an argument that may be absent. An absent argument is null.
func (c *call) optional(expr hcl.Expression) (cty.Value, error) {
	if absent(expr) {
		return cty.NullVal(cty.DynamicPseudoType), nil
	}

	return c.value(expr)
}

// stopCodes evaluates an optional `stop_on_rc` argument. An absent argument is an empty list.
func (c *call) stopCodes(expr hcl.Expression) ([]int, error) {
	if absent(expr) {
		return nil, nil
	}

	value, err := c.value(expr)
	if err != nil {
		return nil, err
	}

	if kind := value.Type(); value.IsNull() || !kind.IsListType() && !kind.IsTupleType() && !kind.IsSetType() {
		return nil, hclfile.At(expr.Range(), "stop_on_rc must be a list of exit codes. Write stop_on_rc = [2].")
	}

	return codesOf(expr, value)
}

// codesOf returns the exit codes in a list value. Each must be a whole number from 1 to 255.
func codesOf(expr hcl.Expression, list cty.Value) ([]int, error) {
	var codes []int

	for it := list.ElementIterator(); it.Next(); {
		_, item := it.Element()

		code, valid := workflow.StopCode(item)
		if !valid {
			return nil, hclfile.At(expr.Range(), "stop_on_rc must hold whole numbers from 1 to 255. Write each exit code as a number.")
		}

		codes = append(codes, code)
	}

	return codes, nil
}
