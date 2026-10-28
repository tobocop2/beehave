package flow

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/tobocop2/obee/internal/hclfile"
	"github.com/tobocop2/obee/internal/workflow"
)

const (
	outputsFolder = "outputs"
	outputsMode   = 0o700
)

// agentArgs are the evaluated arguments of an agent step, and the folder and output file of its node.
type agentArgs struct {
	prompt        string
	data          map[string]any
	runner        string
	model         string
	timeout       time.Duration
	idleTimeout   time.Duration
	pin           string
	requireSkills []string
	resume        string
	tries         int
	dir           string
	file          string
}

// agentArgs evaluates the arguments of an agent step, in the scope of the node.
func (c *call) agentArgs(step *workflow.Agent) (agentArgs, error) {
	a, err := c.agentTexts(step)
	if err == nil {
		err = c.agentLimits(step, &a)
	}

	if err == nil {
		err = c.agentData(step, &a)
	}

	return a, err
}

// agentTexts evaluates the arguments that are text.
func (c *call) agentTexts(step *workflow.Agent) (agentArgs, error) {
	var (
		a   agentArgs
		err error
	)

	texts := []struct {
		expr hcl.Expression
		into *string
	}{{step.Prompt, &a.prompt}, {step.Runner, &a.runner}, {step.Model, &a.model}, {step.Pin, &a.pin}}

	for _, t := range texts {
		if *t.into, err = c.text(t.expr); err != nil {
			return agentArgs{}, err
		}
	}

	return a, nil
}

// agentLimits evaluates the two timeouts and the number of tries.
func (c *call) agentLimits(step *workflow.Agent, a *agentArgs) error {
	var err error

	if a.timeout, err = c.duration(step.Timeout); err != nil {
		return err
	}

	if a.idleTimeout, err = c.duration(step.IdleTimeout); err != nil {
		return err
	}

	a.tries, err = c.tries(step.OutputTries)

	return err
}

// agentData evaluates the required skills, the inputs of the prompt and the session to resume.
func (c *call) agentData(step *workflow.Agent, a *agentArgs) error {
	var err error

	if a.requireSkills, err = c.words(step.RequireSkills); err != nil {
		return err
	}

	if a.data, err = c.inputs(step.Inputs); err != nil {
		return err
	}

	a.resume, err = c.session(step.ResumeFrom)

	return err
}

// inputs evaluates the `inputs` object of an agent step into the data of its prompt template.
func (c *call) inputs(expr hcl.Expression) (map[string]any, error) {
	data := map[string]any{}

	if absent(expr) {
		return data, nil
	}

	value, err := c.value(expr)
	if err != nil {
		return nil, err
	}

	if !value.Type().IsObjectType() && !value.Type().IsMapType() {
		return nil, hclfile.At(expr.Range(), "inputs must be an object. Write inputs = { name = value }.")
	}

	// A value of an object or map type always marshals, so both calls succeed; the join keeps one return path.
	raw, marshalErr := ctyjson.Marshal(value, value.Type())

	return data, errors.Join(marshalErr, json.Unmarshal(raw, &data))
}

// session evaluates `resume_from`: the session output of the agent it names, or empty for a fresh session.
func (c *call) session(expr hcl.Expression) (string, error) {
	if absent(expr) {
		return "", nil
	}

	value, err := c.value(expr)
	if err != nil {
		return "", err
	}

	if value.IsNull() || !value.Type().IsObjectType() || !value.Type().HasAttribute("session") {
		return "", nil
	}

	if session := value.GetAttr("session"); !session.IsNull() {
		return session.AsString(), nil
	}

	return "", nil
}

// tries evaluates `output_tries`: how many runs the agent gets in all. An absent argument is one.
func (c *call) tries(expr hcl.Expression) (int, error) {
	if absent(expr) {
		return 1, nil
	}

	value, err := c.value(expr)
	if err != nil {
		return 0, err
	}

	number, convErr := convert.Convert(value, cty.Number)
	if convErr != nil || number.IsNull() {
		return 0, hclfile.At(expr.Range(), "output_tries must be a whole number of 1 or more.")
	}

	tries, accuracy := number.AsBigFloat().Int64()
	if accuracy != 0 || tries < 1 {
		return 0, hclfile.At(expr.Range(), "output_tries must be a whole number of 1 or more.")
	}

	return int(tries), nil
}

// outputFile is the path where the agent of a step writes its declared outputs. It is in the state folder of the engine, named by item and step,
// so the file never dirties the worktree of the item. A dot or a separator in a name is escaped, so two pairs of item and step never share a file.
func outputFile(state, item, step string) string {
	escape := func(name string) string {
		return strings.ReplaceAll(url.PathEscape(name), ".", "%2E")
	}

	return filepath.Join(state, outputsFolder, escape(item)+"."+escape(step)+".json")
}

// runAgent runs an agent step. The prompt is rendered once and recorded as the note of the first step, so a replay never reads the prompt file again.
// Each run of the agent is a phase on the agents queue, keyed by login. After each run a second step reads the output file and checks the skills.
func runAgent(c *call, step *workflow.Agent) (stepResult, error) {
	args, err := c.prepare(step)
	if err != nil {
		return stepResult{}, err
	}

	rendered, err := c.durably(func(ctx context.Context) (stepResult, error) {
		prompt, renderErr := c.render(ctx, args)

		return stepResult{Note: prompt}, renderErr
	})
	if err != nil {
		return stepResult{}, err
	}

	return c.attempts(step, args, args.phase(c.w.in, cmp.Or(args.runner, c.w.login), rendered.Note))
}

// prepare evaluates the arguments of an agent step and finds its folder and output file.
func (c *call) prepare(step *workflow.Agent) (agentArgs, error) {
	args, err := c.agentArgs(step)
	if err != nil {
		return agentArgs{}, err
	}

	if args.dir, err = c.w.e.deps.Trees.Ensure(c.w.ctx, c.w.in.Project, c.w.in.Item); err != nil {
		return agentArgs{}, fmt.Errorf("find the folder of item %s: %w", c.w.in.Item, err)
	}

	if args.dir == "" {
		return agentArgs{}, fmt.Errorf("item %s of project %s has no folder for the agent", c.w.in.Item, c.w.in.Project)
	}

	args.file = outputFile(c.w.e.dir, c.w.in.Item, c.name)

	if err = os.MkdirAll(filepath.Dir(args.file), outputsMode); err != nil {
		return agentArgs{}, fmt.Errorf("make the folder of the output file: %w", err)
	}

	return args, nil
}

// phase makes the input of the phase that runs the agent.
func (a agentArgs) phase(in ItemInput, login, prompt string) PhaseInput {
	return PhaseInput{
		Item: in.Item, Project: in.Project, Login: login, Prompt: prompt,
		Resume: a.resume, Model: a.model, Timeout: a.timeout, IdleTimeout: a.idleTimeout,
	}
}

// render checks the pin, reads the prompt file and fills it. The data are the inputs and output_file.
func (c *call) render(ctx context.Context, args agentArgs) (string, error) {
	if err := checkPin(ctx, args.dir, args.pin); err != nil {
		return "", err
	}

	path, text, err := c.promptFile(args.prompt)
	if err != nil {
		return "", err
	}

	data := maps.Clone(args.data)
	data["output_file"] = args.file

	filled, err := fill(string(text), data)
	if err != nil {
		return "", fmt.Errorf("prompt file %s: %w", path, err)
	}

	return filled, nil
}

// promptFile reads the prompt file. A relative path starts at the folder of the workflow file.
func (c *call) promptFile(path string) (string, []byte, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(c.w.in.File), path)
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

// attempts runs the agent up to args.tries times in all. A run that leaves a bad output file is run again.
func (c *call) attempts(step *workflow.Agent, args agentArgs, in PhaseInput) (stepResult, error) {
	for try := 1; ; try++ {
		result, err := c.attempt(step, args, in, try)
		if err != nil || result.Note == "" {
			return result, err
		}

		if try >= args.tries {
			return stepResult{}, errors.New(result.Note)
		}
	}
}

// attempt removes the old output file in a recorded step, runs the agent once and reads what it left.
// The removal is recorded, so a replay after the agent finished never removes the file again.
func (c *call) attempt(step *workflow.Agent, args agentArgs, in PhaseInput, try int) (stepResult, error) {
	_, err := runStep(c.w.e, c.w.ctx, c.name+"/clear"+strconv.Itoa(try), func(context.Context) (stepResult, error) {
		if rmErr := os.Remove(args.file); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return stepResult{}, fmt.Errorf("remove the old output file: %w", rmErr)
		}

		return stepResult{}, nil
	})
	if err != nil {
		return stepResult{}, err
	}

	ended, err := c.w.e.queuePhase(c.w.ctx, in)
	if err != nil {
		return stepResult{}, err
	}

	return runStep(c.w.e, c.w.ctx, c.name+"/read"+strconv.Itoa(try), func(ctx context.Context) (stepResult, error) {
		return readAgent(ctx, step, args, ended)
	})
}

// readAgent checks the skills of a finished run and reads its declared outputs. A problem with the output file is the Note of the result,
// so the caller can run the agent again. A missing skill is an error.
func readAgent(ctx context.Context, step *workflow.Agent, args agentArgs, ended PhaseOutput) (stepResult, error) {
	if err := missingSkill(args.requireSkills, ended.Skills); err != nil {
		return stepResult{}, err
	}

	head, err := folderHead(ctx, args.dir)
	if err != nil {
		return stepResult{}, err
	}

	outputs := map[string]any{"text": ended.Text, "session": ended.Session, "transcript": ended.Transcript, "head_sha": head}

	if problem := addDeclared(outputs, declaredNames(step), args.file); problem != "" {
		return stepResult{Note: problem}, nil
	}

	return stepResult{Outputs: outputs}, nil
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

// declaredNames returns the outputs that the step declares in `outputs`, which are the ones that workflow.KindOutputs does not list.
func declaredNames(step *workflow.Agent) []string {
	kind := workflow.KindOutputs(workflow.KindAgent)

	var names []string

	for _, o := range workflow.StepOutputs(step) {
		if !slices.ContainsFunc(kind, func(k workflow.Output) bool { return k.Name == o.Name }) {
			names = append(names, o.Name)
		}
	}

	return names
}

// addDeclared reads the named outputs from the JSON file the agent wrote and adds them to outputs. It returns what is wrong with the file, or an empty text.
func addDeclared(outputs map[string]any, names []string, file string) string {
	if len(names) == 0 {
		return ""
	}

	written, problem := writtenObject(file)
	if problem != "" {
		return problem
	}

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
