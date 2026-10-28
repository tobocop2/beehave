package flow

import (
	"context"
	"errors"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
)

// ErrNotBuilt is returned by a step that this build does not include yet.
var ErrNotBuilt = errors.New("this step is not in this build yet: update obee")

// errEmptyLogin is returned when no agent login is set for an item.
var errEmptyLogin = errors.New("no agent login for this item: add a runner to runners.toml and set the steps to use it")

// BriefRequest asks for the brief of one agent phase. Findings are the open review findings. Answer is the last answer of the owner.
// Run is the id of the run that queued the phase. A brief path is keyed by it, so the brief of a lost run never replaces the brief of a new run of the item.
type BriefRequest struct {
	Run      string     `json:"run"`
	Item     string     `json:"item"`
	Project  string     `json:"project"`
	Step     model.Step `json:"step"`
	Round    int        `json:"round"`
	Findings string     `json:"findings"`
	Answer   string     `json:"answer"`
}

// AgentRequest asks for one agent run. Fresh starts a new agent session; otherwise the run resumes the session of the item.
// Started receives the pid of the agent process, which leads its own process group, as soon as it runs.
type AgentRequest struct {
	Item    string
	Project string
	Step    model.Step
	Round   int
	Login   string
	Fresh   bool
	Brief   string
	Started func(pid int)
}

// GateResult is the outcome of a gate that ran. Report holds the findings when Passed is false. Head is the commit the gate ran on.
// A gate that could not run returns an error instead: that ends the run and spends no fix round.
type GateResult struct {
	Passed bool   `json:"passed"`
	Report string `json:"report"`
	Head   string `json:"head"`
}

// Review is the verdict of one review and the findings behind it. Head is the commit the review read.
type Review struct {
	Verdict  Verdict `json:"verdict"`
	Findings string  `json:"findings"`
	Head     string  `json:"head"`
}

// briefs writes the brief that an agent phase reads and returns its path.
type briefs interface {
	Write(ctx context.Context, request BriefRequest) (string, error)
}

// agents runs one agent phase for a login. The context of Run ends when the run loses its claim, as well as when the phase is canceled or times out.
// Run starts the process of the agent under that context, in its own process group, and the end of the context kills the group.
// A context that is done before the start means no process starts.
type agents interface {
	Run(ctx context.Context, request AgentRequest) (agent.Result, error)
}

// gate runs the gate of an item.
type gate interface {
	Run(ctx context.Context, project, item string) (GateResult, error)
}

// verdicts reads the verdict a review left for an item.
type verdicts interface {
	Read(ctx context.Context, project, item string, round int) (Review, error)
}

// logins picks the agent login for an item of a project and returns its runner name.
// PickFresh returns the runner of the fresh agent in fix round 4, or an empty name to keep the login of the item.
type logins interface {
	Pick(ctx context.Context, project, item string) (string, error)
	PickFresh(ctx context.Context, project, item string) (string, error)
}

// lifecycle holds the steps around the work of one item. Claim runs first, Ready after an approved review and the Forge reads are clean, and Release last, on every exit.
// Claim returns the owner token of the claim. The engine renews that claim through Beats until Release runs, and Release gets the same token.
// A resumed run takes a fresh claim, so its token differs from the one of the claim step it replays.
type lifecycle interface {
	Claim(ctx context.Context, project, item string) (string, error)
	Ready(ctx context.Context, project, item string) error
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

// Deps holds the collaborators of the workflows.
type Deps struct {
	Clock     clock
	Briefs    briefs
	Agents    agents
	Gate      gate
	Verdicts  verdicts
	Logins    logins
	Lifecycle lifecycle
	Forge     forge
	Trees     trees
	Beats     beats
}
