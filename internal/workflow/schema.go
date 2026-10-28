package workflow

import (
	"maps"
	"slices"

	"github.com/hashicorp/hcl/v2"
)

// Kind names a step block.
type Kind string

// The step blocks of a workflow file.
const (
	KindAgent   Kind = "agent"
	KindCommand Kind = "command"
	KindGate    Kind = "gate"
	KindLoop    Kind = "loop"
	KindMerge   Kind = "merge"
	KindWait    Kind = "wait"
)

// stepKinds returns the kinds of a step that records outputs. A loop records only `exhausted`.
func stepKinds() []Kind {
	return []Kind{KindAgent, KindCommand, KindGate, KindMerge, KindWait}
}

// Step is one step block. Loop holds more steps.
type Step interface {
	Kind() Kind
	// Label is the name after the block type.
	Label() string
	// Pos is the range of the block header.
	Pos() hcl.Range
	// NeedsForge is true when the step calls a forge. A project with no forge cannot run it.
	NeedsForge() bool
	place(name string, pos hcl.Range)
	arguments() []argument
}

// argument is an argument of a step that holds an expression. whole is true when the argument
// names a step and no output of it.
type argument struct {
	expr  hcl.Expression
	whole bool
}

// Header is the part that every step block shares.
type Header struct {
	Name  string
	Range hcl.Range
}

// Label returns the step name.
func (h *Header) Label() string { return h.Name }

// Pos returns the range of the block header.
func (h *Header) Pos() hcl.Range { return h.Range }

func (h *Header) place(name string, pos hcl.Range) {
	h.Name, h.Range = name, pos
}

// Agent runs one agent session. Expressions are kept as written and are not evaluated here.
type Agent struct {
	Header

	Prompt        hcl.Expression `hcl:"prompt"`
	Model         hcl.Expression `hcl:"model,optional"`
	Runner        hcl.Expression `hcl:"runner,optional"`
	Skills        hcl.Expression `hcl:"skills,optional"`
	RequireSkills hcl.Expression `hcl:"require_skills,optional"`
	Timeout       hcl.Expression `hcl:"timeout,optional"`
	IdleTimeout   hcl.Expression `hcl:"idle_timeout,optional"`
	Pin           hcl.Expression `hcl:"pin,optional"`
	ResumeFrom    hcl.Expression `hcl:"resume_from,optional"`
	Inputs        hcl.Expression `hcl:"inputs,optional"`
	Outputs       hcl.Expression `hcl:"outputs,optional"`
	OutputTries   hcl.Expression `hcl:"output_tries,optional"`
	// ForbidCommands lists finding kinds. A Bash call of the run that breaks one is recorded in the output `violations`.
	ForbidCommands hcl.Expression `hcl:"forbid_commands,optional"`
	When           hcl.Expression `hcl:"when,optional"`
}

// Kind returns KindAgent.
func (*Agent) Kind() Kind { return KindAgent }

// NeedsForge is false: an agent session calls no forge.
func (*Agent) NeedsForge() bool { return false }

func (a *Agent) arguments() []argument {
	return []argument{
		{expr: a.Prompt},
		{expr: a.Model},
		{expr: a.Runner},
		{expr: a.Skills},
		{expr: a.RequireSkills},
		{expr: a.Timeout},
		{expr: a.IdleTimeout},
		{expr: a.Pin},
		{expr: a.ResumeFrom, whole: true},
		{expr: a.Inputs},
		{expr: a.Outputs},
		{expr: a.OutputTries},
		{expr: a.ForbidCommands},
		{expr: a.When},
	}
}

// Command runs a program.
type Command struct {
	Header

	Run     hcl.Expression `hcl:"run"`
	Timeout hcl.Expression `hcl:"timeout,optional"`
	Host    hcl.Expression `hcl:"host,optional"`
	When    hcl.Expression `hcl:"when,optional"`
}

// Kind returns KindCommand.
func (*Command) Kind() Kind { return KindCommand }

// NeedsForge is false: a program that a command runs does not use the forge of the project.
func (*Command) NeedsForge() bool { return false }

func (c *Command) arguments() []argument {
	return []argument{{expr: c.Run}, {expr: c.Timeout}, {expr: c.Host}, {expr: c.When}}
}

// Gate runs the check command of the project, or the command in Run.
type Gate struct {
	Header

	Run     hcl.Expression `hcl:"run,optional"`
	Host    hcl.Expression `hcl:"host,optional"`
	Timeout hcl.Expression `hcl:"timeout,optional"`
	Head    hcl.Expression `hcl:"head,optional"`
}

// Kind returns KindGate.
func (*Gate) Kind() Kind { return KindGate }

// NeedsForge is false: a gate runs the check command of the project.
func (*Gate) NeedsForge() bool { return false }

func (g *Gate) arguments() []argument {
	return []argument{{expr: g.Run}, {expr: g.Host}, {expr: g.Timeout}, {expr: g.Head}}
}

// Loop repeats its steps up to Max times and stops early when Until holds.
type Loop struct {
	Header

	Max   int            `hcl:"max"`
	Until hcl.Expression `hcl:"until"`
	Steps []Step
}

// Kind returns KindLoop.
func (*Loop) Kind() Kind { return KindLoop }

// NeedsForge is false: the steps inside a loop answer for themselves.
func (*Loop) NeedsForge() bool { return false }

func (l *Loop) arguments() []argument { return []argument{{expr: l.Until}} }

// Merge marks the pull request ready and then waits for the owner to merge it or send it back.
// Head pins the commit. When Merge is true, the step merges at that commit itself, and needs Repo, Base and Own.
// Hold is the label of a pull request that is never merged. Own lists the logins and addresses of the owner.
// ForbidText lists RE2 patterns that no line of the pull request body or of a commit message may match.
// Approved is true when a step other than the one that wrote the code accepted the head: a pull request of an own identity merges only then.
// Children is "refuse" (the default) or "retarget".
type Merge struct {
	Header

	Head       hcl.Expression `hcl:"head"`
	Merge      hcl.Expression `hcl:"merge,optional"`
	Repo       hcl.Expression `hcl:"repo,optional"`
	Base       hcl.Expression `hcl:"base,optional"`
	Hold       hcl.Expression `hcl:"hold,optional"`
	Own        hcl.Expression `hcl:"own,optional"`
	ForbidText hcl.Expression `hcl:"forbid_text,optional"`
	Approved   hcl.Expression `hcl:"approved,optional"`
	Children   hcl.Expression `hcl:"children,optional"`
	When       hcl.Expression `hcl:"when,optional"`
}

// Kind returns KindMerge.
func (*Merge) Kind() Kind { return KindMerge }

// NeedsForge is true: a merge step reads and changes a pull request.
func (*Merge) NeedsForge() bool { return true }

func (m *Merge) arguments() []argument {
	return []argument{
		{expr: m.Head},
		{expr: m.Merge},
		{expr: m.Repo},
		{expr: m.Base},
		{expr: m.Hold},
		{expr: m.Own},
		{expr: m.ForbidText},
		{expr: m.Approved},
		{expr: m.Children},
		{expr: m.When},
	}
}

// Wait stops the workflow until a person answers.
type Wait struct {
	Header

	Question hcl.Expression `hcl:"question,optional"`
	When     hcl.Expression `hcl:"when,optional"`
}

// Kind returns KindWait.
func (*Wait) Kind() Kind { return KindWait }

// NeedsForge is false: a wait step asks a person.
func (*Wait) NeedsForge() bool { return false }

func (w *Wait) arguments() []argument { return []argument{{expr: w.Question}, {expr: w.When}} }

// Workflow is one workflow file. Steps are in file order, and a loop holds its own steps.
type Workflow struct {
	File          string
	Name          string
	MethodVersion string
	RequireModel  bool
	Steps         []Step
}

// header is the decode target of the workflow block.
type header struct {
	MethodVersion string `hcl:"method_version,optional"`
	RequireModel  bool   `hcl:"require_model,optional"`
}

// Walk calls visit for each step in file order. A loop is visited before the steps in its body.
func (w *Workflow) Walk(visit func(step Step, depth int)) {
	walk(w.Steps, 0, visit)
}

// EnvNames returns the sorted names that the expressions of the file read from `env`.
func (w *Workflow) EnvNames() []string {
	seen := map[string]bool{}

	w.Walk(func(step Step, _ int) {
		for _, arg := range step.arguments() {
			for _, tr := range arg.expr.Variables() {
				if name, named := attribute(tr, 1); named && tr.RootName() == rootEnv {
					seen[name] = true
				}
			}
		}
	})

	return slices.Sorted(maps.Keys(seen))
}

func walk(steps []Step, depth int, visit func(step Step, depth int)) {
	for _, step := range steps {
		visit(step, depth)

		if loop, ok := step.(*Loop); ok {
			walk(loop.Steps, depth+1, visit)
		}
	}
}
