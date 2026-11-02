package flow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/agent"
	gatepkg "github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/workflow"
)

// commandCall unpacks the arguments of the command builtin: the request, the stop codes and the label.
func (s *script) commandCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (CommandRequest, []int, string, error) {
	var c commandRaw

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		runKeyword: &c.run, "host": &c.host, "stdin": &c.stdin, "env": &c.own, timeoutKeyword: &c.timeout, maxNewProcessesKeyword: &c.processes, "stop_on_rc": &c.codes, nameKeyword: &c.label,
	})
	if err != nil {
		return CommandRequest{}, nil, "", err
	}

	request, stops, err := s.commandRequest(c)

	return request, stops, c.label, err
}

// commandRaw are the arguments of a command call as the file wrote them.
type commandRaw struct {
	label, timeout, host, stdin string
	run, own, codes             starlark.Value
	// processes is max_new_processes as the file wrote it. It stays a value so that a text or a fraction is refused with its own message.
	processes starlark.Value
}

// commandRequest checks the arguments of a command call and makes its request and its stop codes.
func (s *script) commandRequest(c commandRaw) (CommandRequest, []int, error) {
	request, err := s.request(c.run, c.host, c.timeout)
	if err != nil {
		return request, nil, err
	}

	request.Stdin = c.stdin

	if request.MaxNewProcesses, err = workflow.ProcessCap(c.processes, c.host); err != nil {
		return request, nil, err
	}

	if request.Env, err = s.environment(request, c.own); err != nil {
		return request, nil, err
	}

	stops, err := workflow.StopCodes(c.codes)

	return request, stops, err
}

// request makes the request of a call from its program, host and timeout. The caller adds stdin and the environment.
func (s *script) request(run starlark.Value, host, timeout string) (CommandRequest, error) {
	request := CommandRequest{Project: s.in.Project, Item: s.in.Item, Host: host}

	var err error
	if request.Run, err = workflow.Words("run", run); err != nil {
		return request, err
	}

	request.Timeout, err = workflow.Duration("timeout", timeout)

	return request, err
}

// environment returns the whole environment of the program: the allowed names of the caller, then the variables of the call.
// No other variable of the caller reaches the program. A call with a host gets none, because a host job takes a command line only.
func (s *script) environment(request CommandRequest, own starlark.Value) ([]string, error) {
	pairs, err := variables(own)
	if err != nil {
		return nil, err
	}

	if request.Host == "" {
		return append(agent.LoginEnv(s.e.env, agent.Profile{}, ""), pairs...), nil
	}

	if request.Stdin != "" || len(pairs) > 0 {
		return nil, errors.New("stdin and env cannot be used together with host. A host job takes a command line only. Remove them or host")
	}

	return nil, nil
}

// variables reads the env dict of a command call as NAME=value pairs, sorted by name. An absent dict is empty.
func variables(v starlark.Value) ([]string, error) {
	dict, ok := workflow.Optional[*starlark.Dict](v)
	if !ok {
		return nil, errors.New("env must be a dict of strings. Write env = {\"NAME\": \"value\"}")
	}

	if dict == nil {
		return nil, nil
	}

	return assignments(dict)
}

// assignments makes the sorted NAME=value pairs of an env dict.
func assignments(dict *starlark.Dict) ([]string, error) {
	var pairs []string

	for _, pair := range dict.Items() {
		assignment, err := assignmentOf(pair[0], pair[1])
		if err != nil {
			return nil, err
		}

		pairs = append(pairs, assignment)
	}

	slices.Sort(pairs)

	return pairs, nil
}

// assignmentOf makes one NAME=value pair of the env dict of a command call.
func assignmentOf(key, val starlark.Value) (string, error) {
	name, nameOK := starlark.AsString(key)
	value, valueOK := starlark.AsString(val)

	switch {
	case !nameOK || !valueOK:
		return "", fmt.Errorf("env must hold strings only, and %s is not. Write the value in quotes", key)
	case workflow.ReservedEnvName(name):
		return "", fmt.Errorf("env cannot set `%s`. %s", name, workflow.ReservedEnvAdvice())
	}

	return name + "=" + value, nil
}

// command runs a program. A code in stop_on_rc ends the item: the step records it, so a replay stops at the same call.
func (s *script) command(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	request, stops, label, err := s.commandCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	return s.durable("command", label, func(ctx context.Context, name string) (stepResult, error) {
		return s.runCommand(ctx, name, request, stops)
	})
}

// runCommand runs the program of a command call and records its output. A code in stops adds the stop to the record.
func (s *script) runCommand(ctx context.Context, name string, request CommandRequest, stops []int) (stepResult, error) {
	out, err := s.e.deps.Commands.Run(ctx, request)
	if err != nil {
		return stepResult{}, err
	}

	recorded := stepResult{Outputs: commandOutputs(out.Stdout, out.Stderr, out.RC)}
	if slices.Contains(stops, out.RC) {
		recorded.Stopped = &Stopped{Step: name, RC: out.RC, Output: out.Stdout}
	}

	return recorded, nil
}

// errStopped unwinds the run when a command stops the item. It is not a failure: runStar turns it into a result.
var errStopped = errors.New("a command stopped the item")

// gateCall unpacks the arguments of the gate builtin: the request, the head and the label.
func (s *script) gateCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (request CommandRequest, head, label string, err error) {
	var (
		host, timeout string
		run           starlark.Value
	)

	if err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		runKeyword: &run, "host": &host, timeoutKeyword: &timeout, headKeyword: &head, nameKeyword: &label,
	}); err != nil {
		return request, head, label, err
	}

	request, err = s.request(run, host, timeout)

	return request, head, label, err
}

// gate runs the project gate when the call names no program, and the program otherwise. The gate passes when the program exits with zero.
func (s *script) gate(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	request, head, label, err := s.gateCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	// The gate runs as a child workflow on the exclusive queue, so no merge of the repository starts while it runs.
	// The call records its outputs as its own step, so a replay reads them and runs no gate again.
	ran, err := s.e.queueExclusive(s.ctx, ExclusiveInput{Kind: kindGate, Project: request.Project, Item: request.Item, Head: head, Request: request})
	if err != nil {
		return nil, fmt.Errorf("gate %q could not run: %w", label, err)
	}

	if err := s.fixScope(label); err != nil {
		return nil, err
	}

	return s.durable("gate", label, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: ran.Outputs}, nil
	})
}

// gateOutputs runs the project gate when the request names no program, and the program otherwise.
// The key it returns is empty unless the gate passed on every step and read the tree it judged: only such a pass may stand in for a later gate.
func (e *Engine) gateOutputs(ctx context.Context, request CommandRequest, head string) (outputs map[string]any, key string, err error) {
	if len(request.Run) == 0 {
		return e.projectGateOutputs(ctx, request)
	}

	out, err := e.deps.Commands.Run(ctx, request)
	if err != nil {
		return nil, "", err
	}

	status := passStatus
	if out.RC != 0 {
		status = failStatus
	}

	return gateOutputs(out.RC == 0, head, "", out.Stdout, []GateStep{{Name: gatepkg.StepCommand, Status: status}}), "", nil
}

// projectGateOutputs reuses the pass of an earlier gate with the same key, and runs the project gate when there is none.
func (e *Engine) projectGateOutputs(ctx context.Context, request CommandRequest) (map[string]any, string, error) {
	key := e.gateKey(ctx, request)

	if earlier, ok := e.earlierPass(key, request.Project); ok {
		line := fmt.Sprintf("reused the gate pass of run %s: the tree %s, the gate command and the pins are the same. The gate did not run.", earlier.run, key.Tree)
		e.logger.InfoContext(ctx, line, "item", request.Item)

		return gateOutputs(true, key.Head, key.Tree, line, earlier.steps), key.ID, nil
	}

	gated, err := e.deps.Gate.Run(ctx, request.Project, request.Item)
	if err != nil {
		return nil, "", err
	}

	return gateOutputs(gated.Passed, gated.Head, gated.Tree, gated.Report, gated.Steps), recordedKey(gated, key), nil
}

// gateKey reads the key of the project gate. A key that cannot be read is an empty key: the gate then runs and reports the same problem.
func (e *Engine) gateKey(ctx context.Context, request CommandRequest) GateKey {
	key, err := e.deps.Gate.Key(ctx, request.Project, request.Item)
	if err != nil {
		return GateKey{}
	}

	return key
}

// recordedKey gives the key id for a gate that passed on every step and judged the tree that its key names, and an empty text for any other gate.
func recordedKey(gated GateResult, key GateKey) string {
	if gated.Passed && everyStepIsAPass(gated.Steps) && gated.Head == key.Head && gated.Tree == key.Tree {
		return key.ID
	}

	return ""
}

// waitArgs reads the arguments of a wait: its question, its label and its deadline. No timeout is a deadline of zero.
func waitArgs(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (question, label string, deadline time.Duration, err error) {
	question = "The workflow waits for your answer."

	var timeout string

	if err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"question": &question, "timeout": &timeout, nameKeyword: &label,
	}); err != nil {
		return "", "", 0, err
	}

	// A timeout that the call gives is a deadline above zero, even as an empty text. A call without the keyword has no deadline.
	if !slices.ContainsFunc(kwargs, func(pair starlark.Tuple) bool { return pair[0] == starlark.String("timeout") }) {
		return question, label, 0, nil
	}

	deadline, err = workflow.Deadline("timeout", timeout)
	if err != nil {
		return "", "", 0, fmt.Errorf("wait: %w", err)
	}

	return question, label, deadline, nil
}

// wait asks the owner a question and returns the answer. The wait is durable and holds no queue slot.
func (s *script) wait(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	question, label, deadline, err := waitArgs(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	answer, timedOut, err := s.e.waitOwnerUntil(s.ctx, s.in.Item, question, deadline)
	if err != nil {
		return nil, err
	}

	return s.durable("wait", label, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: answerOutputs(answer, timedOut)}, nil
	})
}

// refuseEnvName returns an error for the name of a variable that a file cannot read: one of the engine, or a secret.
func refuseEnvName(name string) error {
	switch {
	case workflow.ReservedEnvName(name):
		return fmt.Errorf("env cannot read `%s`. %s", name, workflow.ReservedEnvAdvice())
	case workflow.SecretEnvName(name):
		return fmt.Errorf("env cannot read `%s`, because the name is a secret. Pass secrets to the runner, not to the workflow", name)
	}

	return nil
}

// variable returns the redacted value of a variable of the engine. A name that is not set is an error that names it.
func (e *Engine) variable(name string) (string, error) {
	raw, set := e.env[name]
	if !set {
		return "", fmt.Errorf("the environment variable %s is not set. Set it for the engine, or remove env(%q) from the file", name, name)
	}

	return e.redact(raw), nil
}

// variableOr returns the variable, or the default text of the call when the variable is not set. A call with no default gets the error of variable.
func (e *Engine) variableOr(name string, fallback starlark.Value) (string, error) {
	if text, given := starlark.AsString(fallback); given {
		if _, set := e.env[name]; !set {
			return text, nil
		}
	}

	return e.variable(name)
}

// env reads a variable of the engine in a step, so a replay sees the value of the first run. It refuses the names of secrets and of the engine.
func (s *script) env(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var name string

	var fallback starlark.Value

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &name, "default": &fallback}); err != nil {
		return nil, err
	}

	if err := refuseEnvName(name); err != nil {
		return nil, err
	}

	value, err := runStep(s.e, s.ctx, s.name("env", name), func(context.Context) (string, error) { return s.e.variableOr(name, fallback) })
	if err != nil {
		return nil, err
	}

	return starlark.String(value), nil
}
