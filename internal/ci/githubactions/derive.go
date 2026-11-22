// Package githubactions reads GitHub Actions workflow files into the gate steps of a project.
package githubactions

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/tobocop2/beehave/internal/check/shell"
	"github.com/tobocop2/beehave/internal/ci"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	stepJoin       = "; "
	unnamedStep    = "run-"
	nameJoin       = ":"
	nameSeparator  = "-"
	expressionText = "${EXPR}"
	readAll        = "read-all"
	tokenField     = "token"
	mergeTag       = "!!merge"

	// maxExpandedNodes bounds the nodes of a workflow file with its aliases expanded, so that nested aliases cannot make the record of a step grow past any use.
	maxExpandedNodes = 100_000

	writePermission = "can write: the token of the run is not read only. Set permissions to read-all, {} or read and none values, at the job or the file"
	readsSecretText = "reads a secret"
)

var (
	// expressionPart is one expression of the workflow, `${{ ... }}`, with its body as the first group.
	expressionPart = regexp.MustCompile(`(?s)\$\{\{(.*?)\}\}`)
	nonNameRun     = regexp.MustCompile(`[^a-z0-9]+`)
	secretsPart    = regexp.MustCompile(`(?i)\bsecrets\b`)
	// githubPart is the github context in an expression, with the name of the field it reads, when it reads one, as the first group.
	githubPart = regexp.MustCompile(`(?i)(?:^|[^.\w])github\b(?:\s*\.\s*([a-z_][\w-]*))?`)

	errAliasLoop  = errors.New("a YAML alias refers to a node that holds it")
	errAliasBound = fmt.Errorf("with its YAML aliases expanded, it holds more than %d nodes", maxExpandedNodes)
)

// workflowStep is one step of a job of a CI workflow file.
type workflowStep struct {
	Name             string    `yaml:"name"`
	Uses             string    `yaml:"uses"`
	Run              string    `yaml:"run"`
	Shell            string    `yaml:"shell"`
	WorkingDirectory string    `yaml:"working-directory"`
	ContinueOnError  yaml.Node `yaml:"continue-on-error"`
	If               string    `yaml:"if"`
	Env              yaml.Node `yaml:"env"`
}

// runDefaults is the defaults key of a file or a job: the shell and the folder of its run steps, and the node it was read from.
type runDefaults struct {
	Run struct {
		Shell            string `yaml:"shell"`
		WorkingDirectory string `yaml:"working-directory"`
	} `yaml:"run"`
	node *yaml.Node
}

func (d *runDefaults) UnmarshalYAML(node *yaml.Node) error {
	type plain runDefaults

	d.node = node

	return node.Decode((*plain)(d))
}

// workflowJob is one job of a CI workflow file, with the id it has in the file and the node it was read from.
type workflowJob struct {
	ID          string
	Uses        string
	Permissions yaml.Node
	Defaults    runDefaults
	Env         yaml.Node
	Container   yaml.Node
	If          string
	Steps       []workflowStep
	node        *yaml.Node
}

// workflowJobs holds the jobs of a workflow file in file order, which a map would lose.
type workflowJobs []workflowJob

func (j *workflowJobs) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("jobs must be a mapping of job ids to jobs")
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		job, err := decodeJob(node.Content[i].Value, node.Content[i+1])
		if err != nil {
			return err
		}

		*j = append(*j, job)
	}

	return nil
}

// decodeJob reads one job of a workflow file by its id.
func decodeJob(id string, node *yaml.Node) (workflowJob, error) {
	var body struct {
		Uses        string         `yaml:"uses"`
		Permissions yaml.Node      `yaml:"permissions"`
		Defaults    runDefaults    `yaml:"defaults"`
		Env         yaml.Node      `yaml:"env"`
		Container   yaml.Node      `yaml:"container"`
		If          string         `yaml:"if"`
		Steps       []workflowStep `yaml:"steps"`
	}

	if err := node.Decode(&body); err != nil {
		return workflowJob{}, fmt.Errorf("job %s: %w", id, err)
	}

	return workflowJob{
		ID: id, Uses: body.Uses, Permissions: body.Permissions, Defaults: body.Defaults, Env: body.Env, Container: body.Container, If: body.If, Steps: body.Steps, node: node,
	}, nil
}

type workflowFile struct {
	Permissions yaml.Node    `yaml:"permissions"`
	Env         yaml.Node    `yaml:"env"`
	Defaults    runDefaults  `yaml:"defaults"`
	Jobs        workflowJobs `yaml:"jobs"`
}

// readWorkflow parses a workflow file once, puts the node of each YAML alias in its place, and reads the file from that tree.
// Every check then reads the same tree, so a value given through an alias or a merge key is read where it is used.
func readWorkflow(text string) (workflowFile, error) {
	var (
		root     yaml.Node
		workflow workflowFile
	)

	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		return workflow, err
	}

	if err := resolveAliases(&root, map[*yaml.Node]bool{}); err != nil {
		return workflow, err
	}

	if expandedSize(&root, map[*yaml.Node]int{}) > maxExpandedNodes {
		return workflow, errAliasBound
	}

	return workflow, root.Decode(&workflow)
}

// expandedSize counts the nodes of a tree with each alias expanded, up to one past maxExpandedNodes. sizes keeps the count of each node, so the count of a node shared by aliases is made once.
func expandedSize(node *yaml.Node, sizes map[*yaml.Node]int) int {
	if size, seen := sizes[node]; seen {
		return size
	}

	size := 1
	for _, child := range node.Content {
		size = min(size+expandedSize(child, sizes), maxExpandedNodes+1)
	}

	sizes[node] = size

	return size
}

// resolveAliases puts the node that each alias names in the place of the alias. resolved holds false for a node whose children are being resolved and true for a node that is done,
// so a node shared by two places is resolved once and an alias inside the node it names is an error.
func resolveAliases(node *yaml.Node, resolved map[*yaml.Node]bool) error {
	switch done, seen := resolved[node]; {
	case seen && done:
		return nil
	case seen:
		return errAliasLoop
	}

	resolved[node] = false

	for i := range node.Content {
		node.Content[i] = aliasTarget(node.Content[i])
		if err := resolveAliases(node.Content[i], resolved); err != nil {
			return err
		}
	}

	resolved[node] = true

	return nil
}

// aliasTarget gives the node that an alias names, or the node itself when it is no alias.
func aliasTarget(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return node.Alias
	}

	return node
}

// secretReader reports whether a node holds a name or a value that names a secret or the token of the run. It keeps the answer for each node, so a node shared by aliases is read once.
type secretReader map[*yaml.Node]bool

func (r secretReader) reads(node *yaml.Node) bool {
	if node == nil {
		return false
	}

	if got, seen := r[node]; seen {
		return got
	}

	got := node.Kind == yaml.ScalarNode && namesSecret(node.Value) || slices.ContainsFunc(node.Content, r.reads)
	r[node] = got

	return got
}

// namesSecret reports whether a text names the secrets context, or holds an expression that reads the token of the run: github.token, or the github context as a whole (an index, a filter, a function of it).
func namesSecret(text string) bool {
	if secretsPart.MatchString(text) {
		return true
	}

	for _, expression := range expressionPart.FindAllStringSubmatch(text, -1) {
		if readsToken(expression[1]) {
			return true
		}
	}

	return false
}

// readsToken reports whether the body of one expression reads the token of the run: github.token, or the github context as a whole.
func readsToken(body string) bool {
	for _, ref := range githubPart.FindAllStringSubmatch(body, -1) {
		if ref[1] == "" || strings.EqualFold(ref[1], tokenField) {
			return true
		}
	}

	return false
}

// ifReadsToken reports whether the if of a job or of one of its steps reads the token of the run. CI reads each if as an expression, with or without `${{ }}`.
func ifReadsToken(job *workflowJob) bool {
	return readsToken(job.If) || slices.ContainsFunc(job.Steps, func(step workflowStep) bool { return readsToken(step.If) })
}

// readOnly reports whether a permissions node gives the token of the run no write: read-all, or a mapping of read and none values only. A missing node takes the default of the repository, which can write.
func readOnly(node *yaml.Node) bool {
	if node.Kind == yaml.ScalarNode {
		return node.Value == readAll
	}

	if node.Kind != yaml.MappingNode {
		return false
	}

	for i := 1; i < len(node.Content); i += 2 {
		if value := node.Content[i]; value.Kind != yaml.ScalarNode || !slices.Contains([]string{"read", "none"}, value.Value) {
			return false
		}
	}

	return true
}

// Derive reads the CI workflow files and returns the gate steps: one for each run step of each job, in file order. A step or a job that the gate list cannot hold is in Refused, with the reason.
// A job stays out when it calls a reusable workflow, when its token can write, or when it, any of its steps, or the file env or defaults names a secret. A step stays out when it uses an action other than a setup or checkout action,
// runs in a shell other than bash or sh, or has run text that the shell reader finds a git write, an opaque command or an unparsed line in. Derive refuses a file that is not a workflow, and files with no run step to give.
func Derive(files []ci.File) (ci.Derived, error) {
	out, problems := ci.Derived{}, []string(nil)

	for _, file := range files {
		got, bad := deriveFile(file)
		out.Steps = append(out.Steps, got.Steps...)
		out.Refused = append(out.Refused, got.Refused...)
		problems = append(problems, bad...)
	}

	if len(problems) == 0 && len(out.Steps) == 0 {
		problems = append([]string{"the workflow files give no run step for the gate list. Name the CI workflow files that hold the gate commands"}, refusedLines(out.Refused)...)
	}

	if len(problems) > 0 {
		return ci.Derived{}, ci.NewRefusal(problems, nil)
	}

	out.Steps = uniqueNames(out.Steps)

	return out, nil
}

func refusedLines(refused []ci.Refused) []string {
	lines := make([]string, len(refused))
	for i, r := range refused {
		lines[i] = r.Name + " " + r.Reason
	}

	return lines
}

func deriveFile(file ci.File) (ci.Derived, []string) {
	workflow, err := readWorkflow(file.Text)
	if err != nil {
		return ci.Derived{}, []string{fmt.Sprintf("%s: not a workflow file: %v", file.Path, err)}
	}

	return deriveJobs(file.Path, &workflow), nil
}

// deriveJobs gives the steps of each job of a workflow file, and each job that the list leaves out whole.
func deriveJobs(path string, workflow *workflowFile) ci.Derived {
	var out ci.Derived

	secrets := secretReader{}

	for _, job := range workflow.Jobs {
		if problem := jobProblem(workflow, job, secrets); problem != "" {
			out.Refused = append(out.Refused, ci.Refused{Name: "job " + path + nameJoin + job.ID, Reason: problem})

			continue
		}

		got := deriveJob(job, jobContext(workflow, &job))
		out.Steps = append(out.Steps, got.Steps...)
		out.Refused = append(out.Refused, got.Refused...)
	}

	return out
}

// jobProblem says why a whole job cannot give gate steps, or gives an empty text. The permissions key of the job wins over the key of the file.
// A secret in any step leaves the job out, because a later step can read what an earlier step writes.
func jobProblem(file *workflowFile, job workflowJob, secrets secretReader) string {
	permissions := &file.Permissions
	if !job.Permissions.IsZero() {
		permissions = &job.Permissions
	}

	switch {
	case job.Uses != "":
		return "calls the reusable workflow " + job.Uses + ", which the gate does not read"
	case !readOnly(permissions):
		return writePermission
	case secrets.reads(job.node) || secrets.reads(&file.Env) || secrets.reads(file.Defaults.node) || ifReadsToken(&job):
		return readsSecretText
	}

	return ""
}

// stepContext is what a job and its file give each run step: the default shell and folder, the env names, and the container.
type stepContext struct {
	shell, dir, container string
	env                   []string
}

func jobContext(file *workflowFile, job *workflowJob) stepContext {
	return stepContext{
		shell:     cmp.Or(job.Defaults.Run.Shell, file.Defaults.Run.Shell),
		dir:       cmp.Or(job.Defaults.Run.WorkingDirectory, file.Defaults.Run.WorkingDirectory),
		container: nodeText(&job.Container),
		env:       append(envNames(&file.Env), envNames(&job.Env)...),
	}
}

// deriveJob gives the steps of a job.
func deriveJob(job workflowJob, context stepContext) ci.Derived {
	var out ci.Derived

	for i, step := range job.Steps {
		stepName := stepNameOf(job.ID, step, i+1)
		stepShell := cmp.Or(step.Shell, context.shell)

		if problem := stepProblem(step, stepShell); problem != "" {
			out.Refused = append(out.Refused, ci.Refused{Name: "step " + stepName, Reason: problem})
		} else if command := runCommand(step.Run); command != "" {
			out.Steps = append(out.Steps, ci.Step{Name: stepName, Command: stepRecord(command, step, stepShell, context)})
		}
	}

	return out
}

// stepRecord is the command of a derived step: the run text, then each part of the context that CI runs it in, so that a change to any of them is a change to the command.
func stepRecord(command string, step workflowStep, stepShell string, context stepContext) string {
	env := slices.Concat(context.env, envNames(&step.Env))
	slices.Sort(env)

	parts := []string{command}

	for _, part := range [][2]string{
		{"shell", stepShell},
		{"working-directory", cmp.Or(step.WorkingDirectory, context.dir)},
		{"env", strings.Join(slices.Compact(env), " ")},
		{"container", context.container},
		{"continue-on-error", nodeText(&step.ContinueOnError)},
		{"if", step.If},
	} {
		if part[1] != "" {
			parts = append(parts, "["+part[0]+" "+part[1]+"]")
		}
	}

	return strings.Join(parts, " ")
}

// envNames gives the names of an env mapping, with the names that each merge key merges, or the text of an env that is one expression.
func envNames(node *yaml.Node) []string {
	if node.Kind == yaml.ScalarNode && node.Value != "" {
		return []string{node.Value}
	}

	var names []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		names = append(names, mergedNames(node.Content[i], node.Content[i+1])...)
	}

	return names
}

// mergedNames gives the name of one env pair, or for a merge key the names of each mapping it merges.
func mergedNames(key, value *yaml.Node) []string {
	if key.ShortTag() != mergeTag {
		return []string{key.Value}
	}

	if value.Kind != yaml.SequenceNode {
		return envNames(value)
	}

	var names []string
	for _, item := range value.Content {
		names = append(names, envNames(item)...)
	}

	return names
}

// nodeText gives a node as one line: a value, `{key: value, ...}` or `[value, ...]`.
func nodeText(node *yaml.Node) string {
	if node.Kind == yaml.ScalarNode {
		return node.Value
	}

	if node.Kind == yaml.MappingNode {
		return "{" + strings.Join(pairTexts(node), ", ") + "}"
	}

	if node.Kind == yaml.SequenceNode {
		items := make([]string, len(node.Content))
		for i, item := range node.Content {
			items[i] = nodeText(item)
		}

		return "[" + strings.Join(items, ", ") + "]"
	}

	return ""
}

// pairTexts gives each pair of a mapping node as `key: value`.
func pairTexts(node *yaml.Node) []string {
	var pairs []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		pairs = append(pairs, node.Content[i].Value+": "+nodeText(node.Content[i+1]))
	}

	return pairs
}

// stepProblem says why a step cannot be a gate step, or gives an empty text. stepShell is the shell the step runs in, or an empty text for the default.
func stepProblem(step workflowStep, stepShell string) string {
	switch {
	case step.Uses != "" && !setupAction(step.Uses):
		return "uses " + step.Uses + ", an action that is neither a checkout nor a setup. Add its command to the gate list by hand"
	case stepShell != "" && !slices.Contains([]string{"bash", "sh"}, stepShell):
		return "runs in the shell " + stepShell + ", which the gate does not read"
	}

	findings := shell.ScanFor(expressionPart.ReplaceAllLiteralString(step.Run, expressionText), []model.FindingKind{model.FindingGitWrite, model.FindingOpaque, model.FindingUnparsed})
	if len(findings) > 0 {
		return "runs `" + findings[0].Text + "`, which the gate does not judge (" + string(findings[0].Kind) + ")"
	}

	return ""
}

// setupAction reports whether a uses value names a checkout action or a setup action, which the gate list needs no step for.
func setupAction(uses string) bool {
	name, _, _ := strings.Cut(strings.ToLower(uses), "@")

	return slices.Contains([]string{
		"actions/checkout", "actions/setup-go", "actions/setup-node", "actions/setup-python", "actions/setup-java", "actions/setup-dotnet",
	}, name)
}

// stepNameOf names a gate step by its job and its step name, or by its place in the job when the step has no name.
func stepNameOf(job string, step workflowStep, place int) string {
	slug := strings.Trim(nonNameRun.ReplaceAllString(strings.ToLower(step.Name), nameSeparator), nameSeparator)
	if slug == "" {
		slug = unnamedStep + strconv.Itoa(place)
	}

	return job + nameJoin + slug
}

// runCommand joins the lines of a run step to one command. A blank line and a comment line add nothing. A run of blanks outside quotes is one space; the text inside quotes is kept as CI has it.
func runCommand(run string) string {
	var lines []string

	for _, line := range strings.Split(strings.ReplaceAll(run, "\\\n", " "), "\n") {
		if line = flatOutsideQuotes(line); line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}

	return strings.Join(lines, stepJoin)
}

// flatOutsideQuotes trims a line and makes each run of spaces and tabs outside quotes one space. A backslash outside single quotes keeps the byte after it.
func flatOutsideQuotes(line string) string {
	var out strings.Builder

	scan, blank := quoteScan{}, false

	for i := range len(line) {
		switch {
		case scan.blankOutside(line[i]):
			blank = out.Len() > 0
		case blank:
			out.WriteByte(' ')

			blank = false

			fallthrough
		default:
			out.WriteByte(line[i])
		}
	}

	return out.String()
}

// quoteScan follows the quotes of a line of shell, one byte at a time.
type quoteScan struct {
	quote   byte
	escaped bool
}

// blankOutside moves the scan past one byte, and reports whether the byte is a space or a tab outside quotes and not escaped.
func (q *quoteScan) blankOutside(char byte) bool {
	switch {
	case q.escaped:
		q.escaped = false
	case char == '\\' && q.quote != '\'':
		q.escaped = true
	case q.quote != 0:
		q.close(char)
	case strings.IndexByte(`'"`, char) >= 0:
		q.quote = char
	default:
		return strings.IndexByte(" \t", char) >= 0
	}

	return false
}

// close ends the quote when the byte is the quote that opened it.
func (q *quoteScan) close(char byte) {
	if char == q.quote {
		q.quote = 0
	}
}

// uniqueNames gives a later step of the same name a number, so that the list holds no name twice.
func uniqueNames(steps []ci.Step) []ci.Step {
	seen := map[string]bool{}

	for i, step := range steps {
		name := step.Name

		for n := 2; seen[name]; n++ {
			name = step.Name + nameSeparator + strconv.Itoa(n)
		}

		seen[name] = true
		steps[i].Name = name
	}

	return steps
}
