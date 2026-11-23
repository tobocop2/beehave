package flow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/agent"
	gatepkg "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// commandPlan is a command call after its check: the request, the stop codes, the label, and the count of its tries.
type commandPlan struct {
	request CommandRequest
	stops   []int
	label   string
	tries   *tries
}

// commandCall unpacks and checks the arguments of the command builtin.
func (s *script) commandCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (commandPlan, error) {
	c := commandRaw{failure: string(workflow.FailureRaise)}

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		runKeyword: &c.run, hostKeyword: &c.host, "stdin": &c.stdin, "env": &c.own, timeoutKeyword: &c.timeout, maxNewProcessesKeyword: &c.processes, slotsKeyword: &c.slots, "stop_on_rc": &c.codes, string(sandboxKeyword): &c.sandbox, nameKeyword: &c.label,
		retriesKeyword: &c.retries, backoffKeyword: &c.backoff, failureKeyword: &c.failure,
	})
	if err != nil {
		return commandPlan{}, err
	}

	plan := commandPlan{label: c.label}
	if plan.request, plan.stops, err = s.commandRequest(c); err != nil {
		return plan, err
	}

	plan.tries, err = triesOf(c)

	return plan, err
}

// triesOf reads the retry policy and the failure mode of a call.
func triesOf(c commandRaw) (*tries, error) {
	policy, err := workflow.Retry(c.retries, c.backoff)
	if err != nil {
		return nil, err
	}

	if err := workflow.RetrySlots(c.retries, c.slots != nil && c.slots != starlark.None); err != nil {
		return nil, err
	}

	if !slices.Contains(workflow.FailureModes(), c.failure) {
		return nil, fmt.Errorf("failure is %q. Use \"raise\" or \"return\"", c.failure)
	}

	return &tries{policy: policy, failure: workflow.FailureMode(c.failure)}, nil
}

// commandRaw are the arguments of a command call as the file wrote them.
type commandRaw struct {
	label, timeout, host, stdin string
	backoff, failure            string
	retries                     int
	run, own, codes             starlark.Value
	// processes is max_new_processes as the file wrote it. It stays a value so that a text or a fraction is refused with its own message.
	processes starlark.Value
	// slots is the slot class as the file wrote it. It stays a value so that an empty text and None are refused with their own message.
	slots starlark.Value
	// sandbox is the sandbox keyword as the file wrote it.
	sandbox starlark.Value
}

// commandRequest checks the arguments of a command call and makes its request and its stop codes.
func (s *script) commandRequest(c commandRaw) (CommandRequest, []int, error) {
	request, err := s.request(c.run, c.host, c.timeout)
	if err != nil {
		return request, nil, err
	}

	request.Stdin = c.stdin

	if request.MaxNewProcesses, request.SlotClass, err = limitsOf(c); err != nil {
		return request, nil, err
	}

	if err = s.confine(&request, c); err != nil {
		return request, nil, err
	}

	stops, err := workflow.StopCodes(c.codes)

	return request, stops, err
}

// confine sets the environment and the sandbox of the request from the arguments of the call.
func (s *script) confine(request *CommandRequest, c commandRaw) (err error) {
	if request.Env, err = s.environment(*request, c.own); err != nil {
		return err
	}

	request.Sandbox, err = sandboxOf(c.sandbox, *request)

	return err
}

// sandboxOf reads the sandbox keyword of a command call. A call that leaves it out, or gives None, runs with no sandbox: the project default of the sandbox
// is for agent steps. A sandbox value cannot go with a host, stdin or a process cap.
func sandboxOf(value starlark.Value, request CommandRequest) (*model.Sandbox, error) {
	choice, err := readSandbox(value)
	if err != nil || choice.box == nil {
		return nil, err
	}

	return choice.box, workflow.SandboxWith(request.Host, request.Stdin, request.MaxNewProcesses)
}

// limitsOf reads the two settings that limit a call: the cap of its processes and the slot class of its host job. Each one refuses the other host rule on its own.
func limitsOf(c commandRaw) (processes int, class string, err error) {
	if processes, err = workflow.ProcessCap(c.processes, c.host); err != nil {
		return 0, "", err
	}

	class, err = workflow.SlotClass(c.slots, c.host)

	return processes, class, err
}

// request makes the request of a call from its program, host and timeout. The caller adds stdin and the environment.
func (s *script) request(run starlark.Value, host, timeout string) (CommandRequest, error) {
	request := CommandRequest{Project: s.in.Project, Item: s.in.Item, Host: host, BuildHost: s.hostChosen, TreeStart: s.treeStart}

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
	pairs, err := workflow.EnvAssignments(own)
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

// command runs a program. A code in stop_on_rc ends the item: the step records it, so a replay stops at the same call.
func (s *script) command(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	plan, err := s.commandCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	if plan.request.SlotClass != "" {
		return s.slotCommand(plan)
	}

	return s.durableRetried("command", plan.label, plan.tries.policy, func(ctx context.Context, name string) (stepResult, error) {
		return s.runCommand(ctx, name, plan)
	})
}

// slotCommand runs a command call that names a slot class as a job on the queue of the class, then records its output as the step of the call.
// DBOS records a child run from the run, never from inside a step, so the queue wait comes first and the step records what it returned.
// A replay reads the recorded result of the child run and queues nothing again. With failure "return", a job that could not run is the recorded result.
func (s *script) slotCommand(plan commandPlan) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	out, err := s.e.queueSlotCommand(s.ctx, plan.request)
	if err != nil && !plan.tries.returned(s.ctx, err, func(error) bool { return false }) {
		return nil, fmt.Errorf("command %q could not run: %w", plan.label, err)
	}

	return s.durable("command", plan.label, func(_ context.Context, name string) (stepResult, error) {
		if err != nil {
			return stepResult{Outputs: s.e.failedOutputs(err, 1)}, nil
		}

		return commandRecord(name, out, plan.stops, 1), nil
	})
}

// runCommand runs one try of the program of a command call and records its output. A code in stops adds the stop to the record.
// A try that a retry may cure returns retriedRC. With failure "return", an error that ends the tries is the recorded result.
func (s *script) runCommand(ctx context.Context, name string, plan commandPlan) (stepResult, error) {
	plan.tries.start()

	out, err := s.writes().Run(ctx, plan.request)

	switch {
	case err != nil && plan.tries.returned(ctx, err, s.e.retryable):
		return stepResult{Outputs: s.e.failedOutputs(err, plan.tries.n)}, nil
	case err != nil:
		return stepResult{}, err
	case plan.tries.retry(out.RC, plan.stops):
		return stepResult{}, &retriedRC{rc: out.RC}
	}

	return commandRecord(name, out, plan.stops, plan.tries.n), nil
}

// commandRecord is the record of a command step: the output, and the stop when the exit code is one of stops.
func commandRecord(name string, out CommandOutput, stops []int, attempts int) stepResult {
	recorded := stepResult{Outputs: commandOutputs(out, attempts)}
	if slices.Contains(stops, out.RC) {
		recorded.Stopped = &Stopped{Step: name, RC: out.RC, Output: out.Stdout}
	}

	return recorded
}

// errStopped unwinds the run when a command stops the item. It is not a failure: runStar turns it into a result.
var errStopped = errors.New("a command stopped the item")

// gateCall unpacks the arguments of the gate builtin: the request, the head and the label.
func (s *script) gateCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (request CommandRequest, head, label string, err error) {
	var (
		host, timeout, base, pinBase string
		run                          starlark.Value
	)

	if err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		runKeyword: &run, hostKeyword: &host, timeoutKeyword: &timeout, headKeyword: &head, baseKeyword: &base, pinBaseKeyword: &pinBase, nameKeyword: &label,
	}); err != nil {
		return request, head, label, err
	}

	request, err = s.request(run, host, timeout)
	request.Base, request.PinBase = base, pinBase

	return request, head, label, err
}

// gate runs the project gate when the call names no program, and the program otherwise. The gate passes when the program exits with zero.
func (s *script) gate(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	request, head, label, err := s.gateCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	outputs, err := s.exclusiveGate(request, head, label)
	if err != nil {
		return nil, err
	}

	if err := s.fixScope(label); err != nil {
		return nil, err
	}

	return s.recordGate(label, outputs)
}

// exclusiveGate runs the gate of a call as a child workflow on the exclusive queue, so no merge of the repository starts while it runs,
// and returns its outputs. The gate call records them as its own step, so a replay reads them and runs no gate again.
func (s *script) exclusiveGate(request CommandRequest, head, label string) (map[string]any, error) {
	in := ExclusiveInput{Kind: kindGate, Project: request.Project, Item: request.Item, Head: head, Request: request, Severity: string(s.severity)}

	ran, err := s.e.queueExclusive(s.ctx, in)
	s.noteGateHost(ran)

	if err != nil {
		return nil, fmt.Errorf("gate %q could not run: %w", label, err)
	}

	// A lost job with no proof at the limit asks the owner here, in the item run, under the name of the gate call that comes next.
	outputs, err := s.e.lostOutputs(s.ctx, in, s.nextName(kindGate, label), ran)
	if err != nil {
		return nil, fmt.Errorf("gate %q lost its job: %w", label, err)
	}

	return outputs, nil
}

// programGateOutputs runs the program that a gate call names, with the exit code as the verdict. It has no key: no pass of it stands in for a later gate.
func (e *Engine) programGateOutputs(ctx context.Context, request CommandRequest, head string, slot model.Slot) (map[string]any, error) {
	request.Gate = true
	request.SlotClass, request.SlotCount = slot.Class, slot.Count

	out, err := e.deps.Commands.Run(ctx, request)
	if err != nil {
		return nil, err
	}

	status := passStatus
	if out.RC != 0 {
		status = failStatus
	}

	return gateOutputs(out.RC == 0, head, "", out.Stdout, []GateStep{{Name: gatepkg.StepCommand, Status: status}}, e.requiredSteps(request.Project), nil), nil
}

// gateStart is what the first step of a project gate records: the pass of an earlier gate that the key reuses, or the host job that the gate submitted.
// A replay reads it and submits nothing.
// The job holds the Plan of the gate: the gate, the list, the test report, the build host and the pin check, all fixed here with the job text.
// Required is the list of the gate in that Plan. The wait and the finish read the record, and read nothing from the project.
type gateStart struct {
	Job      GateJob        `json:"job"`
	Reused   map[string]any `json:"reused,omitempty"`
	Key      GateKey        `json:"key"`
	Required []string       `json:"required,omitempty"`
}

// reuseProjectGate reads the key of the project gate and reuses the pass of an earlier gate with the same key. With no such pass, the start has the key and no Reused.
func (e *Engine) reuseProjectGate(ctx context.Context, request CommandRequest) (gateStart, error) {
	if err := e.refuseGate(request.Project, false); err != nil {
		return gateStart{}, err
	}

	if err := e.remakeTree(ctx, request); err != nil {
		return gateStart{}, err
	}

	key := e.gateKey(ctx, request)

	// An earlier pass has no checks of a candidate, so a candidate gate reuses none.
	if earlier, ok := e.earlierPass(key, request.Project); ok && request.Base == "" {
		line := fmt.Sprintf("reused the gate pass of run %s: the tree %s, the gate command and the pins are the same. The gate did not run.", earlier.run, key.Tree)
		e.logger.InfoContext(ctx, line, "item", request.Item)

		return gateStart{Reused: gateOutputs(true, key.Head, key.Tree, line, earlier.steps, e.requiredSteps(request.Project), nil), Key: key}, nil
	}

	return gateStart{Key: key}, nil
}

// startProjectGate reuses the pass of an earlier gate with the same key, and submits the host job of the project gate when there is none.
func (e *Engine) startProjectGate(ctx context.Context, request CommandRequest, slot model.Slot) (gateStart, error) {
	start, err := e.reuseProjectGate(ctx, request)
	if err != nil || start.Reused != nil {
		return start, err
	}

	start.Job, err = e.deps.Gate.Submit(ctx, request.Project, request.Item, request.BuildHost, request.Base, request.PinBase, start.Key.BaseSHA, slot)
	start.Required = e.requiredOf(start.Job, request.Project)

	return start, err
}

// requiredOf is the names that a merge needs from the gate of job: the list of its plan. A job with no plan (a gate that is not the project gate of beehave)
// takes the list of the project now, once, here in the step that records it.
func (e *Engine) requiredOf(job GateJob, project string) []string {
	if job.Plan.Empty() {
		return e.requiredSteps(project)
	}

	return job.Plan.Required()
}

// finishProjectGate waits for the host job of the project gate and judges it. The key it returns is empty unless the gate passed on every step
// and read the tree it judged: only such a pass may stand in for a later gate. The key to compare with is the one that the first step read and recorded.
// A wait that ended as lost while the run still runs gives the lost job and no outputs. A wait that a cancel stopped stays an error.
func (e *Engine) finishProjectGate(ctx context.Context, in ExclusiveInput, start gateStart) (gateFinish, string, error) {
	request := in.Request
	if err := e.remakeTree(ctx, request); err != nil {
		return gateFinish{}, "", err
	}

	gated, err := e.deps.Gate.Run(ctx, request.Project, request.Item, request.BuildHost, start.Job)
	if errors.Is(err, host.ErrLost) && ctx.Err() == nil {
		return gateFinish{Lost: lostJobOf(in, start, err)}, "", nil
	}

	if err != nil {
		return gateFinish{}, "", err
	}

	outputs := gateOutputs(gated.Passed, gated.Head, gated.Tree, gated.Report, gated.Steps, start.Required, gated.Checks)

	return gateFinish{Outputs: outputs}, recordedKey(gated, start.Key), nil
}

// remakeTree makes the worktree of a round item again at its tree start when someone removed it, so the project gate, which reads the folder by its path, finds it.
// A request with no tree start makes nothing.
func (e *Engine) remakeTree(ctx context.Context, request CommandRequest) error {
	if request.TreeStart == "" {
		return nil
	}

	if _, err := e.deps.Trees.Ensure(ctx, request.Project, request.Item, request.TreeStart, ""); err != nil {
		return fmt.Errorf("find the folder of item %s: %w", request.Item, err)
	}

	return nil
}

// gateKey reads the key of the project gate. A key that cannot be read is an empty key: the gate then runs and reports the same problem.
func (e *Engine) gateKey(ctx context.Context, request CommandRequest) GateKey {
	key, err := e.deps.Gate.Key(ctx, request.Project, request.Item, request.BuildHost, request.Base, request.PinBase)
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

// waitArgs reads the arguments of a wait: its question, its label, its deadline, the people it asks, its quorum and its choices.
// No timeout is a deadline of zero. A call that names no people has a quorum of one.
func waitArgs(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (waitPlan, error) {
	plan := waitPlan{question: "The workflow waits for your answer."}

	var kw waitKeywords

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"question": &plan.question, "timeout": &kw.timeout, nameKeyword: &plan.label, "to": &kw.to, "quorum": &kw.quorum, "choices": &kw.choices, "remind": &kw.remind,
	}); err != nil {
		return waitPlan{}, err
	}

	if err := errors.Join(plan.ask(kw, gave(kwargs, "quorum")), plan.limit(kw, gave(kwargs, "timeout")), plan.reminders(kw, gave(kwargs, "timeout"))); err != nil {
		return waitPlan{}, fmt.Errorf("wait: %w", err)
	}

	return plan, nil
}

// reminders reads the shares of the window at which a wait reminds its people. A wait that sets none sends none.
func (p *waitPlan) reminders(kw waitKeywords, timeoutGiven bool) error {
	if kw.remind == nil {
		return nil
	}

	var err error
	if p.remind, err = workflow.Remind(kw.remind); err != nil {
		return err
	}

	return workflow.CheckRemind(timeoutGiven, len(p.asked.From) > 0)
}

// limit reads the deadline of a wait. A timeout that the call gives is a deadline above zero, even as an empty text. A call without the keyword has no deadline.
func (p *waitPlan) limit(kw waitKeywords, timeoutGiven bool) error {
	if !timeoutGiven {
		return nil
	}

	var err error
	p.deadline, err = workflow.Deadline("timeout", kw.timeout)

	return err
}

// waitKeywords are the keywords of a wait as the call gave them. A keyword that the call leaves out holds its zero value.
type waitKeywords struct {
	timeout string
	to      starlark.Value
	choices starlark.Value
	remind  starlark.Value
	quorum  int
}

// gave reports whether the call sets a keyword, whatever its value.
func gave(kwargs []starlark.Tuple, keyword string) bool {
	return slices.ContainsFunc(kwargs, func(pair starlark.Tuple) bool { return pair[0] == starlark.String(keyword) })
}

// ask reads the people, the choices and the quorum of a wait with the rules of the check.
func (p *waitPlan) ask(kw waitKeywords, quorumGiven bool) error {
	var err error

	if p.asked.From, err = workflow.OptionalNames("to", kw.to); err != nil {
		return err
	}

	if p.asked.Choices, err = workflow.OptionalNames("choices", kw.choices); err != nil {
		return err
	}

	return p.needs(kw.quorum, quorumGiven)
}

// needs reads the quorum of a wait. A wait that names people and sets no quorum needs one answer.
func (p *waitPlan) needs(quorum int, given bool) error {
	if given {
		if _, err := workflow.Quorum(starlark.MakeInt(quorum)); err != nil {
			return err
		}
	}

	if err := workflow.CheckQuorum(quorum, p.asked.From); err != nil {
		return err
	}

	p.asked.Quorum = quorum
	if len(p.asked.From) > 0 {
		p.asked.Quorum = max(quorum, 1)
	}

	return nil
}

// wait asks the owner a question and returns the answer. The wait is durable and holds no queue slot.
// A wait that names people or choices counts their answers; any other wait takes the first plain answer.
func (s *script) wait(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	plan, err := waitArgs(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	heard, err := s.listen(plan)
	if err != nil {
		return nil, err
	}

	return s.durableWith("wait", plan.label, waitZeros(), func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: answerOutputs(heard.answer, heard.answers, heard.timedOut)}, nil
	})
}

// refuseEnv returns an error for a variable that a file cannot read: one of the engine, or a secret by its name or its value.
func (e *Engine) refuseEnv(name string) error {
	switch {
	case workflow.AgentSocketRead(name) != nil:
		return workflow.AgentSocketRead(name)
	case workflow.ReservedEnvName(name):
		return fmt.Errorf("env cannot read `%s`. %s", name, workflow.ReservedEnvAdvice())
	case workflow.SecretEnv(name, e.env[name]):
		return fmt.Errorf("env cannot read `%s`, because the variable is a secret. Pass secrets to the runner, not to the workflow", name)
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

// now reads the wall clock in a step and returns it as RFC 3339 text in UTC. DBOS records the text, so a replay returns the first reading.
func (s *script) now(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{}); err != nil {
		return nil, err
	}

	value, err := countedStep(s, "now", "", func(context.Context) (string, error) {
		return s.e.deps.Clock.reading().UTC().Format(time.RFC3339), nil
	})

	return starlark.String(value), err
}

// env reads a variable of the engine in a step, so a replay sees the value of the first run. It refuses the names of secrets and of the engine.
func (s *script) env(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var name string

	var fallback starlark.Value

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &name, "default": &fallback}); err != nil {
		return nil, err
	}

	if err := s.e.refuseEnv(name); err != nil {
		return nil, err
	}

	value, err := countedStep(s, "env", name, func(context.Context) (string, error) { return s.e.variableOr(name, fallback) })
	if err != nil {
		return nil, err
	}

	return starlark.String(value), nil
}
