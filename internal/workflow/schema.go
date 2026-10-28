package workflow

import "github.com/hashicorp/hcl/v2"

// Kind names a step block.
type Kind string

// The step blocks of a workflow file.
const (
	KindAgent   Kind = "agent"
	KindCommand Kind = "command"
	KindGate    Kind = "gate"
	KindLoop    Kind = "loop"
	KindWait    Kind = "wait"
)

// Step is one step block. Loop holds more steps.
type Step interface {
	Kind() Kind
	// Label is the name after the block type.
	Label() string
	// Pos is the range of the block header.
	Pos() hcl.Range
	place(name string, pos hcl.Range)
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
	When          hcl.Expression `hcl:"when,optional"`
}

// Kind returns KindAgent.
func (*Agent) Kind() Kind { return KindAgent }

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

// Loop repeats its steps up to Max times and stops early when Until holds.
type Loop struct {
	Header

	Max   int            `hcl:"max"`
	Until hcl.Expression `hcl:"until"`
	Steps []Step
}

// Kind returns KindLoop.
func (*Loop) Kind() Kind { return KindLoop }

// Wait stops the workflow until a person answers.
type Wait struct {
	Header

	Question hcl.Expression `hcl:"question,optional"`
	When     hcl.Expression `hcl:"when,optional"`
}

// Kind returns KindWait.
func (*Wait) Kind() Kind { return KindWait }

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

func walk(steps []Step, depth int, visit func(step Step, depth int)) {
	for _, step := range steps {
		visit(step, depth)

		if loop, ok := step.(*Loop); ok {
			walk(loop.Steps, depth+1, visit)
		}
	}
}
