package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

const (
	outputsFolder = "outputs"
	outputsMode   = 0o700
)

// agentArgs are the arguments of an agent call, and the folder and output file of its step.
type agentArgs struct {
	prompt        string
	data          map[string]any
	runner        string
	kind          string
	model         string
	timeout       time.Duration
	idleTimeout   time.Duration
	pin           string
	requireSkills []string
	forbid        []model.FindingKind
	protect       []string
	scope         scope
	start         startMark
	declared      []string
	resume        string
	noFollowUps   bool
	// edits is the edits argument: false starts the step so that Claude Code refuses each file edit.
	edits bool
	tries int
	dir   string
	file  string
	// sandbox is the sandbox keyword as written. box is the sandbox the step runs with, which the render step records, or nil for no sandbox.
	sandbox sandboxChoice
	box     *model.Sandbox
}

// agentCall unpacks the arguments of the agent builtin. The second result is the label of the call.
func agentCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (agentArgs, string, error) {
	a := agentArgs{tries: 1, edits: true}
	raw := agentRaw{followUps: workflow.FollowUpsAllow}

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"prompt": &a.prompt, "model": &a.model, "runner": &a.runner, "inputs": &raw.inputs, "outputs": &raw.outputs, "pin": &a.pin,
		"resume": &a.resume, "require_skills": &raw.skills, "forbid_commands": &raw.kinds, "allow_paths": &raw.allow, "protect_paths": &raw.protect, "output_tries": &a.tries,
		"edits": &a.edits, "follow_ups": &raw.followUps, "kind": &a.kind, timeoutKeyword: &raw.timeout, "idle_timeout": &raw.idle, "sandbox": &raw.sandbox, nameKeyword: &raw.label,
	})
	if err == nil && a.model == "" {
		err = errors.New("an agent call needs a model. Add model = \"sonnet\" or the name of another model")
	}

	if err != nil {
		return a, "", err
	}

	return a, raw.label, errors.Join(a.checkKind(), a.read(raw))
}

// agentRaw are the arguments of an agent call that need a check before they are used.
type agentRaw struct {
	label, timeout, idle, followUps string
	inputs, outputs, skills, kinds  starlark.Value
	allow, protect, sandbox         starlark.Value
}

// read checks the raw arguments of an agent call and keeps the result in a.
func (a *agentArgs) read(raw agentRaw) (err error) {
	if err = workflow.OutputTries(a.tries); err != nil {
		return err
	}

	if err = a.limits(raw); err != nil {
		return err
	}

	if a.data, err = workflow.InputMap(raw.inputs); err != nil {
		return err
	}

	if a.sandbox, err = readSandbox(raw.sandbox); err != nil {
		return err
	}

	return a.lists(raw)
}

// limits reads the timeout and the idle timeout of an agent call.
func (a *agentArgs) limits(raw agentRaw) (err error) {
	if a.timeout, err = workflow.Duration("timeout", raw.timeout); err != nil {
		return err
	}

	a.idleTimeout, err = workflow.Duration("idle_timeout", raw.idle)

	return err
}

// checkKind refuses a kind that is not one of the two kinds. An empty kind is the kind implement.
func (a agentArgs) checkKind() error {
	if a.kind == "" || slices.Contains(workflow.AgentKinds(), a.kind) {
		return nil
	}

	return fmt.Errorf("kind is %q. Use one of: %s", a.kind, strings.Join(workflow.AgentKinds(), ", "))
}

// lists reads the three list arguments of an agent call, then the follow_ups option, which needs the declared outputs.
func (a *agentArgs) lists(raw agentRaw) (err error) {
	if a.declared, err = workflow.Words("outputs", raw.outputs); err != nil {
		return err
	}

	if a.requireSkills, err = workflow.Words("require_skills", raw.skills); err != nil {
		return err
	}

	if a.forbid, a.protect, err = forbidden(raw.kinds, raw.protect); err != nil {
		return err
	}

	if a.scope, err = readScope(raw.allow); err != nil {
		return err
	}

	a.noFollowUps, err = followUps(raw.followUps, a.declared)

	return err
}

// findingKinds reads the forbid_commands argument: a list of the names of finding kinds.
func findingKinds(v starlark.Value) ([]model.FindingKind, error) {
	return workflow.ListOfEach(v, func(item starlark.Value) (model.FindingKind, error) {
		name, ok := starlark.AsString(item)
		if !ok || !slices.Contains(workflow.ForbidKinds(), name) {
			return "", fmt.Errorf("forbid_commands has the unknown kind %s. Use one of: %s", item, strings.Join(workflow.ForbidKinds(), ", "))
		}

		return model.FindingKind(name), nil
	}, errors.New("forbid_commands must be a list of strings"))
}

// forbidden reads the forbid_commands argument and the protect_paths argument that goes with it.
func forbidden(kinds, protect starlark.Value) ([]model.FindingKind, []string, error) {
	forbid, err := findingKinds(kinds)
	if err != nil {
		return nil, nil, err
	}

	paths, err := protectPaths(protect, forbid)

	return forbid, paths, err
}

// protectPaths reads the protect_paths argument: the directories the kind protected-path guards. A relative path starts at the folder of the step.
// The list and the kind go together: a kind that needs paths with none would pass every command, and paths with no kind would guard nothing.
func protectPaths(v starlark.Value, forbid []model.FindingKind) ([]string, error) {
	paths, err := workflow.Words("protect_paths", v)
	if err != nil {
		return nil, err
	}

	if slices.Contains(paths, "") {
		return nil, errors.New("protect_paths holds an empty path, which would name the folder of the step. Give a path")
	}

	for _, kind := range forbid {
		if kind.NeedsPaths() && len(paths) == 0 {
			return nil, fmt.Errorf("forbid_commands lists %s, so protect_paths must list at least one directory", kind)
		}
	}

	if len(paths) > 0 && !slices.ContainsFunc(forbid, model.FindingKind.NeedsPaths) {
		return nil, errors.New("protect_paths is set, but forbid_commands lists no kind that uses it. Add \"protected-path\" to forbid_commands")
	}

	return paths, nil
}

// outputFile is the path where the agent of a step writes its declared outputs. It is in the state folder of the engine, named by item and step,
// so the file never dirties the worktree of the item. A dot or a separator in a name is escaped, so two pairs of item and step never share a file.
func outputFile(state, item, step string) string {
	return filepath.Join(state, outputsFolder, model.SafeName(item)+"."+model.SafeName(step)+".json")
}

// agent runs an agent call. The prompt is rendered once and recorded as the note of the first step, so a replay never reads the prompt file again.
// Each run of the agent is a phase on the agents queue, keyed by login. After each run a second step reads the output file and checks the skills.
func (s *script) agent(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	a, label, err := agentCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	name := s.name("agent", label)

	login, prompt, err := s.ready(&a, name)
	if err != nil {
		return nil, err
	}

	done, err := s.attempts(name, a, a.phase(s.in, login, prompt))
	if err != nil {
		return nil, err
	}

	return result(done.Outputs)
}

// ready returns the login and the rendered prompt of an agent call. The login comes first, so a call that waits for a usage cap holds no start mark.
func (s *script) ready(a *agentArgs, name string) (login, prompt string, err error) {
	if login, err = s.loginFor(*a); err != nil {
		return "", "", err
	}

	prompt, err = s.promptOf(a, name)

	return login, prompt, err
}

// promptOf finds the folder and the output file of the call, then renders the prompt in a step and returns it.
func (s *script) promptOf(a *agentArgs, name string) (string, error) {
	if err := s.prepare(a, name); err != nil {
		return "", err
	}

	if err := s.markStart(a, name); err != nil {
		return "", err
	}

	// The sandbox is recorded with the prompt, in the same step, so a replay reads the sandbox of the first run and never the current default of the project.
	rendered, err := runStep(s.e, s.ctx, name+"/render", s.renderStep(*a))
	if err != nil {
		return "", err
	}

	return rendered.Note, a.recall(rendered.Outputs)
}

// recall reads the sandbox and the output file that the render step recorded. The output file is the one that the recorded prompt names,
// whatever the name function gives today. A record made before the path was pinned keeps the computed path.
func (a *agentArgs) recall(outputs map[string]any) (err error) {
	a.file = recalledOutputFile(outputs, a.file)

	if err = os.MkdirAll(filepath.Dir(a.file), outputsMode); err != nil {
		return fmt.Errorf("make the folder of the output file: %w", err)
	}

	a.box, err = recalledSandbox(outputs)

	return err
}

// renderStep returns the step that renders the prompt of an agent call and records the sandbox of the call.
func (s *script) renderStep(a agentArgs) func(context.Context) (stepResult, error) {
	return func(ctx context.Context) (stepResult, error) {
		prompt, err := s.render(ctx, a)
		if err != nil {
			return stepResult{Note: prompt}, err
		}

		outputs := map[string]any{recordedOutputFile: a.file}
		maps.Copy(outputs, recordSandbox(a.sandbox.effective(s.sandboxDefault())))

		return stepResult{Note: prompt, Outputs: outputs}, nil
	}
}

// prepare finds the folder and the output file of an agent call.
func (s *script) prepare(a *agentArgs, name string) (err error) {
	if a.dir, err = s.e.deps.Trees.Ensure(s.ctx, s.in.Project, s.in.Item); err != nil {
		return fmt.Errorf("find the folder of item %s: %w", s.in.Item, err)
	}

	if a.dir == "" {
		return fmt.Errorf("item %s of project %s has no folder for the agent", s.in.Item, s.in.Project)
	}

	a.file = outputFile(s.e.dir, s.in.Item, name)

	if err = os.MkdirAll(filepath.Dir(a.file), outputsMode); err != nil {
		return fmt.Errorf("make the folder of the output file: %w", err)
	}

	return nil
}

// phase makes the input of the phase that runs the agent.
func (a agentArgs) phase(in ItemInput, login, prompt string) PhaseInput {
	return PhaseInput{
		Item: in.Item, Project: in.Project, Login: login, Kind: a.kind, Prompt: prompt,
		Resume: a.resume, Model: a.model, Timeout: a.timeout, IdleTimeout: a.idleTimeout, Forbid: a.forbid, Protect: a.protect,
		NoEdits: !a.edits, OutputFile: a.file, Outputs: a.declared, Sandbox: a.box,
	}
}

// render checks the pin, reads the prompt file and fills it. The data are the inputs and output_file.
func (s *script) render(ctx context.Context, a agentArgs) (string, error) {
	if err := checkPin(ctx, a.dir, a.pin); err != nil {
		return "", err
	}

	path, text, err := s.promptFile(a.prompt)
	if err != nil {
		return "", err
	}

	data := maps.Clone(a.data)
	data["output_file"] = a.file

	filled, err := fill(string(text), data)
	if err != nil {
		return "", fmt.Errorf("prompt file %s: %w", path, err)
	}

	return filled, nil
}

// promptFile reads the prompt file. A relative path starts at the folder of the workflow file.
func (s *script) promptFile(path string) (string, []byte, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(s.in.File), path)
	}

	text, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		return "", nil, fmt.Errorf("read the prompt file: %w", err)
	}

	return path, text, nil
}

// fill executes a prompt template. A name that the data lacks is an error.
func fill(text string, data map[string]any) (string, error) {
	tmpl, err := template.New("prompt").Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}

	var out bytes.Buffer

	err = tmpl.Execute(&out, data)

	return out.String(), err
}

// checkPin refuses a folder whose HEAD is not the pin. An empty pin accepts any HEAD.
func checkPin(ctx context.Context, dir, pin string) error {
	if pin == "" {
		return nil
	}

	head, err := folderHead(ctx, dir)
	if err != nil {
		return err
	}

	if head != pin {
		return fmt.Errorf("the pin is %s and HEAD of the item folder is %s. Check out the pinned commit, or change the pin", pin, head)
	}

	return nil
}

// attempts runs the agent up to a.tries times in all. A run that leaves a bad output file is run again.
func (s *script) attempts(name string, a agentArgs, in PhaseInput) (stepResult, error) {
	for try := 1; ; try++ {
		done, err := s.attempt(name, a, in, try)
		if err != nil || done.Note == "" {
			return done, err
		}

		if try >= a.tries {
			return stepResult{}, errors.New(done.Note)
		}
	}
}

// attempt removes the old output file in a recorded step, runs the agent once and reads what it left.
// The removal is recorded, so a replay after the agent finished never removes the file again.
func (s *script) attempt(name string, a agentArgs, in PhaseInput, try int) (stepResult, error) {
	_, err := runStep(s.e, s.ctx, name+"/clear"+strconv.Itoa(try), func(context.Context) (stepResult, error) {
		if rmErr := os.Remove(a.file); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return stepResult{}, fmt.Errorf("remove the old output file: %w", rmErr)
		}

		return stepResult{}, nil
	})
	if err != nil {
		return stepResult{}, err
	}

	ended, err := s.e.queuePhase(s.ctx, in)
	if err != nil {
		return stepResult{}, err
	}

	return runStep(s.e, s.ctx, name+"/read"+strconv.Itoa(try), func(ctx context.Context) (stepResult, error) {
		return readAgent(ctx, a, ended)
	})
}

// readAgent checks the skills of a finished run, records its violations and reads its declared outputs.
// A violation does not fail the step: the workflow reads `violations` and decides. A problem with the output file is the Note of the result,
// so the caller can run the agent again. A missing skill is an error.
func readAgent(ctx context.Context, a agentArgs, ended PhaseOutput) (stepResult, error) {
	if err := missingSkill(a.requireSkills, ended.Skills); err != nil {
		return stepResult{}, err
	}

	head, err := folderHead(ctx, a.dir)
	if err != nil {
		return stepResult{}, err
	}

	if err = a.scope.check(ctx, a.dir, a.start); err != nil {
		return stepResult{}, err
	}

	outputs := agentOutputs(ended.Text, ended.Session, ended.Transcript, head, strings.Join(ended.Violations, "\n"), capOf(ended.Outcome))

	if problem := a.readOutputs(outputs); problem != "" {
		return stepResult{Note: problem}, nil
	}

	return stepResult{Outputs: outputs}, nil
}

// capOf names the cap that ended a run, or returns "" for a run that no cap ended.
func capOf(outcome agent.Outcome) string {
	if outcome == agent.OutcomeTurnCap {
		return string(agent.StopTurns)
	}

	if outcome == agent.OutcomeBudgetCap {
		return string(agent.StopBudget)
	}

	return ""
}

// readOutputs reads the output file when the call declares outputs, adds them to outputs, then applies the follow_ups option.
// A call that forbids follow-ups declares the output verdict, so it always has an output file. It returns what is wrong with the file, or an empty text.
func (a agentArgs) readOutputs(outputs map[string]any) string {
	// A run that a cap stopped returns its stop with no declared outputs: the workflow guard names the cap, and a missing output file would hide it.
	if len(a.declared) == 0 || outputs["stop"] != "" {
		return ""
	}

	written, problem := writtenObject(a.file)
	if problem != "" {
		return problem
	}

	if problem := addDeclared(outputs, a.declared, written); problem != "" {
		return problem
	}

	if a.noFollowUps {
		return applyFollowUps(outputs, written)
	}

	return ""
}

// missingSkill returns an error that names the first required skill that the run did not call.
func missingSkill(required, called []string) error {
	for _, skill := range required {
		if !slices.Contains(called, skill) {
			return fmt.Errorf("the agent did not call the skill %q. Check that the runner has the skill", skill)
		}
	}

	return nil
}

// addDeclared adds the named outputs from the JSON object the agent wrote to outputs. It returns what is wrong with the object, or an empty text.
func addDeclared(outputs map[string]any, names []string, written map[string]any) string {
	for _, name := range names {
		text, problem := stringOutput(written, name)
		if problem != "" {
			return problem
		}

		outputs[name] = text
	}

	return ""
}

// writtenObject reads the JSON object in the output file. The second result says what is wrong with the file.
func writtenObject(file string) (map[string]any, string) {
	raw, err := fs.ReadFile(os.DirFS(filepath.Dir(file)), filepath.Base(file))
	if err != nil {
		return nil, "the agent wrote no output file at " + file
	}

	var written map[string]any
	if err := json.Unmarshal(raw, &written); err != nil {
		return nil, fmt.Sprintf("the output file %s is not a JSON object: %v", file, err)
	}

	return written, ""
}

// stringOutput returns one declared output of the output file. The second result says what is wrong with it.
func stringOutput(written map[string]any, name string) (string, string) {
	v := written[name]
	if v == nil {
		return "", fmt.Sprintf("the agent did not write the output %q", name)
	}

	text, ok := v.(string)
	if !ok {
		return "", fmt.Sprintf("the output %q must be a string", name)
	}

	return text, ""
}

// folderHead returns the commit at HEAD of the item folder.
func folderHead(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read HEAD of the item folder: %w", err)
	}

	return strings.TrimSpace(out), nil
}
