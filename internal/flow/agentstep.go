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

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	outputsFolder = "outputs"
	outputsMode   = 0o700
	// inputsFolder holds the data files of the agent calls, beside the output files. dataMode is the mode of a data file.
	inputsFolder = "inputs"
	dataMode     = 0o600
)

// agentArgs are the arguments of an agent call, and the folder and output file of its step.
type agentArgs struct {
	prompt string
	data   map[string]any
	runner string
	// label is the label keyword: a free text that the ledger and the status line show. It picks no behavior.
	label       string
	model       string
	timeout     time.Duration
	idleTimeout time.Duration
	// maxBudget is the max_budget_usd of the step: the most dollars that the agent may spend. 0 means the step sets no cap.
	maxBudget float64
	// maxContext is the max_context_percent of the step: the share of the context window, in percent, at which the step stops with the stop reason context. 0 means no limit.
	maxContext int
	pin        string
	forbid     []model.FindingKind
	protect    []string
	scope      scope
	start      startMark
	// plain is true when the project has no repository: the item folder is a plain folder, and no step reads git there.
	plain bool
	// startHead is the commit of the item folder when the render step ran, which is before the agent starts. It is the baseline of the whole call, not of one try. It is empty for a record made before the render step kept it.
	startHead string
	declared  []string
	resume    string
	// skill is the skill keyword: the skill that the CLI loads before the first turn. Empty means none. skillLine is the line of the CLI that loads it, which the call reads before the render step.
	skill     string
	skillLine string
	// edits is the edits argument: false starts the step so that Claude Code refuses each file edit.
	edits bool
	tries int
	dir   string
	// git is the runner of the engine for git in dir.
	git  proc.Runner
	file string
	// sandbox is the sandbox keyword as written. box is the sandbox the step runs with, which the render step records, or nil for no sandbox.
	sandbox sandboxChoice
	box     *model.Sandbox
	// skills are the skills that the prompt declares through the template function skill, which the render step records. The step gets these skills.
	skills []string
	// record is the JSON text of the data keyword, and dataFile the file that the prompt names for it. Both are empty for a call with no data.
	record   string
	dataFile string
}

// agentCall unpacks the arguments of the agent builtin. The second result is the label of the call.
func agentCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (agentArgs, string, error) {
	a := agentArgs{tries: 1, edits: true}
	raw := agentRaw{}

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"prompt": &a.prompt, "model": &a.model, "runner": &a.runner, "inputs": &raw.inputs, "outputs": &raw.outputs, "pin": &a.pin,
		"resume": &a.resume, "skill": &a.skill, "forbid_commands": &raw.kinds, "allow_paths": &raw.allow, "protect_paths": &raw.protect, "output_tries": &a.tries,
		"edits": &a.edits, workflow.LabelKeyword: &a.label, timeoutKeyword: &raw.timeout, "idle_timeout": &raw.idle, "max_budget_usd": &raw.budget, "max_context_percent": &a.maxContext, string(sandboxKeyword): &raw.sandbox, nameKeyword: &raw.name,
		"data": &raw.data,
	})
	if err == nil && a.model == "" {
		err = errors.New("an agent call needs a model. Add model = \"sonnet\" or the name of another model")
	}

	if err != nil {
		return a, "", err
	}

	return a, raw.name, a.read(raw)
}

// agentRaw are the arguments of an agent call that need a check before they are used. name is the name keyword, the suffix of the step name.
type agentRaw struct {
	name, timeout, idle             string
	inputs, outputs, kinds          starlark.Value
	allow, protect, sandbox, budget starlark.Value
	data                            starlark.Value
}

// read checks the raw arguments of an agent call and keeps the result in a.
func (a *agentArgs) read(raw agentRaw) (err error) {
	if err = workflow.OutputTries(a.tries); err != nil {
		return err
	}

	if err = a.limits(raw); err != nil {
		return err
	}

	if err = a.values(raw); err != nil {
		return err
	}

	if a.sandbox, err = readSandbox(raw.sandbox); err != nil {
		return err
	}

	return a.lists(raw)
}

// values reads the inputs of the prompt and the data of the data file of an agent call.
func (a *agentArgs) values(raw agentRaw) (err error) {
	if a.data, err = workflow.InputMap(raw.inputs); err != nil {
		return err
	}

	a.record, err = workflow.DataRecord(raw.data)

	return err
}

// limits reads the timeout, the idle timeout and the dollar cap of an agent call.
func (a *agentArgs) limits(raw agentRaw) (err error) {
	if a.timeout, err = workflow.Duration("timeout", raw.timeout); err != nil {
		return err
	}

	if a.idleTimeout, err = workflow.Duration("idle_timeout", raw.idle); err != nil {
		return err
	}

	if raw.budget == nil { // the keyword is left out: the step sets no cap
		return nil
	}

	a.maxBudget, err = workflow.Dollars("max_budget_usd", raw.budget)

	return err
}

// lists reads the list arguments of an agent call.
func (a *agentArgs) lists(raw agentRaw) (err error) {
	if a.declared, err = workflow.Words("outputs", raw.outputs); err != nil {
		return err
	}

	if a.forbid, a.protect, err = forbidden(raw.kinds, raw.protect); err != nil {
		return err
	}

	a.scope, err = readScope(raw.allow)

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

	return paths, workflow.Protection(forbid, paths)
}

// outputFile is the path where the agent of a step writes its declared outputs: the one file of a folder of its own in the outputs folder of the state
// folder, so the file never dirties the worktree of the item and the sandbox can name that folder as a working directory and open no other output file.
func outputFile(state, item, step string) string {
	return filepath.Join(callFolder(state, outputsFolder, item, step), "output.json")
}

// dataFile is the path where the phase of a step writes the data of the call: the one file of a folder of its own in the inputs folder of the state folder,
// so the sandbox can name that folder as a working directory and open no other file.
func dataFile(state, item, step string) string {
	return filepath.Join(callFolder(state, inputsFolder, item, step), "data.json")
}

// callFolder is the folder of one item and step in a folder of the state folder. A dot or a separator in a name is escaped, so two pairs of item and step
// never share a folder.
func callFolder(state, folder, item, step string) string {
	return filepath.Join(state, folder, model.SafeName(item)+"."+model.SafeName(step))
}

// dataFileOf is the data file of the call, or "" for a call with no data.
func (a agentArgs) dataFileOf(state, item, step string) string {
	if a.record == "" {
		return ""
	}

	return dataFile(state, item, step)
}

// agent runs an agent call. The prompt is rendered once and recorded as the note of the first step, so a replay never reads the prompt file again.
// Each run of the agent is a phase on the agents queue, keyed by login. After each run a second step reads the output file and checks the skills.
func (s *script) agent(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	a, label, err := agentCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	name, login, prompt, err := s.ready(&a, label)
	if err != nil {
		return nil, err
	}

	outputs, err := s.agentAndCommit(name, a, a.phase(s.in, login, prompt, s.treeStart))
	if err != nil {
		return nil, err
	}

	return result(outputs)
}

// ready returns the step name, the login and the rendered prompt of an agent call. The login comes first, so a call that waits for a usage cap holds no start mark.
func (s *script) ready(a *agentArgs, label string) (name, login, prompt string, err error) {
	if name, err = s.name("agent", label); err != nil {
		return "", "", "", err
	}

	a.box = a.sandbox.effective(s.runMode())

	if login, err = s.loginFor(*a); err != nil {
		return "", "", "", err
	}

	if err = s.readSkillLine(a, login); err != nil {
		return "", "", "", err
	}

	prompt, err = s.promptOf(a, name, login)

	return name, login, prompt, err
}

// readSkillLine reads the line that loads the skill keyword on the runner of the call. A runner with no such line refuses the step before the render step and any agent start.
func (s *script) readSkillLine(a *agentArgs, login string) error {
	if a.skill == "" {
		return nil
	}

	if a.skillLine = s.e.skillLine(login, a.skill); a.skillLine == "" {
		return fmt.Errorf("runner %s has no line that loads a skill before the first turn, so the step cannot start with the skill %s. The step did not start. Run it on a runner of an agent that has one", login, a.skill)
	}

	return nil
}

// promptOf finds the folder and the output file of the call, then renders the prompt in a step and returns it. login is the runner that the call runs on.
func (s *script) promptOf(a *agentArgs, name, login string) (string, error) {
	if err := s.prepare(a, name); err != nil {
		return "", err
	}

	if err := s.markStart(a, name); err != nil {
		return "", err
	}

	// The sandbox is recorded with the prompt, in the same step, so a replay reads the sandbox of the first run and never the current default of the project.
	rendered, err := runStep(s.e, s.ctx, name+"/render", s.renderStep(*a, login))
	if err != nil {
		return "", err
	}

	return rendered.Note, a.recall(rendered.Outputs)
}

// recall reads the sandbox and the output file that the render step recorded. A replay keeps the recorded path.
// A call that really starts and declares outputs stops before the step that makes the empty file when the recorded path is not the one file of the folder of its call.
// A finished call replays from its record and never runs that check.
// A record made before the path was pinned keeps the computed path. A recorded prompt that names a data file other than
// the file that the phase writes for this item and step stops the call, because the agent would read a file that the engine did not write.
func (a *agentArgs) recall(outputs map[string]any) (err error) {
	a.file = recalledOutputFile(outputs, a.file)
	a.startHead, _ = outputs[recordedStartHead].(string) //nolint:errcheck // a record made before the start head was kept has none
	given, err := recalledData(outputs)
	if err != nil {
		return err
	}

	if given.File != "" && given.File != a.dataFile {
		return fmt.Errorf("the recorded prompt names the data file %s, and the data file of this item and step is %s. The step did not start. Start a new run", given.File, a.dataFile)
	}

	a.record = given.Text

	if err = a.makeOutputFolder(); err != nil {
		return err
	}

	return a.recallLimits(outputs)
}

// recallLimits reads the sandbox and the skills that the render step recorded. A record that holds neither replays with neither.
func (a *agentArgs) recallLimits(outputs map[string]any) (err error) {
	if a.box, err = recalledSandbox(outputs); err != nil {
		return err
	}

	a.skills, err = recalledSkills(outputs)

	return err
}

// renderStep returns the step that renders the prompt of an agent call, records the sandbox, the lane kind, the login and the model of the call and marks its start commit.
func (s *script) renderStep(a agentArgs, login string) func(context.Context) (stepResult, error) {
	return func(ctx context.Context) (stepResult, error) {
		prompt, skills, err := s.render(ctx, a)
		if err != nil {
			return stepResult{Note: prompt}, err
		}

		head, err := a.writeStartMark(ctx)
		if err != nil {
			return stepResult{Note: prompt}, err
		}

		a.skills = skills

		return stepResult{Note: prompt, Outputs: a.recordOutputs(head, login)}, nil
	}
}

// recordOutputs is the record of the render step: the output file, the start commit, the login, the label and model when set, the sandbox, the skills of the prompt, and the data and its file.
func (a agentArgs) recordOutputs(head, login string) map[string]any {
	outputs := map[string]any{recordedOutputFile: a.file, recordedStartHead: head, recordedLogin: login}
	if a.label != "" {
		outputs[recordedLabel] = a.label
	}

	if a.model != "" {
		outputs[recordedModel] = a.model
	}

	maps.Copy(outputs, recordSandbox(a.box))
	maps.Copy(outputs, recordSkills(a.skills))
	maps.Copy(outputs, recordData(promptData{File: a.dataFile, Text: a.record}))

	return outputs
}

// prepare finds the folder and the output file of an agent call.
func (s *script) prepare(a *agentArgs, name string) (err error) {
	if a.dir, err = s.tree(s.ctx); err != nil {
		return fmt.Errorf("find the folder of item %s: %w", s.in.Item, err)
	}

	if a.dir == "" {
		return fmt.Errorf("item %s of project %s has no folder for the agent", s.in.Item, s.in.Project)
	}

	a.git, a.plain = s.e.deps.Trees.Git(a.dir), !s.hasRepository()
	if err = a.refusePlain(s.in.Project); err != nil {
		return err
	}

	a.file, a.dataFile = outputFile(s.e.dir, s.in.Item, name), a.dataFileOf(s.e.dir, s.in.Item, name)

	return a.makeOutputFolder()
}

// makeOutputFolder makes the folder of the output file of a call that declares outputs or commits. A call with neither gets no folder.
func (a agentArgs) makeOutputFolder() error {
	if len(a.wanted()) == 0 {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(a.file), outputsMode); err != nil {
		return fmt.Errorf("make the folder of the output file: %w", err)
	}

	return nil
}

// refusePlain refuses the keywords that read git in an item folder of a project with no repository: the pin and the allowed paths.
// It accepts every call of a project with a repository.
func (a agentArgs) refusePlain(project string) error {
	if !a.plain {
		return nil
	}

	for keyword, set := range map[string]bool{"pin": a.pin != "", "allow_paths": a.scope.active()} {
		if set {
			return fmt.Errorf("agent keyword %s reads git, and project %s has no repository. Set `source` or `forge` in the project file, or remove %s", keyword, project, keyword)
		}
	}

	return nil
}

// phase makes the input of the phase that runs the agent. treeStart is the commit of the worktree of a round item that the run made.
func (a agentArgs) phase(in ItemInput, login, prompt, treeStart string) PhaseInput {
	return PhaseInput{
		Item: in.Item, Project: in.Project, Login: login, Prompt: prompt,
		Resume: a.resume, Model: a.model, Timeout: a.timeout, IdleTimeout: a.idleTimeout, Forbid: a.forbid, Protect: a.protect, MaxBudget: a.maxBudget, MaxContextPercent: a.maxContext,
		NoEdits: !a.edits, OutputFile: a.file, Outputs: a.wanted(), Sandbox: a.box, Skills: a.skills, StartHead: a.startHead, TreeStart: treeStart, Base: in.Base,
		Data: a.record,
	}
}

// render checks the pin, reads the prompt file and fills it. The data are the inputs, output_file and data_file.
// It returns the prompt and the skills that the prompt declares.
func (s *script) render(ctx context.Context, a agentArgs) (string, []string, error) {
	if err := checkPin(ctx, a.git, a.pin); err != nil {
		return "", nil, err
	}

	path, text, err := s.promptFile(a.prompt)
	if err != nil {
		return "", nil, err
	}

	filled, skills, err := fill(string(text), templateData(a))
	if err != nil {
		return "", nil, fmt.Errorf("prompt file %s: %w", path, err)
	}

	filled, skills = withSkillLine(a, filled, skills)

	return filled, skills, nil
}

// templateData is the data that fills the template of the prompt of a step: the inputs, output_file and data_file.
func templateData(a agentArgs) map[string]any {
	data := maps.Clone(a.data)
	data["output_file"] = a.file
	data["data_file"] = a.dataFile

	return data
}

// withSkillLine starts the prompt with the line that loads the skill keyword of the step, and gives the step the skill like each declared one.
// The CLI loads only the first skill of the prompt, so the line is the first line. A step with no skill keyword is unchanged.
func withSkillLine(a agentArgs, prompt string, skills []string) (string, []string) {
	if a.skill == "" {
		return prompt, skills
	}

	return a.skillLine + "\n" + prompt, withSkill(skills, a.skill)
}

// withSkill returns the skills with the name added at the end, once.
func withSkill(skills []string, name string) []string {
	if slices.Contains(skills, name) {
		return skills
	}

	return append(skills, name)
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

// fill executes a prompt template and returns the text with the skills that the template declares. A name that the data lacks is an error.
func fill(text string, data map[string]any) (string, []string, error) {
	var declared skillDeclarations

	tmpl, err := template.New("prompt").Option("missingkey=error").Funcs(declared.funcs()).Parse(text)
	if err != nil {
		return "", nil, err
	}

	var out bytes.Buffer

	err = tmpl.Execute(&out, data)

	return out.String(), declared.names, err
}

// checkPin refuses a folder whose HEAD is not the pin. An empty pin accepts any HEAD.
func checkPin(ctx context.Context, tree proc.Runner, pin string) error {
	if pin == "" {
		return nil
	}

	head, err := folderHead(ctx, tree)
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

		if try >= a.tries || done.Final {
			return stepResult{}, errors.New(done.Note)
		}
	}
}

// attempt removes the old output file in a recorded step, runs the agent and reads what it left.
// The removal is recorded, so a replay after the agent finished never removes the file again.
// A run that a provider limit stopped is followed by a wait for the reset and a rerun inside the same try: a limit spends no output try and no lane.
// Each phase looks at the caps first, because the limited run can have spent money before the limit stopped it.
func (s *script) attempt(name string, a agentArgs, in PhaseInput, try int) (stepResult, error) {
	in.Severity, in.Call = string(s.severity), name

	if err := s.clearOutput(name, a, in, try); err != nil {
		return stepResult{}, err
	}

	ended, err := s.runPastLimits(name, in, try)
	if err != nil {
		return stepResult{}, err
	}

	return s.read(name, a, ended, try)
}

// runPastLimits runs a new phase of one try after each cap stop and each provider limit, and returns the first run that neither stopped. Each limit rerun starts from base.
func (s *script) runPastLimits(name string, base PhaseInput, try int) (PhaseOutput, error) {
	look, in, counts := 0, base, limitCounts{}

	for {
		ended, err := s.runOnce(name, in)

		switch {
		case err != nil || (ended.CapStop == nil && ended.Limit == nil):
			return ended, err
		case ended.CapStop != nil:
			err = s.holdAtCap(name, try, look, ended.CapStop)
			look++
		default:
			in, err = s.waitOutLimit(name, base, in, ended, try, &counts)
		}

		if err != nil {
			return PhaseOutput{}, err
		}
	}
}

// runOnce runs the agent once as a phase. The cost of a run that failed goes to the stream of failed runs, which a resume keeps, so every total
// and the run cap count it after a resume too. The write and its step record are one transaction, so a replay appends nothing.
// The error of the failed run stays the error of the step, so a failure to write the cost is not reported over it.
func (s *script) runOnce(name string, in PhaseInput) (PhaseOutput, error) {
	ended, err := s.e.queuePhase(s.ctx, in)
	if err == nil || !s.running() {
		return ended, err
	}

	if writeErr := dbos.WriteStream(s.ctx, failedRunsStream, failedRun{Call: name, Spend: ended.Spend}); writeErr != nil {
		s.e.logger.Warn("cannot keep the cost of the failed agent run; the totals leave it out", "item", s.in.Item, "step", name, "error", writeErr)
	}

	return ended, err
}

// running is false once the run is stopped. A stopped wait leaves its reading goroutine running, and it shares the step counter of the run:
// a stopped run records no step.
func (s *script) running() bool {
	return s.ctx.Err() == nil
}

// read reads what a finished run left, in a recorded step. The run cap counts the cost from the record.
func (s *script) read(name string, a agentArgs, ended PhaseOutput, try int) (stepResult, error) {
	return runStep(s.e, s.ctx, name+"/read"+strconv.Itoa(try), func(ctx context.Context) (stepResult, error) {
		return readAgent(ctx, a, ended)
	})
}

// countFailedRun records the cost of a run whose limit step failed, in a step of its own, so the totals count it. The error of the limit step
// stays the error of the step, so a failure to record the cost is not reported over it. The caller records it only while the run is running.
// Only the failure of the limit step writes this record.
func (s *script) countFailedRun(name string, try int, spend agent.Spend) {
	_, err := runStep(s.e, s.ctx, name+"/spent"+strconv.Itoa(try), func(context.Context) (stepResult, error) {
		return stepResult{Outputs: spendOutputs(spend)}, nil
	})
	if err != nil {
		s.e.logger.Warn("cannot record the cost of the limited agent run; the totals leave it out", "item", s.in.Item, "step", name, "error", err)
	}
}

// clearOutput makes the output file of an agent call empty in a recorded step: it removes the old file, and a call that declares outputs gets a new empty one.
// A call whose output file is not the one file of its own folder stops first, in the same step, so the stop makes no file and a replay of a finished step does not run it.
func (s *script) clearOutput(name string, a agentArgs, in PhaseInput, try int) error {
	_, err := runStep(s.e, s.ctx, name+"/clear"+strconv.Itoa(try), func(context.Context) (stepResult, error) {
		if checkErr := s.e.checkOutputFile(in); checkErr != nil {
			return stepResult{}, checkErr
		}

		return stepResult{}, a.emptyOutput()
	})

	return err
}

// emptyOutput removes the old output file and makes a new empty one. A call that declares no outputs and does not commit has no output file and no folder, so it touches nothing.
// The step stays recorded for such a call, so the step numbers of a run recorded before this rule still replay.
func (a agentArgs) emptyOutput() error {
	if len(a.wanted()) == 0 {
		return nil
	}

	if err := os.Remove(a.file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove the old output file: %w", err)
	}

	if err := os.WriteFile(a.file, nil, dataMode); err != nil {
		return fmt.Errorf("make the empty output file: %w", err)
	}

	return nil
}

// dollars writes an amount of dollars with at most four decimals and no trailing zero.
func dollars(amount float64) string {
	return strings.TrimSuffix(strings.TrimRight(strconv.FormatFloat(amount, 'f', 4, floatBits), "0"), ".")
}

// readAgent records the violations and the skills of a finished run and reads its declared outputs.
// A violation does not fail the step: the workflow reads `violations` and decides. A problem with the output file is the Note of the result,
// so the caller can run the agent again.
func readAgent(ctx context.Context, a agentArgs, ended PhaseOutput) (stepResult, error) {
	head, rewrote, violations, err := a.endState(ctx, ended.Violations)
	if err != nil {
		return stepResult{}, err
	}

	outputs := agentOutputs(ended.Text, ended.Session, ended.Transcript, head, strings.Join(violations, "\n"), capOf(ended.Outcome), ended.Spend, rewrote, a.startHead != "" && head != a.startHead, ended.Skills)

	// A run that left a bad output file still cost money, so its outputs stay in the result and the run cap counts them.
	if problem := a.refusedEdits(ended); problem != "" {
		return stepResult{Note: problem, Final: true, Outputs: spendOutputs(ended.Spend)}, nil
	}

	return a.readAll(ctx, outputs, ended)
}

// readAll reads the declared outputs and then the commit message from the output file. The first problem is the Note of the result, and the run still
// costs what it cost.
func (a agentArgs) readAll(ctx context.Context, outputs map[string]any, ended PhaseOutput) (stepResult, error) {
	problem := a.readOutputs(outputs, ended.Denials)

	if problem == "" {
		var err error

		if problem, err = a.readCommit(ctx, outputs); err != nil {
			return stepResult{}, err
		}
	}

	if problem != "" {
		return stepResult{Note: problem, Outputs: spendOutputs(ended.Spend)}, nil
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

	if outcome == agent.OutcomeContextCap {
		return string(agent.StopContext)
	}

	return ""
}

// readOutputs reads the output file when the call declares outputs and adds them to outputs. It returns what is wrong with the file, or an empty text.
// A missing file names each call to it that the permission check of the agent CLI refused.
func (a agentArgs) readOutputs(outputs map[string]any, denials []agent.Denial) string {
	// A run that a cap stopped returns its stop with no declared outputs: the workflow guard names the cap, and a missing output file would hide it.
	if len(a.declared) == 0 || outputs["stop"] != "" {
		return ""
	}

	written, problem := writtenObject(a.file, denials)
	if problem != "" {
		return problem
	}

	return addDeclared(outputs, a.declared, written)
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

// writtenObject reads the JSON object in the output file. The second result says what is wrong with the file. A missing or empty file names the tools whose
// calls to write it the permission check of the agent CLI refused.
func writtenObject(file string, denials []agent.Denial) (map[string]any, string) {
	raw, err := fs.ReadFile(os.DirFS(filepath.Dir(file)), filepath.Base(file))
	if err != nil || len(raw) == 0 {
		return nil, "the agent wrote no output file at " + file + refusedWrites(file, denials)
	}

	var written map[string]any
	if err := json.Unmarshal(raw, &written); err != nil {
		return nil, fmt.Sprintf("the output file %s is not a JSON object: %v", file, err)
	}

	return written, ""
}

// refusedWrites says which calls on file the permission check of the agent CLI refused, each by what it does to the file, or returns "".
func refusedWrites(file string, denials []agent.Denial) string {
	var calls []string

	for _, denial := range denials {
		if call := refusedCall(file, denial); call != "" {
			calls = append(calls, call)
		}
	}

	if len(calls) == 0 {
		return ""
	}

	slices.Sort(calls)

	return ": the permission check of the agent CLI refused the " + strings.Join(slices.Compact(calls), " and the ")
}

// refusedCall names a refused call by what it does to file: a write, a read, or a command that names it. A call that does not touch file has no name.
func refusedCall(file string, denial agent.Denial) string {
	switch {
	case samePath(denial.Target, file) && denial.Edit:
		return denial.Tool + " call that writes it"
	case samePath(denial.Target, file):
		return denial.Tool + " call that reads it"
	case namesPath(denial.Target, file):
		return denial.Tool + " call whose command names it"
	}

	return ""
}

// commandBounds are the characters on each side of a word of a shell command: a blank, a quote, a backtick, a redirect, an operator or the = of a flag.
const commandBounds = " \t\n'\"`<>;&|()="

// namesPath reports whether a shell command holds path as a whole word, so the command names that file and not a longer path that starts or ends with it.
func namesPath(command, path string) bool {
	for at := strings.Index(command, path); at >= 0; at = nextIndex(command, path, at) {
		end := at + len(path)
		before, after := at == 0 || strings.ContainsRune(commandBounds, rune(command[at-1])), end == len(command) || strings.ContainsRune(commandBounds, rune(command[end]))

		if before && after {
			return true
		}
	}

	return false
}

// nextIndex returns the index of the next path in command after the one at at, or -1.
func nextIndex(command, path string, at int) int {
	next := strings.Index(command[at+1:], path)
	if next < 0 {
		return -1
	}

	return at + 1 + next
}

// refusedEdits says which files of the work folder the agent CLI refused to edit, though the step may edit them and no deny rule covers them, with what the CLI
// wrote on its error stream, or returns "" when it refused none.
func (a agentArgs) refusedEdits(ended PhaseOutput) string {
	files := a.refusedWorkFiles(ended.Denials)
	if len(files) == 0 {
		return ""
	}

	note := fmt.Sprintf("the agent CLI refused the edits of %s, files of the work folder that the step may edit, and no deny rule covers them, "+
		"so the step stopped with no other try", strings.Join(slices.Compact(files), " and "))

	if stderr := strings.TrimSpace(ended.Stderr); stderr != "" {
		note += ". The agent CLI wrote on its error stream: " + stderr
	}

	return note
}

// refusedWorkFiles returns the files of the work folder whose edits the permission check refused and no deny rule covers, sorted, for a step that may
// edit, or none.
func (a agentArgs) refusedWorkFiles(denials []agent.Denial) []string {
	var files []string

	for _, denial := range denials {
		if a.edits && denial.Edit && !denial.Closed && insideFolder(a.dir, denial.Target) {
			files = append(files, denial.Target)
		}
	}

	slices.Sort(files)

	return files
}

// insideFolder says whether path is in the folder dir or below it.
func insideFolder(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)

	return err == nil && filepath.IsLocal(rel)
}

// samePath says whether two paths name the same file. It cleans both and resolves the links of their folders, so the file need not exist.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}

	folderA, errA := filepath.EvalSymlinks(filepath.Dir(a))
	folderB, errB := filepath.EvalSymlinks(filepath.Dir(b))

	return errA == nil && errB == nil && folderA == folderB && filepath.Base(a) == filepath.Base(b)
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

// writeStartMark reads the commit at HEAD of the item folder and writes it to the start mark of the call. A plain folder has no commit, so it reads
// and writes nothing and returns an empty head.
func (a agentArgs) writeStartMark(ctx context.Context) (string, error) {
	if a.plain {
		return "", nil
	}

	head, err := folderHead(ctx, a.git)
	if err != nil {
		return "", err
	}

	if _, err = git(ctx, a.git, "update-ref", callStartRef, head); err != nil {
		return "", fmt.Errorf("write the start mark of the item folder: %w", err)
	}

	return head, nil
}

// checkEnd reads the head of the folder, checks the paths the agent changed against the scope and tells whether it rewrote the history.
// A plain folder has no history: the head is empty, nothing is checked, and rewrote is false.
func (a agentArgs) checkEnd(ctx context.Context) (head string, rewrote bool, err error) {
	if a.plain {
		return "", false, nil
	}

	if head, err = folderHead(ctx, a.git); err != nil {
		return "", false, err
	}

	if err = a.scope.check(ctx, a.git, a.start); err != nil {
		return "", false, err
	}

	rewrote, err = rewroteHistory(ctx, a.git, a.startHead, head)

	return head, rewrote, err
}

// endState reads the folder after a run: its head, whether the run rewrote history, and the violations of the run with one more line
// when a call with edits = False changed the folder.
func (a agentArgs) endState(ctx context.Context, violations []string) (head string, rewrote bool, all []string, err error) {
	head, rewrote, err = a.checkEnd(ctx)
	if err != nil {
		return "", false, nil, err
	}

	changed, err := a.changedReadOnly(ctx, head)
	if err != nil {
		return "", false, nil, err
	}

	if changed {
		violations = append(slices.Clone(violations), "a step with edits = False changed the item folder: a tracked file or the commit moved")
	}

	return head, rewrote, violations, nil
}

// changedReadOnly is true when a call with edits = False left the folder other than it found it: HEAD moved, or a tracked file differs from HEAD.
// The permission mode refuses the file tools only, so a shell write passes it. Untracked files do not count: a reviewer may build tables there.
// A plain folder, and a record made before the render step kept the start head, are not checked.
func (a agentArgs) changedReadOnly(ctx context.Context, head string) (bool, error) {
	if a.edits || a.plain || a.startHead == "" {
		return false, nil
	}

	if head != a.startHead {
		return true, nil
	}

	out, err := git(ctx, a.git, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, fmt.Errorf("read the tracked changes of the item folder: %w", err)
	}

	return strings.TrimSpace(out) != "", nil
}

// rewroteHistory is true when the commit the agent started on is not in the history of the commit it ended on: the agent amended, squashed or reset.
// The ancestor test of git answers with exit code 1 for "not an ancestor"; any other failure is an error.
func rewroteHistory(ctx context.Context, tree proc.Runner, start, head string) (bool, error) {
	if start == "" || start == head {
		return false, nil
	}

	_, err := git(ctx, tree, "merge-base", "--is-ancestor", start, head)

	var exit *gitExit
	if errors.As(err, &exit) && exit.code == 1 {
		return true, nil
	}

	if err != nil {
		return false, fmt.Errorf("compare the start of the agent with HEAD of the item folder: %w", err)
	}

	return false, nil
}

// folderHead returns the commit at HEAD of the item folder.
func folderHead(ctx context.Context, tree proc.Runner) (string, error) {
	out, err := git(ctx, tree, revParse, "HEAD")
	if err != nil {
		return "", fmt.Errorf("read HEAD of the item folder: %w", err)
	}

	return strings.TrimSpace(out), nil
}
