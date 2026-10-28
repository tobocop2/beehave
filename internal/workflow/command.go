package workflow

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

const (
	// minStopCode and maxStopCode are the exit codes that `stop_on_rc` can list. Code 0 is success.
	minStopCode = 1
	maxStopCode = 255
)

// reservedEnvPrefixes lists the prefixes of the variable names that a command step never sets.
// The engine and DBOS own these names.
func reservedEnvPrefixes() []string { return []string{"DBOS__", "OBEE_"} }

// ReservedEnvAdvice is the end of the message for a reserved `env` name.
func ReservedEnvAdvice() string {
	return "Names that start with " + strings.Join(reservedEnvPrefixes(), " or ") + " belong to the engine. Use another name."
}

// ReservedEnvName reports whether name starts with a prefix that a command step never sets in `env`.
func ReservedEnvName(name string) bool {
	for _, prefix := range reservedEnvPrefixes() {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

// StopCode returns the exit code that value holds, and false when value is not a whole number from 1 to 255.
func StopCode(value cty.Value) (int, bool) {
	if !value.IsKnown() || value.IsNull() || value.Type() != cty.Number {
		return 0, false
	}

	code, exact := value.AsBigFloat().Int64()

	return int(code), exact == big.Exact && code >= minStopCode && code <= maxStopCode
}

// commands checks the settings that a command step adds to its program: stdin, env and stop_on_rc.
// A value that an expression computes is checked again when the step runs.
func (c *checker) commands() {
	for _, s := range c.sites {
		step, ok := s.step.(*Command)
		if !ok {
			continue
		}

		c.stopOnRC(step.StopOnRC)
		c.stdin(step.Stdin)
		c.env(step.Env)
		c.hostInput(step)
	}
}

// stopOnRC checks `stop_on_rc`: a list of whole numbers from 1 to 255.
func (c *checker) stopOnRC(expr hcl.Expression) {
	items, diags := hcl.ExprList(expr)
	if diags.HasErrors() {
		if value, err := expr.Value(nil); !err.HasErrors() && !value.IsNull() {
			c.add(expr.Range(), "stop_on_rc must be a list of exit codes. Write stop_on_rc = [2].")
		}

		return
	}

	for _, item := range items {
		if !validStopCode(item) {
			c.add(item.Range(), fmt.Sprintf("stop_on_rc must hold whole numbers from %d to %d. Write each exit code as a number.", minStopCode, maxStopCode))
		}
	}
}

// validStopCode is false for a literal that is not a whole number from 1 to 255. An expression that is not a literal is valid here.
func validStopCode(expr hcl.Expression) bool {
	value, diags := expr.Value(nil)
	if diags.HasErrors() || !value.IsKnown() {
		return true
	}

	_, valid := StopCode(value)

	return valid
}

// stdin checks `stdin`: a string.
func (c *checker) stdin(expr hcl.Expression) {
	if notString(expr) {
		c.add(expr.Range(), "stdin must be a string. Write the text in quotes, or name a step output that holds text.")
	}
}

// env checks `env`: a map of strings, and no name that starts with DBOS__ or OBEE_.
func (c *checker) env(expr hcl.Expression) {
	pairs, diags := hcl.ExprMap(expr)
	if diags.HasErrors() {
		if value, err := expr.Value(nil); !err.HasErrors() && !value.IsNull() {
			c.add(expr.Range(), "env must be a map of strings. Write env = { NAME = \"value\" }.")
		}

		return
	}

	for _, pair := range pairs {
		if name, literal := literalString(pair.Key); literal && ReservedEnvName(name) {
			c.add(pair.Key.Range(), fmt.Sprintf("env cannot set `%s`. %s", name, ReservedEnvAdvice()))
		}

		if notString(pair.Value) {
			c.add(pair.Value.Range(), "env must hold strings only. Write each value in quotes.")
		}
	}
}

// hostInput refuses `stdin` and `env` on a command that runs on a host. A host job takes a command line only.
func (c *checker) hostInput(step *Command) {
	if unset(step.Host) {
		return
	}

	if !unset(step.Stdin) {
		c.add(step.Stdin.Range(), "stdin cannot be set together with host. A host job takes a command line only. Remove stdin or host.")
	}

	if !unset(step.Env) {
		c.add(step.Env.Range(), "env cannot be set together with host. A host job takes a command line only. Remove env or host.")
	}
}
