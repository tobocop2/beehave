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
	// NoEdits starts the agent so that it cannot edit files. Shell commands stay allowed.
	NoEdits bool
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

// gate runs the gate of an item. Key reads the key of that gate and starts nothing.
type gate interface {
	Run(ctx context.Context, project, item string) (GateResult, error)
	Key(ctx context.Context, project, item string) (GateKey, error)
}

// logins picks the agent login for an item of a project and returns its runner name.
type logins interface {
	Pick(ctx context.Context, project, item string, busy []string) (string, error)
}

// lifecycle holds the claim of one item. Claim runs first and Release last, on every exit.
// Claim returns the owner token of the claim. The engine renews that claim through Beats until Release runs, and Release gets the same token.
// A resumed run takes a fresh claim, so its token differs from the one of the claim step it replays.
type lifecycle interface {
	Claim(ctx context.Context, project, item string) (string, error)
	Release(ctx context.Context, project, item, owner string) error
}

// beats renews the claims of one owner in the tracker of a project.
// A non-empty text means the tracker says the claim is gone. An error means the renewal could not run, and the claim can still be live.
// The signature matches claim.Claims.Beat, plus the project.
type beats interface {
	Beat(ctx context.Context, project, owner string) (string, error)
}

// clock sets the intervals the engine waits on. The zero value uses the real ones.
// Tick replaces the ticker of the status tick: each value received runs one tick.
// Poll replaces the interval at which the agents queue looks for work, at which a run looks for the result of a phase,
// at which a phase looks again for the first trust of its run, and at which a run tries again the first renewal of a claim it did not take.
// Release replaces the time that the git calls of one worktree release share.
// Beat replaces the time that one claim renewal may take. OwnerWait replaces the time of one wait for the owner before the wait starts again.
// Retain replaces the age at which Launch deletes a finished run. Now replaces the monotonic reading of the clock that measures the time since the last good claim renewal.
// Wall replaces the wall reading of that clock: when it is nil, the wall reading is the one that Now returns.
// After replaces the timer that ends the trust in a claim: it starts f once d has passed, and the function it returns stops the timer.
// Grace replaces the time that a phase waits for the first trust of a run that is not open.
// Watch replaces the time of the durable sleep between two reads of a ready pull request.
// Settle replaces the time of the durable sleep between two reads of a batch member whose mergeable state is UNKNOWN.
type clock struct {
	Tick      <-chan time.Time
	Poll      time.Duration
	Release   time.Duration
	Beat      time.Duration
	OwnerWait time.Duration
	Retain    time.Duration
	Now       func() time.Time
	Wall      func() time.Time
	After     func(d time.Duration, f func()) (stop func())
	Grace     time.Duration
	Watch     time.Duration
	Settle    time.Duration
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
	Timeout time.Duration
	// MaxNewProcesses is the number of processes above the count of the user at the start. It is zero for a call that sets no cap.
	MaxNewProcesses int
	// Stdin is the text that the program reads. It is empty when the call sets none.
	Stdin string
	// Env is the whole environment of the program as NAME=value pairs. It is nil for a request that keeps the environment of the process.
	Env []string
}

// CommandOutput is what a program printed and its exit code. A non-zero RC is data, not an error.
type CommandOutput struct {
	Stdout string
	RC     int
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
	// Poll serves the scheduled review poll. A nil Poll means the build cannot read the forges: the engine sets no poll schedule.
	Poll pollReader
	// Hosts serves host_teardown. A nil Hosts means the build cannot stop a host: a host_teardown call fails.
	Hosts hostStopper
	// Kept lists errors, besides the errors of the gate and the host, that a gate or a merge keeps for errors.Is when it fails. A call that runs in
	// a child run returns its error as text, and the parent finds each listed error again from that text.
	Kept []error
}
