package flow

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/review"
	"github.com/tobocop2/beehave/internal/step"
)

const (
	testVersion = "v1"
	testLogin   = "alice"
	testItem    = "12"
	testPull    = 7
	testProject = "p"
	testOwner   = "owner-"
	// eventTimeout bounds a wait on a condition that is sure to come, such as the last of fourteen durable agent steps. A passing test never waits
	// the full time. The old bound of 30 s was below the real time of such a run under the race detector in a full gate with four gates on the
	// host: the same runs took 34 s to 52 s there, so the bound failed correct runs and told nothing about a hang.
	eventTimeout = 3 * time.Minute
	pollEvery    = 5 * time.Millisecond
	// runCheckEvery is the interval at which a wait reads the status and the steps of the run under test.
	runCheckEvery = 100 * time.Millisecond
	// storePoll is the interval at which the two queues of an engine and a result wait read the store. Each poll parses a statement, which
	// costs most of the CPU of the package under the race detector, and the engines of the parallel tests all poll at once. It is the
	// library default: a shorter interval made the package slower and did not make any test wait less than the tests need.
	storePoll = time.Second
)

var errFake = errors.New("fake failure")

// fakeAgents records each request and counts the agents in flight, in all and for each login.
type fakeAgents struct {
	mu       sync.Mutex
	requests []AgentRequest
	inFlight int
	maxTotal int
	perLogin map[string]int
	maxLogin int
	gate     chan struct{}
	pid      int
	result   func(AgentRequest) agent.Result
	err      error
	// named is the session id that the agent names before it counts as in flight, when it is set.
	named string
	// said is what the agent says at each start, before it counts as running.
	said []agent.Said
	// hangAt is the number (from 1) of the one agent start that holds until its context ends, so a test stops the engine while that agent runs. 0 holds none.
	hangAt int
	// stall is the item whose first stallStarts agent starts each hold for stallFor, or until the context ends, so a test sees that run stalled.
	// A canceled phase of a fake has no process for the tick to end, so the hold ends on its own and gives the slot of the login back.
	stall       string
	stallStarts int
	stallFor    time.Duration
}

// sayAll gives each entry of said to the request, as the agent says it.
func sayAll(request AgentRequest, said []agent.Said) {
	for _, one := range said {
		request.Said(one)
	}
}

// holdOf is what the start of an agent for item waits on, besides the end of its context. The start at hangAt waits on a nil channel, so only the
// context ends it. The first stallStarts starts of the stalled item wait for stallFor. Any other start goes on at once. The caller holds mu.
func (a *fakeAgents) holdOf(item string) <-chan time.Time {
	switch {
	case len(a.requests) == a.hangAt:
		return nil
	case item == a.stall && a.stallStarts > 0:
		a.stallStarts--

		return time.After(a.stallFor)
	default:
		return time.After(0)
	}
}

// Run holds each agent in flight until the test closes gate, when gate is set. It reports pid as the process of the agent, when pid is set.
// It names the session named before it counts as in flight, so a test that sees the agent in flight sees the saved session.
// Like the command of the real runner, it starts nothing under a context that is done, and it kills the group of its process when the context ends.
func (a *fakeAgents) Run(ctx context.Context, request AgentRequest) (agent.Result, error) {
	if err := ctx.Err(); err != nil {
		return agent.Result{Outcome: agent.OutcomeNotStarted, Note: err.Error()}, err
	}

	if a.pid != 0 {
		request.Started(a.pid)

		// The kill is never stopped: a stop that wins the race against the start of the function would leave the group alive, as no release kills it now.
		// It stands in for the end of the agent: the real runner kills the group when the context ends (the cancel function of the command) and again
		// after every wait, so a phase end that leaves no group is proved here through the context only.
		context.AfterFunc(ctx, func() { _ = proc.KillGroupID(a.pid) }) //nolint:errcheck // ESRCH means the group has no member left
	}

	a.mu.Lock()
	if a.named != "" {
		request.Named(a.named)
	}

	a.requests = append(a.requests, request)
	a.inFlight++
	a.perLogin[request.Login]++
	a.maxTotal = max(a.maxTotal, a.inFlight)
	a.maxLogin = max(a.maxLogin, a.perLogin[request.Login])
	hold := a.holdOf(request.Item)
	said := a.said
	a.mu.Unlock()

	sayAll(request, said)

	select {
	case <-hold:
	case <-ctx.Done():
	}

	if a.gate != nil {
		select {
		case <-a.gate:
		case <-ctx.Done():
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.inFlight--
	a.perLogin[request.Login]--

	if a.result != nil {
		return a.result(request), a.err
	}

	return agent.Result{Outcome: agent.OutcomeExited}, a.err
}

// running counts the agents in flight now.
func (a *fakeAgents) running() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.inFlight
}

func (a *fakeAgents) seen() []AgentRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]AgentRequest(nil), a.requests...)
}

// logins returns the login of each request, in order.
func (a *fakeAgents) logins() []string {
	var logins []string

	for _, request := range a.seen() {
		logins = append(logins, request.Login)
	}

	return logins
}

// fakeGate answers each call from results in order; the last result repeats.
type fakeGate struct {
	mu      sync.Mutex
	results []GateResult
	calls   int
	err     error
	via     gate
	// submits counts the calls of Submit. jobs are the host jobs that Run was given.
	submits atomic.Int32
	jobs    []GateJob
	// keys are the answers of Key in order; keyReads counts them.
	keys     []GateKey
	keyReads int
	keyErr   error
	// hosts are the build hosts that the calls of Key, Submit and Run were given, in order.
	hosts []string
	// bases are the base branches that Submit was given, in order.
	bases []string
	// keyBases are the base branches that Key was given, in order.
	keyBases []string
	// pinBases and keyPinBases are the pin bases that Submit and Key were given, in order.
	pinBases    []string
	keyPinBases []string
	// checks is the check record that Run gives a passed result with none, or nil for no record.
	checks *GateChecks
}

// gatedOn lists the build hosts that the calls of the gate were given, in order.
func (g *fakeGate) gatedOn() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.hosts)
}

// Key answers from keys in order, the last repeating. With no keys it gives an empty key, so no gate reuses a pass.
func (g *fakeGate) Key(_ context.Context, _, _, host, base, pinBase string) (GateKey, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.hosts = append(g.hosts, host)
	g.keyBases = append(g.keyBases, base)
	g.keyPinBases = append(g.keyPinBases, pinBase)

	if len(g.keys) == 0 {
		return GateKey{}, g.keyErr
	}

	index := min(g.keyReads, len(g.keys)-1)
	g.keyReads++

	return g.keys[index], g.keyErr
}

// The gates below run a tree of their own and give no key, so the engine never reuses a pass for them.

func (*treeGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (packageGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (hostDiedGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (zeroTestGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (deadlineGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (slowGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

// Submit counts the host jobs that it started and gives each one an id.
func (g *fakeGate) Submit(_ context.Context, _, _, host, base, pinBase string) (GateJob, error) {
	g.mu.Lock()
	g.hosts = append(g.hosts, host)
	g.bases = append(g.bases, base)
	g.pinBases = append(g.pinBases, pinBase)
	g.mu.Unlock()

	return GateJob{ID: fmt.Sprintf("job-%d", g.submits.Add(1)), Head: gatedHead}, nil
}

// waitedOn lists the host jobs that Run was given, in order.
func (g *fakeGate) waitedOn() []GateJob {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.jobs)
}

func (g *fakeGate) Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.jobs = append(g.jobs, job)
	g.hosts = append(g.hosts, host)

	if g.via != nil {
		g.calls++

		result, err := g.via.Run(ctx, project, item, host, job)

		return g.withChecks(result), err
	}

	index := min(g.calls, len(g.results)-1)
	g.calls++

	result := g.results[index]
	if result.Steps == nil {
		result.Steps = commandStep(result.Passed)
	}

	return g.withChecks(result), g.err
}

// withChecks gives a passed result with no check record the record of the gate.
func (g *fakeGate) withChecks(result GateResult) GateResult {
	if g.checks != nil && result.Passed && result.Checks == nil {
		checks := *g.checks
		result.Checks = &checks
	}

	return result
}

// commandStep is the one step that a real gate gives from the exit code of its command.
func commandStep(passed bool) []GateStep {
	if passed {
		return []GateStep{{Name: "command", Status: "pass"}}
	}

	return []GateStep{{Name: "command", Status: "fail"}}
}

// fakeLogins picks byItem[item] when set and login otherwise.
type fakeLogins struct {
	login  string
	byItem map[string]string
	err    error
	// reset is the reset time that the usage read gives for a login. The zero time is a read that has none.
	reset    time.Time
	resetErr error
	asked    *[]string
}

func (l fakeLogins) Reset(_ context.Context, _, login string) (time.Time, bool, error) {
	if l.asked != nil {
		*l.asked = append(*l.asked, login)
	}

	return l.reset, !l.reset.IsZero(), l.resetErr
}

func (l fakeLogins) Pick(_ context.Context, _, item string, _ []string, _ bool) (string, error) {
	if login, ok := l.byItem[item]; ok {
		return login, l.err
	}

	return l.login, l.err
}

// noReset is the usage read of a fake pool that knows no reset time.
type noReset struct{}

func (noReset) Reset(context.Context, string, string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

// loginPick is one answer of loginSequence.
type loginPick struct {
	login string
	err   error
}

// loginSequence answers each Pick from picks in order. The last answer repeats. It counts the calls.
type loginSequence struct {
	noReset

	mu    sync.Mutex
	calls int
	picks []loginPick
}

func (l *loginSequence) Pick(context.Context, string, string, []string, bool) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	pick := l.picks[min(l.calls, len(l.picks)-1)]
	l.calls++

	return pick.login, pick.err
}

func (l *loginSequence) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.calls
}

// fakeLifecycle records each step it runs and fails the steps named in failing.
type fakeLifecycle struct {
	mu       sync.Mutex
	steps    []string
	projects []string
	failing  map[string]bool
	takes    map[string]int
	released []string
	// failErr replaces errFake as the error of a failing step.
	failErr error
	// onClaim, when set, runs at the start of each Claim: a test uses it to move the clock during the take.
	onClaim func()
	// hang makes Release wait for its context to end, and expired counts the releases that the context ended.
	hang    bool
	expired int
}

func (l *fakeLifecycle) step(name, project, item string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.steps = append(l.steps, name+":"+item)
	l.projects = append(l.projects, project)

	if l.failing[name] {
		return cmp.Or(l.failErr, errFake)
	}

	return nil
}

// hangs reports whether Release waits for its context.
func (l *fakeLifecycle) hangs() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.hang
}

// expire counts a release that its context ended.
func (l *fakeLifecycle) expire() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.expired++
}

// expiries counts the releases that their context ended.
func (l *fakeLifecycle) expiries() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.expired
}

// Claim returns testOwner+item for the first take of an item and a numbered token for each later take.
func (l *fakeLifecycle) Claim(_ context.Context, project, item string) (string, error) {
	if l.onClaim != nil {
		l.onClaim()
	}

	err := l.step("claim", project, item)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.takes == nil {
		l.takes = map[string]int{}
	}

	l.takes[item]++

	if l.takes[item] == 1 {
		return testOwner + item, err
	}

	return fmt.Sprintf("%s%s#%d", testOwner, item, l.takes[item]), err
}

func (l *fakeLifecycle) Release(ctx context.Context, project, item, owner string) error {
	l.mu.Lock()
	l.released = append(l.released, owner)
	l.mu.Unlock()

	if l.hangs() {
		hang(ctx.Done())
		l.expire()

		return ctx.Err()
	}

	return l.step("release", project, item)
}

// releasedBy lists the owner token of each release, in order.
func (l *fakeLifecycle) releasedBy() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.released...)
}

// projectsSeen lists the project of each lifecycle call, in order.
func (l *fakeLifecycle) projectsSeen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.projects...)
}

// fakeBeats is the fake tracker: it records each renewal and fails the owner named in failFor, with failure text or an error.
// onBeat, when set, runs in each renewal before it answers: a test uses it to move the clock during the call.
type fakeBeats struct {
	mu      sync.Mutex
	calls   []string
	failFor string
	text    string
	err     error
	onBeat  func()
	renewed time.Time

	renewedErr error
}

func (b *fakeBeats) Beat(_ context.Context, project, owner string) (string, error) {
	b.mu.Lock()
	b.calls = append(b.calls, project+"/"+owner)
	b.renewed = time.Now() // a renewal moves the lease to now, as the claim store does
	hook, failing, text, err := b.onBeat, owner == b.failFor, b.text, b.err
	b.mu.Unlock()

	if hook != nil {
		hook()
	}

	if failing {
		return text, err
	}

	return "", nil
}

// Renewed answers with the time that setRenewed set, or the time of the last Beat when one came after: the last renewal of the claim lease of any item.
func (b *fakeBeats) Renewed(context.Context, string, string, string) (time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.renewed, b.renewedErr
}

// failRenewed makes every later read of the last renewal fail with err.
func (b *fakeBeats) failRenewed(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.renewedErr = err
}

// setRenewed sets the last renewal of the claim lease that the store reports. The zero time is an item with no claim.
func (b *fakeBeats) setRenewed(at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.renewed = at
}

// hook sets the function that runs in each renewal.
func (b *fakeBeats) hook(f func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.onBeat = f
}

// fail makes every later renewal for owner fail with text or err.
func (b *fakeBeats) fail(owner, text string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.failFor, b.text, b.err = owner, text, err
}

func (b *fakeBeats) seen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]string(nil), b.calls...)
}

func (l *fakeLifecycle) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.steps...)
}

// harness holds the fakes and the options of one test. Each engine of the test shares one state folder.
type harness struct {
	agents    *fakeAgents
	gate      *fakeGate
	logins    fakeLogins
	picker    logins // replaces logins when set
	life      *fakeLifecycle
	merge     *fakeMerge
	batch     batchHost   // nil unless a test sets it
	preflight preflighter // nil unless a test sets it
	forge     *fakeForge
	trees     *fakeTrees
	beats     *fakeBeats
	commands  *fakeCommands
	items     *fakeItems
	writer    writer      // nil unless a test sets it
	reader    reader      // nil unless a test sets it
	hosts     hostKeeper  // nil unless a test sets it
	queue     queueReader // nil unless a test sets it
	poll      pollReader  // nil unless a test sets it
	others    *fakeOthers // nil unless a test sets it
	// sandboxDefault is the project default for a step that sets no sandbox. It is nil, and so off, unless a test sets it.
	sandboxDefault func(project string) bool
	// repository says whether a project has a git repository. It is nil, and so every project has one, unless a test sets it.
	repository func(project string) bool
	// facts is the project of a run as the config holds it. It is nil, and so every project has no facts, unless a test sets it.
	facts func(project string) model.Project
	// bare stops the harness from giving the facts a source when the test set none, so the project has no repository in its facts.
	bare bool
	// mutation is the command that the facts of the run hold as vars.mutation_command. It is nil, and so the vars hold none, unless a test sets it.
	mutation []string
	// skillLine is the skill-load line of the adapter of each login. It is nil, and so every login has the line "/fake-skills:<skill>", unless a test sets it.
	skillLine func(login, skill string) string
	// noSkillLine leaves Deps.SkillLine nil, as an engine built with no adapter table has it.
	noSkillLine bool
	// refuseGate is the refusal of a gate on this machine. It is nil, and so no gate is refused, unless a test sets it.
	refuseGate func(project string, program bool) error
	// slots resolves the slot class of a command call. It is nil, and so the build has no slot classes, unless a test sets it.
	slots func(project, cluster, class string) (SlotGrant, error)
	ticks chan time.Time
	sleep time.Duration
	// draft is the limit of each draft of a hand-off that ends. A test sets it to shorten the limit.
	draft time.Duration
	// wall replaces the wall clock of the engine when a test sets it.
	wall func() time.Time
	// reading replaces the reading of the clock that the now builtin and the wait for a lost job read, when a test sets it.
	reading func() time.Time
	capWait time.Duration
	// overload replaces the first wait of the backoff after an overload when a test sets it.
	overload time.Duration
	// lostPoll is the sleep between two looks for the proof of a lost gate job. It is a millisecond unless a test sets it.
	lostPoll time.Duration
	opts     Options
}

// defaultFlow is the workflow file of a harness that a test does not replace: one agent step.
const defaultFlow = "def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\")\n"

// defaultFlowName is the name of the default workflow file, which sits beside the state folder of the harness.
const defaultFlowName = "workflow.star"

// waitFlow is a workflow file with one agent step, then a question for the owner. A run of it waits for beehave answer after its agent ends.
const waitFlow = "def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\")\n    wait(name = \"ask\", question = \"What now?\")\n"

// flowIs replaces the default workflow file of the harness. Call it before the first run.
func (h *harness) flowIs(t *testing.T, text string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(h.opts.StateDir), defaultFlowName), []byte(text), 0o600))
}

// Run walks the default workflow file of the harness. It is a helper of the tests: the production engine runs only a file named by the caller.
func (e *Engine) Run(item, project string) (ItemResult, error) {
	return e.RunFile(item, project, filepath.Join(filepath.Dir(e.dir), defaultFlowName))
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	tree, _ := gitTree(t)
	folder, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err, "the temp folder of the OS can be a link, and the engine holds the real path of its state folder")
	require.NoError(t, os.MkdirAll(filepath.Join(folder, "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(folder, defaultFlowName), []byte(defaultFlow), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "prompts", "work.md"), []byte("Work on the item.\n"), 0o600))

	life := &fakeLifecycle{failing: map[string]bool{}}

	h := &harness{
		agents:   &fakeAgents{perLogin: map[string]int{}},
		gate:     &fakeGate{results: []GateResult{{Passed: true}}},
		logins:   fakeLogins{login: testLogin},
		life:     life,
		forge:    &fakeForge{life: life, state: review.Standing{Number: testPull}},
		trees:    &fakeTrees{dir: tree},
		beats:    &fakeBeats{},
		commands: &fakeCommands{},
		items:    &fakeItems{fields: map[string]string{"id": testItem, "title": "a title", "body": "a body"}},
		ticks:    make(chan time.Time),
		opts: Options{
			StateDir:      filepath.Join(folder, "state"),
			Version:       testVersion,
			MaxConcurrent: 2, MaxReviewConcurrent: 2,
			Env:     map[string]string{},
			Modules: []step.Module{StepModule()},
		},
	}
	h.migrate(t)

	return h
}

func (h *harness) deps() Deps {
	deps := Deps{
		Agents: h.agents, Gate: h.gate, Logins: h.logins, Lifecycle: h.life, Forge: h.forge, Trees: h.trees, Beats: h.beats, Commands: h.commands, Items: h.items, Writer: h.writer, Reader: h.reader,
		Kept:  []error{errFake},
		Clock: clock{Tick: h.ticks, Poll: storePoll, Settle: cmp.Or(h.sleep, time.Millisecond), Queued: cmp.Or(h.sleep, time.Millisecond), Watch: cmp.Or(h.sleep, time.Millisecond), Draft: h.draft, Wall: h.wall, Reading: h.reading, CapWait: cmp.Or(h.capWait, time.Millisecond), Overload: cmp.Or(h.overload, time.Millisecond), LostPoll: cmp.Or(h.lostPoll, time.Millisecond)},
	}
	if h.merge != nil {
		deps.Merge = h.merge
	}

	if h.batch != nil {
		deps.Batch = h.batch
	}

	deps.Preflight = h.preflight
	deps.Hosts = h.hosts
	deps.Queue = h.queue
	deps.Poll = h.poll

	if h.others != nil {
		deps.Others = h.others
	}

	deps.SandboxDefault = h.sandboxDefault
	deps.Repository = h.repository
	deps.Facts = h.factsWithMutation()
	deps.RefuseGate = h.refuseGate
	deps.Slots = h.slots
	deps.SkillLine = h.skillLine

	if deps.SkillLine == nil && !h.noSkillLine {
		deps.SkillLine = func(_, skill string) string { return "/fake-skills:" + skill }
	}

	if h.picker != nil {
		deps.Logins = h.picker
	}

	return deps
}

// tick runs one status tick and returns when it has finished. The second send is taken only after the first tick ended.
func (h *harness) tick(t *testing.T) {
	t.Helper()

	for range 2 {
		h.ticks <- time.Time{}
	}
}

// migratedDatabase is the database file of a new state folder after the migrations of the workflow store, built once for the test process.
// The migrations parse and run many SQL statements, which costs more than the rest of a test under the race detector.
var migratedDatabase = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "flow-template-")
	if err != nil {
		return nil, err
	}

	defer os.RemoveAll(dir)

	opts := Options{StateDir: filepath.Join(dir, "state"), Version: testVersion, MaxConcurrent: minMaxConcurrent, MaxReviewConcurrent: minMaxConcurrent, Env: map[string]string{}, Modules: []step.Module{StepModule()}}

	engine, err := NewEngine(context.Background(), opts, Deps{})
	if err != nil {
		return nil, err
	}

	if err = engine.Shutdown(); err != nil {
		return nil, err
	}

	return os.ReadFile(filepath.Join(opts.StateDir, databaseFile))
})

// migrate gives the state folder of the harness a copy of the migrated database, so the engine or the client of any test over the harness opens a store that has its migrations.
// The migrations cost more CPU than the rest of a test under the race detector. A test of the first open, which makes the folder and the database itself, calls fresh.
func (h *harness) migrate(t *testing.T) {
	t.Helper()

	text, err := migratedDatabase()
	require.NoError(t, err)
	require.NoError(t, config.EnsureStateDir(h.opts.StateDir))
	require.NoError(t, os.WriteFile(filepath.Join(h.opts.StateDir, databaseFile), text, privateMode))
}

// fresh removes the state folder, so the next open creates the folder and the database itself, for a test of the files that the first open makes.
func (h *harness) fresh(t *testing.T) {
	t.Helper()

	require.NoError(t, os.RemoveAll(h.opts.StateDir))
}

// engine builds and launches an engine over the harness. The cleanup shuts it down.
func (h *harness) engine(t *testing.T) *Engine {
	t.Helper()

	return h.engineWith(t, func(*Engine) {})
}

// engineWith is engine with a change that runs on the built engine before Launch. Nothing reads the engine before Launch, so a field the change sets has no reader to race with.
func (h *harness) engineWith(t *testing.T, change func(*Engine)) *Engine {
	t.Helper()

	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)

	change(engine)

	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

// opened builds and opens an engine over the harness without launching it, so that the test registers its own workflows and then launches. The cleanup shuts it down.
// engine and engineWith differ: they launch the engine with the workflows of the flow.
func (h *harness) opened(t *testing.T) *Engine {
	t.Helper()

	opts := h.opts
	state, err := openStore(opts)
	require.NoError(t, err)

	held, err := claimFolder(state.dir, applicationVersion(opts.Version), ModeRun)
	require.NoError(t, err)

	engine := newEngine(opts, h.deps(), state, held, moduleRegistry(t))
	require.NoError(t, engine.open(t.Context(), opts, state))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

// client opens a client over the harness state folder with the given build version.
func (h *harness) client(t *testing.T, version string) *Client {
	t.Helper()

	opts := h.opts
	opts.Version = version

	give := ReleaseDeps{Lifecycle: h.life, Beats: h.beats, Trees: h.trees, Clock: clock{Release: time.Second}}
	if h.merge != nil {
		give.Merge = h.merge
	}

	client, err := OpenClient(t.Context(), opts, give)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	return client
}

// runRef names the run that a wait watches. A nil runRef watches no run.
type runRef struct {
	ctx  context.Context
	flow dbos.Client
	id   string
	// database is the file of the workflow store, which holds the messages that were sent to the run.
	database string
}

// runOfEngine is the test run of an engine.
func runOfEngine(t *testing.T, engine *Engine) *runRef {
	t.Helper()

	return &runRef{ctx: t.Context(), flow: engine.ctx, id: testRun, database: filepath.Join(engine.dir, databaseFile)}
}

// runOfClient is the test run as a client reads it.
func runOfClient(t *testing.T, client *Client) *runRef {
	t.Helper()

	return &runRef{ctx: t.Context(), flow: client.flow, id: testRun, database: filepath.Join(client.dir, databaseFile)}
}

// messagePending says whether a message that the run receives (on the owner topic or the voice topic) waits unconsumed. DBOS wakes a receive for such a message at its next one-second poll, so the receive is not parked.
func (r *runRef) messagePending() (bool, error) {
	db, err := sql.Open("sqlite", r.database+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return false, err
	}

	defer db.Close()

	var pending bool

	err = db.QueryRowContext(r.ctx, "SELECT EXISTS (SELECT 1 FROM notifications WHERE destination_uuid = ? AND topic IN (?, ?) AND consumed = 0)", r.id, ownerTopic, voiceTopic).Scan(&pending)

	return pending, err
}

// stalled says why the run can no longer bring the awaited fact, or returns "". The run has ended, or it parks in a receive or a sleep that wakes after the bound of the wait.
func (r *runRef) stalled(bound time.Time) string {
	rows, err := dbos.ListWorkflows(r.flow, dbos.WithFilterWorkflowIDs(r.id))
	if err != nil {
		return fmt.Sprintf("the status of the run %s cannot be read: %v", r.id, err)
	}

	if len(rows) != 1 {
		return ""
	}

	if slices.Contains(endedStatuses, rows[0].Status) {
		return fmt.Sprintf("the run %s ended with status %s", r.id, rows[0].Status)
	}

	return r.parked(bound)
}

// endedStatuses are the statuses of a run that has ended.
var endedStatuses = []dbos.WorkflowStatusType{dbos.WorkflowStatusSuccess, dbos.WorkflowStatusError, dbos.WorkflowStatusCancelled, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded}

// parked says that the run waits in a receive window that ends after the bound, or returns "". A receive takes a step id for its DBOS.recv step and the next id for its window, a DBOS.sleep step. The window is written first and stays the last step after the message arrives. The DBOS.recv step is written when the receive ends, so the window has ended once that step is recorded. A window with an unconsumed message is about to end, so it is not a park.
func (r *runRef) parked(bound time.Time) string {
	pending, err := r.messagePending()
	if err != nil {
		return fmt.Sprintf("the messages of the run %s cannot be read: %v", r.id, err)
	}

	if pending {
		return ""
	}

	steps, err := dbos.GetWorkflowSteps(r.flow, r.id, dbos.WithStepsLoadOutput(true))
	if err != nil {
		return fmt.Sprintf("the steps of the run %s cannot be read: %v", r.id, err)
	}

	if len(steps) == 0 {
		return ""
	}

	last := steps[len(steps)-1]
	if last.StepName != "DBOS.sleep" {
		return ""
	}

	wake, ok := windowEnd(last.Output)
	if !ok || !wake.After(bound) {
		return ""
	}

	received := func(s dbos.StepInfo) bool { return s.StepName == "DBOS.recv" && s.StepID == last.StepID-1 }
	if slices.ContainsFunc(steps, received) {
		return ""
	}

	return fmt.Sprintf("the run %s waits in step %d until %s, which is after the bound of the wait", r.id, last.StepID, wake.Format(time.RFC3339))
}

// windowEnd reads the end of a receive window from the output of its DBOS.sleep step: Unix milliseconds.
func windowEnd(output any) (time.Time, bool) {
	text, isText := output.(string)
	if !isText {
		return time.Time{}, false
	}

	ms, err := strconv.ParseInt(text, 10, 64)

	return time.UnixMilli(ms), err == nil
}

// waiting is one wait of a test: a fact that arrives on ch or that read finds, and the run under test that must be able to bring it.
type waiting[T any] struct {
	what string
	run  *runRef
	ch   <-chan T
	read func() (value T, found bool, err error)
	last func() string
}

// found reads the fact once without blocking.
func (w waiting[T]) found(t *testing.T) (T, bool) {
	t.Helper()

	select {
	case value := <-w.ch:
		return value, true
	default:
	}

	if w.read == nil {
		return *new(T), false
	}

	value, ok, err := w.read()
	if err != nil {
		t.Fatalf("waiting for %s: %v", w.what, err)
	}

	return value, ok
}

// stallLook reads the run of a wait at most every runCheckEvery, and confirms a stall only when two reads in a row see it.
type stallLook struct {
	checked time.Time
	stalls  int
}

// confirmed returns why the run is stalled, or "" when it is not, when no run is watched, or when the stall is not yet confirmed.
func (s *stallLook) confirmed(run *runRef, bound time.Time) string {
	if run == nil || time.Since(s.checked) < runCheckEvery {
		return ""
	}

	s.checked = time.Now()

	why := run.stalled(bound)
	if why == "" {
		s.stalls = 0

		return ""
	}

	if s.stalls++; s.stalls < 2 {
		return ""
	}

	return why
}

// waitOn is the one wait of the flow tests. It returns when the fact is recorded. When the run under test ends, or parks until after the bound, while the fact is still absent, it fails at once and names the run.
// The bound of eventTimeout stays only as a guard against a real hang.
func waitOn[T any](t *testing.T, w waiting[T]) T {
	t.Helper()

	// The ticker wakes the wait to read the fact or the run. Only the channel of the fact wakes a wait with neither.
	var tick <-chan time.Time

	switch {
	case w.read != nil:
		ticker := time.NewTicker(pollEvery)
		defer ticker.Stop()

		tick = ticker.C
	case w.run != nil:
		ticker := time.NewTicker(runCheckEvery)
		defer ticker.Stop()

		tick = ticker.C
	}

	bound := time.Now().Add(eventTimeout)
	guard := time.NewTimer(eventTimeout)

	defer guard.Stop()

	var look stallLook

	for {
		select {
		case value := <-w.ch:
			return value
		case <-tick:
			if value, ok := w.found(t); ok {
				return value
			}

			why := look.confirmed(w.run, bound)
			if why == "" {
				continue
			}

			// The fact is read after the stall is seen, so a fact written by the end of the run is not missed.
			if value, ok := w.found(t); ok {
				return value
			}

			t.Fatalf("%s, and %s is not there%s", why, w.what, w.lastText())
		case <-guard.C:
			t.Fatalf("waited %s for %s%s", eventTimeout, w.what, w.lastText())
		}
	}
}

// lastText is the text of last after a semicolon, or "" when the wait has no last.
func (w waiting[T]) lastText() string {
	if w.last == nil {
		return ""
	}

	return "; " + w.last()
}

// hang holds a fake that stands for a hung child until release fires. It is the hang that a test provokes, not a wait of a test, and a fake has no test to fail.
func hang[T any](release <-chan T) {
	<-release
}

// resultOfHandle waits for the result of the run of handle. The result is the end of the run. When the wait fails, the goroutine that reads the result ends with the shutdown of the engine in the cleanup of the test, which cancels the context of the handle.
func resultOfHandle[R any](t *testing.T, handle dbos.WorkflowHandle[R]) (R, error) {
	t.Helper()

	type outcome struct {
		result R
		err    error
	}

	done := make(chan outcome, 1)

	go func() {
		result, err := handle.GetResult(dbos.WithHandlePollingInterval(storePoll))
		done <- outcome{result: result, err: err}
	}()

	got := waitOn(t, waiting[outcome]{what: "the result of run " + handle.GetWorkflowID(), ch: done})

	return got.result, got.err
}

// receive waits for a value on ch, such as the outcome of a run.
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()

	return waitOn(t, waiting[T]{what: "a value on a channel of the test", ch: ch})
}

// condition is the fact that check returns true. The test reads it with waitOn.
func condition(check func() bool) waiting[struct{}] {
	return waiting[struct{}]{what: "a condition of the test", read: func() (struct{}, bool, error) { return struct{}{}, check(), nil }}
}

// conditionOf is the fact that check returns true, for a test that has a run under test. The wait fails at once when the run ends or parks without the fact.
func conditionOf(run *runRef, check func() bool) waiting[struct{}] {
	fact := condition(check)
	fact.run = run

	return fact
}

// windowsEnded counts the receive windows of the run that have ended: each one is a DBOS.recv step, recorded when its receive ends. A test that expects no answer waits for a count and does not sleep.
func (r *runRef) windowsEnded() (int, error) {
	steps, err := dbos.GetWorkflowSteps(r.flow, r.id)
	if err != nil {
		return 0, err
	}

	return len(slices.DeleteFunc(steps, func(s dbos.StepInfo) bool { return s.StepName != "DBOS.recv" })), nil
}

// windowsEndedBy is the fact that the run has ended at least count receive windows. The run must still be alive: the wait fails when the run ends or parks first.
func windowsEndedBy(run *runRef, count int) waiting[struct{}] {
	return waiting[struct{}]{what: fmt.Sprintf("%d receive windows of the run to end", count), run: run, read: func() (struct{}, bool, error) {
		ended, err := run.windowsEnded()

		return struct{}{}, ended >= count, err
	}}
}

// inASleep is the fact that the run has started a durable sleep. The sleep records its step when it starts, so a cancel sent after this lands inside the sleep and not in the step before it.
func inASleep(t *testing.T, engine *Engine) waiting[struct{}] {
	t.Helper()

	return waiting[struct{}]{what: "the run to start a durable sleep", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)

		return struct{}{}, err == nil && slices.ContainsFunc(steps, func(s dbos.StepInfo) bool { return s.StepName == "DBOS.sleep" }), nil
	}}
}

// ownerAsks is the fact that the open run shows its question. The last verdict is recorded before the run asks, so a wait on the verdict count alone can read the run in between.
func ownerAsks(t *testing.T, h *harness) waiting[struct{}] {
	t.Helper()

	client := h.client(t, testVersion)
	read := newStatusRead(client)

	return waiting[struct{}]{what: "the open run to show a question", run: runOfClient(t, client), last: read.last, read: func() (struct{}, bool, error) {
		infos := read.next()

		return struct{}{}, read.err == nil && len(infos) == 1 && infos[0].Question != "", nil
	}}
}

// statusRead reads the status of the runs and keeps the last result and the last error, so a wait that ends at its deadline can report them.
type statusRead struct {
	client *Client
	infos  []RunInfo
	err    error
}

func newStatusRead(client *Client) *statusRead { return &statusRead{client: client} }

// next reads the status once. On an error it keeps the error and returns the infos of the last good read.
func (s *statusRead) next() []RunInfo {
	infos, err := s.client.Status()
	s.err = err

	if err == nil {
		s.infos = infos
	}

	return s.infos
}

func (s *statusRead) last() string {
	return fmt.Sprintf("the last status read was %+v with error %v", s.infos, s.err)
}

// runInBackground runs an item on the engine and returns the channel that gets the outcome.
func runInBackground(engine *Engine, item string) <-chan runOutcome {
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.Run(item, testProject)
		done <- runOutcome{result: result, err: err}
	}()

	return done
}

type runOutcome struct {
	result ItemResult
	err    error
}

// fakeForge answers every read with state, or fails with err. It records the items it was asked about.
type fakeForge struct {
	mu    sync.Mutex
	life  *fakeLifecycle
	state review.Standing
	err   error
	asked []string
	// heads is the head of the pull request of each item that has its own. An item that is not in it gets the head of state.
	heads map[string]string
	// failFrom, when above zero, makes the read of that number and every later read fail with errFake.
	failFrom int
	// closing is the closing issues that each read of ClosingIssues gives, in order; the last repeats. closingReads counts the reads.
	closing      [][]int
	closingReads int
	closingErr   error // fails every read after the first closingOK reads
	closingOK    int
	noPullAfter  int
	noPull       bool // reports no pull request after the first noPullAfter reads
	// notes holds each Comment call as project|item|key|text. writeErr fails it.
	notes    []string
	opened   []string // each Open call as project|item|title|body|issue|base|parent
	writeErr error
	// pushed holds each Push call as project|item|branch|head|expect. pushErr fails it.
	pushed  []string
	pushErr error
	// history is what History answers; historyErr fails it. historyAsked holds each History call as project|item.
	history      []model.PriorPull
	historyErr   error
	historyAsked []string
}

// Push records the call.
func (f *fakeForge) Push(_ context.Context, project, item, branch, head, expect string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.pushed = append(f.pushed, strings.Join([]string{project, item, branch, head, expect}, "|"))

	return f.pushErr
}

// History records the call and answers with history.
func (f *fakeForge) History(_ context.Context, project, item string) ([]model.PriorPull, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.historyAsked = append(f.historyAsked, project+"|"+item)

	return f.history, f.historyErr
}

// Comment records the call and answers with the number of the standing.
func (f *fakeForge) Comment(_ context.Context, project, item, key, text string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.notes = append(f.notes, project+"|"+item+"|"+key+"|"+text)

	return f.state.Number, f.writeErr
}

// Open records the call as project|item|title|body|issue|base|parent. The first call creates the pull request and the later ones find it.
// A parent gives the stack S_1.
func (f *fakeForge) Open(_ context.Context, project, item string, req OpenRequest) (OpenResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.opened = append(f.opened, strings.Join([]string{project, item, req.Title, req.Body, strconv.Itoa(req.Issue), req.Base, req.Parent}, "|"))

	out := OpenResult{Number: f.state.Number, Created: len(f.opened) == 1}
	if req.Parent != "" {
		out.Stack = "S_1"
	}

	return out, f.writeErr
}

func (f *fakeForge) Pull(_ context.Context, _, item string) (review.Standing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, item)

	if f.failFrom > 0 && len(f.asked) >= f.failFrom {
		return review.Standing{}, errFake
	}

	standing := f.state
	if head, own := f.heads[item]; own {
		standing.Head = head
	}

	return standing, f.err
}

// ClosingIssues answers with the next of closing and then repeats the last. A set of nil is the empty set. noPull makes it report no pull request. closingErr fails every read.
func (f *fakeForge) ClosingIssues(_ context.Context, _, _ string) ([]int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closingReads++

	if f.closingErr != nil && f.closingReads > f.closingOK {
		return nil, false, f.closingErr
	}

	gone := f.noPull && f.closingReads > f.noPullAfter

	if gone || len(f.closing) == 0 {
		return nil, !gone, nil
	}

	return f.closing[min(f.closingReads, len(f.closing))-1], true, nil
}

// fakeTrees answers every lookup with dir, or fails with err. A fakeTrees with late set has no folder until Ensure makes it.
type fakeTrees struct {
	dir string
	err error
	// ensureErr fails Ensure only.
	ensureErr error
	// headsErr fails Heads only, and Heads still returns the branches it read, so a release that ignores the error sees the tip.
	headsErr error
	late     *lateFolder
	// per, when set, gives the folder of each item and wins over dir and late.
	per func(ctx context.Context, item string) (string, error)
	// repos, when set, is the folder of the clones, for item folders that are not beside their clone.
	repos string
	// ensured records the item of each Ensure call when a test sets it.
	ensured *ensureLog
	// starts records the start of each Ensure call when a test sets it.
	starts *ensureLog
	// bases records the base of each Ensure call when a test sets it.
	bases *ensureLog
}

// ensureLog is the items that Ensure was called for, in order.
type ensureLog struct {
	mu    sync.Mutex
	items []string
}

func (l *ensureLog) add(item string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.items = append(l.items, item)
}

func (l *ensureLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.items...)
}

// lateFolder is a worktree that exists only after Ensure.
type lateFolder struct {
	mu   sync.Mutex
	dir  string
	made bool
}

func (l *lateFolder) path() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.made {
		return l.dir
	}

	return ""
}

func (l *lateFolder) make() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.made = true

	return l.dir
}

func (f fakeTrees) Path(ctx context.Context, _, item string) (string, error) {
	if f.per != nil {
		return f.per(ctx, item)
	}

	if f.late != nil {
		return f.late.path(), f.err
	}

	return f.dir, f.err
}

// engineGit is the runner of the engine for a folder whose parent folder holds the clones: a repository there, or a worktree of one.
func engineGit(dir string) proc.Runner {
	return gitexec.NewRepositories(filepath.Dir(dir)).Runner(dir, 0)
}

func (f fakeTrees) Git(dir string) proc.Runner {
	if f.repos != "" {
		return gitexec.NewRepositories(f.repos).Runner(dir, 0)
	}

	return engineGit(dir)
}

func (f fakeTrees) WithTree(ctx context.Context, project, item string, fn func(dir string)) error {
	dir, err := f.Path(ctx, project, item)
	if err != nil {
		return err
	}

	fn(dir)

	return nil
}

// Heads reads the branches of the origin remote of the one folder that the fake gives. A fake that gives a folder for each item has no one remote.
func (f fakeTrees) Heads(ctx context.Context, _ string) (string, error) {
	dir := f.dir
	if f.late != nil {
		dir = f.late.path()
	}

	if dir == "" {
		return "", errFake
	}

	// The read of the branches reaches the remote, so it runs with plain git and not with the local git calls of the release. It allows the ext transport,
	// so a test can give the remote a program that does not answer.
	cmd := proc.GroupCommand(ctx, "git", []string{"ls-remote", "--heads", remoteName}, dir)
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=file:ext")

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return string(out), f.headsErr
}

func (f fakeTrees) Ensure(ctx context.Context, _, item, start, base string) (string, error) {
	if f.ensured != nil {
		f.ensured.add(item)
	}

	if f.bases != nil {
		f.bases.add(base)
	}

	if f.starts != nil {
		f.starts.add(start)
	}

	if f.ensureErr != nil {
		return "", f.ensureErr
	}

	if f.per != nil {
		return f.per(ctx, item)
	}

	if f.late != nil {
		return f.late.make(), f.err
	}

	return f.dir, f.err
}
