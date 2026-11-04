package flow

import (
	"context"
	"errors"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
)

// errEmptyLogin is returned when no agent login is set for an agent step.
var errEmptyLogin = errors.New("no agent login for this step: add a runner to runners.star and name it in the step with `runner`")

// AgentRequest asks for one agent run. Prompt is the rendered prompt of the agent step.
// Started receives the pid of the agent process, which leads its own process group, as soon as it runs.
// Resume is the session to continue, empty for a fresh session. Model, Timeout and IdleTimeout are zero when the step sets none.
// Forbid lists the kinds of command that the step forbids. The result holds one line in Violations for each Bash call of the run that breaks one.
// Protect lists the directories of the kind protected-path; a relative one starts at the folder of the step.
type AgentRequest struct {
	Item        string
	Project     string
	Login       string
	Prompt      string
	Resume      string
	Model       string
	Timeout     time.Duration
	IdleTimeout time.Duration
	Forbid      []model.FindingKind
	Protect     []string
	// MaxBudget is the most dollars that the run may spend, passed to the budget option of the agent CLI. 0 means the step sets no cap.
	MaxBudget float64
	// NoEdits starts the agent so that it cannot edit files. Shell commands stay allowed.
	NoEdits bool
	// Sandbox is the sandbox the shell of the step runs in, or nil for none.
	Sandbox *model.Sandbox
	// OutputFile is the file where the agent writes its declared outputs as a JSON object. Outputs names them. Outputs is empty for a call that declares none.
	OutputFile string
	Outputs    []string
	Started    func(pid int)
}

// GateResult is the outcome of a gate that ran. Report holds the findings when Passed is false. Head is the commit the gate ran on.
// Tree is the tree id of that commit, or empty when the gate did not read it. The gate step records it as the output `tree`, and a merge call can pass it on.
// A gate that could not run returns an error instead: that ends the run and spends no fix round.
// Steps holds one result for each step of the gate: pass, fail or unknown. The gate step records it as the output `steps`, and a merge call can check it.
type GateResult struct {
	Passed bool       `json:"passed"`
	Report string     `json:"report"`
	Head   string     `json:"head"`
	Tree   string     `json:"tree"`
	Steps  []GateStep `json:"steps"`
}

// GateStep is the result of one step of a gate. Status is "pass", "fail" or "unknown".
type GateStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// agents runs one agent phase for a login. The context of Run ends when the run loses its claim, as well as when the phase is canceled or times out.
// Run starts the process of the agent under that context, in its own process group, and the end of the context kills the group.
// A context that is done before the start means no process starts.
type agents interface {
	Run(ctx context.Context, request AgentRequest) (agent.Result, error)
}

// GateKey names the gate that Run would start for an item: ID is the same for the same tree, gate command, test report, build host and pins.
// Head and Tree are the commit and tree id of the item. An empty ID means that no earlier pass may stand in for the gate.
type GateKey struct {
	ID   string
	Head string
	Tree string
}

// GateJob is the host job that a gate started: its id and the head that it judges. A gate with no host job has an empty GateJob.
// The engine records it as a step of its own, so a run that starts again waits on the same job and starts none.
type GateJob struct {
	ID   string `json:"id,omitempty"`
	Head string `json:"head,omitempty"`
}

// gate runs the gate of an item. Key reads the key of that gate and starts nothing.
// Submit starts the host job and returns at once. Run then waits for that job and judges the result; given an empty GateJob it starts the job itself.
// host is the build host that the run recorded in host_up. An empty host is the default build host of the project.
type gate interface {
	Submit(ctx context.Context, project, item, host string) (GateJob, error)
	Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error)
	Key(ctx context.Context, project, item, host string) (GateKey, error)
}

// logins picks the agent login for an item of a project and returns its runner name.
// A pick with overCap set takes the runner with the lowest use even when every runner is over a cap.
// When no runner has room, the error is one with a ResetAt method, and the engine waits for that time.
type logins interface {
	Pick(ctx context.Context, project, item string, busy []string, overCap bool) (string, error)
}

// resetter is the error of a pick that found no runner under the usage caps. ResetAt returns the earliest time a runner gets room,
// and false when none is known.
type resetter interface {
	ResetAt() (time.Time, bool)
}

// lifecycle holds the claim of one item. Claim runs first and Release last, on every exit.
// Claim returns the owner token of the claim. The engine renews that claim through Beats until Release runs, and Release gets the same token.
// A resumed run takes a fresh claim, so its token differs from the one of the claim step it replays.
type lifecycle interface {
	Claim(ctx context.Context, project, item string) (string, error)
	Release(ctx context.Context, project, item, owner string) error
}

// orchestrators lists the other orchestrators that run in the claim store of a project: one line for each, naming the owner and its items.
// A store that cannot be read is an error and gives no list, so a failed read is never read as "nobody else runs".
type orchestrators interface {
	Others(ctx context.Context, project, owner string) ([]string, error)
}

// beats renews the claims of one owner in the tracker of a project.
// A non-empty text means the tracker says the claim is gone. An error means the renewal could not run, and the claim can still be live.
// The signature matches claim.Claims.Beat, plus the project.
// Renewed returns when the lease of the claim of the owner on the item was last renewed, or the zero time when the owner holds no claim on the item or the claim has no lease.
type beats interface {
	Beat(ctx context.Context, project, owner string) (string, error)
	Renewed(ctx context.Context, project, owner, item string) (time.Time, error)
}

// clock sets the intervals the engine waits on. The zero value uses the real ones.
// Tick replaces the ticker of the status tick: each value received runs one tick.
// Poll replaces the interval at which the agents queue looks for work, at which a run looks for the result of a phase,
// at which a phase looks again for the first trust of its run, and at which a run tries again the first renewal of a claim it did not take.
// Release replaces the time that the git calls of one worktree release share.
// Beat replaces the time that one claim renewal may take. OwnerWait replaces the time of one wait for the owner before the wait starts again.
// Retain replaces the age at which Launch deletes a finished run. Now replaces the monotonic reading of the clock that measures the time since the last good claim renewal.
// Wall replaces the wall reading of that clock: when it is nil, the wall reading is the one that Now returns.
// Reading replaces the time that the now builtin returns, and the time from which a wait counts its deadline and its reminders. The lease age keeps using Wall.
// After replaces the timer that ends the trust in a claim: it starts f once d has passed, and the function it returns stops the timer.
// Grace replaces the time that a phase waits for the first trust of a run that is not open.
// Watch replaces the time of the durable sleep between two reads of a ready pull request.
// Settle replaces the time of the durable sleep between two reads of a batch member whose mergeable state is UNKNOWN.
// Queued replaces the time of the durable sleep between two reads of a pull request whose merge the forge holds.
// CapWait replaces the least time of the durable sleep that waits for a usage cap to reset.
type clock struct {
	CapWait   time.Duration
	Tick      <-chan time.Time
	Poll      time.Duration
	Release   time.Duration
	Beat      time.Duration
	OwnerWait time.Duration
	Retain    time.Duration
	Now       func() time.Time
	Wall      func() time.Time
	Reading   func() time.Time
	After     func(d time.Duration, f func()) (stop func())
	Grace     time.Duration
	Watch     time.Duration
	Settle    time.Duration
	Queued    time.Duration
}

// now reads the clock of the process. The reading carries the monotonic clock, so a difference of two readings counts the time that the machine ran.
func (c clock) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}

	return time.Now()
}

// wall reads the wall clock, which keeps counting while the machine sleeps.
func (c clock) wall() time.Time {
	if c.Wall != nil {
		return c.Wall()
	}

	return c.now().Round(0)
}

// reading is the time that the now builtin returns. It is the wall clock, unless Reading replaces it; the lease age never uses Reading.
func (c clock) reading() time.Time {
	if c.Reading != nil {
		return c.Reading()
	}

	return c.wall()
}

// age returns how long ago since began, a reading of now. It counts the larger of the monotonic and the wall time, because the monotonic clock stops
// while the machine sleeps and the lease in the tracker does not. The larger count can only make the engine stop trusting the claim early.
func (c clock) age(since time.Time) time.Duration {
	return max(c.now().Sub(since), c.wall().Sub(since.Round(0)))
}

// after starts f once d has passed and returns the function that stops the timer.
func (c clock) after(d time.Duration, f func()) func() {
	if c.After != nil {
		return c.After(d, f)
	}

	timer := time.AfterFunc(d, f)

	return func() { timer.Stop() }
}

// resultOptions makes a result wait poll at the interval of the clock.
func (c clock) resultOptions() []dbos.GetResultOption {
	if c.Poll <= 0 {
		return nil
	}

	return []dbos.GetResultOption{dbos.WithHandlePollingInterval(c.Poll)}
}

// CommandRequest is one program that a command call or a gate call runs. Host is empty for the item folder.
type CommandRequest struct {
	Project string
	Item    string
	Run     []string
	Host    string
	// BuildHost is the build host that the run recorded in host_up. The gate and a call with Host run there. Empty means the default of the project.
	BuildHost string
	Timeout   time.Duration
	// Gate is true for the program of a gate (a gate call with run, or the gate_run of batch_merge). A gate on this machine runs under the sandbox of its project.
	Gate bool
	// MaxNewProcesses is the number of processes above the count of the user at the start. It is zero for a call that sets no cap.
	MaxNewProcesses int
	// Stdin is the text that the program reads. It is empty when the call sets none.
	Stdin string
	// Env is the whole environment of the program as NAME=value pairs. It is nil for a request that keeps the environment of the process.
	Env []string
}

// CommandOutput is what a program printed and its exit code. A non-zero RC is data, not an error.
// Stderr is empty for a program on a host, which keeps no output, and for a dry run.
type CommandOutput struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	RC     int    `json:"rc"`
}

// commands runs the programs of command calls.
type commands interface {
	Run(ctx context.Context, request CommandRequest) (CommandOutput, error)
}

// items reads the fields of an item, which are the `item` argument of main in a workflow file.
type items interface {
	Fields(ctx context.Context, project, item string) (map[string]string, error)
}

// absentError is the error of a step that needs a collaborator the build does not set. step says what the step does.
// A dry run leaves such a collaborator out on purpose, so it reports the step as not run instead of as a failure.
type absentError struct{ message, step string }

func (e *absentError) Error() string { return e.message }

// Deps holds the collaborators of the workflows. A nil Merge means the build cannot merge: a merge step with `merge = true` fails.
type Deps struct {
	Clock     clock
	Agents    agents
	Gate      gate
	Logins    logins
	Lifecycle lifecycle
	Forge     forge
	Watch     watcher
	Trees     treeMaker
	Beats     beats
	Merge     merger
	// Preflight serves preflight. A nil Preflight means the build cannot run it: a preflight call fails.
	Preflight preflighter
	Commands  commands
	Items     items
	Writer    writer
	// Batch serves batch_merge. A nil Batch means the build cannot run it.
	Batch batchHost
	// Queue serves queue and the hold check at the start of a run that spawn started. A nil Queue means the build cannot read the open work: a queue call fails.
	Queue queueReader
	// Others lists the other orchestrators at the start of a run. A nil Others means the build cannot list them: the run lists none.
	Others orchestrators
	// Poll serves the scheduled review poll. A nil Poll means the build cannot read the forges: the engine sets no poll schedule.
	Poll pollReader
	// Hosts serves host_up and host_teardown. A nil Hosts means the build cannot start or stop a host: both calls fail.
	Hosts hostKeeper
	// SandboxDefault says whether a project runs the agent steps that set no sandbox in the sandbox. A nil SandboxDefault means no project does.
	SandboxDefault func(project string) bool
	// RefuseGate returns the error of a gate that must not run on this machine, or nil. program is true for a gate that names its own program.
	// A nil RefuseGate means every gate runs. The engine asks inside a recorded step that comes before the gate key, an earlier pass, any git work or any program
	// (for batch_merge, the first step of the call), so a replay of a recorded gate returns the record and is never refused.
	RefuseGate func(project string, program bool) error
	// Repository says whether a project has a git repository. A nil Repository means every project has one.
	// The item folder of a project with no repository is a plain folder: no step reads git there, and the git builtins refuse the project.
	Repository func(project string) bool
	// Facts returns the project as the config holds it. The `facts` builtin reads it in a recorded step. A nil Facts means every project has no facts.
	Facts func(project string) model.Project
	// Kept lists errors, besides the errors of the gate and the host, that a gate or a merge keeps for errors.Is when it fails. A call that runs in
	// a child run returns its error as text, and the parent finds each listed error again from that text.
	Kept []error
}
