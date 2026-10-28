package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/review"
)

const (
	testVersion  = "v1"
	testLogin    = "alice"
	testItem     = "12"
	testPull     = 7
	testProject  = "p"
	testOwner    = "owner-"
	eventTimeout = 30 * time.Second
	pollEvery    = 5 * time.Millisecond
	storePoll    = 5 * time.Millisecond
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
}

// Run holds each agent in flight until the test closes gate, when gate is set. It reports pid as the process of the agent, when pid is set.
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
	a.requests = append(a.requests, request)
	a.inFlight++
	a.perLogin[request.Login]++
	a.maxTotal = max(a.maxTotal, a.inFlight)
	a.maxLogin = max(a.maxLogin, a.perLogin[request.Login])
	a.mu.Unlock()

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

type fakeBriefs struct {
	mu       sync.Mutex
	requests []BriefRequest
	err      error
}

func (b *fakeBriefs) Write(_ context.Context, request BriefRequest) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.requests = append(b.requests, request)

	return "brief-" + string(request.Step), b.err
}

func (b *fakeBriefs) seen() []BriefRequest {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]BriefRequest(nil), b.requests...)
}

// fakeGate answers each call from results in order; the last result repeats.
type fakeGate struct {
	mu      sync.Mutex
	results []GateResult
	calls   int
	err     error
}

func (g *fakeGate) Run(_ context.Context, _, _ string) (GateResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	index := min(g.calls, len(g.results)-1)
	g.calls++

	return g.results[index], g.err
}

// fakeVerdicts answers each call from reviews in order; the last review repeats.
type fakeVerdicts struct {
	mu      sync.Mutex
	reviews []Review
	rounds  []int
	err     error
	// failFirst makes the first reads fail with errFake.
	failFirst int
}

func (v *fakeVerdicts) Read(_ context.Context, _, _ string, round int) (Review, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	index := min(len(v.rounds), len(v.reviews)-1)
	failing := len(v.rounds) < v.failFirst
	v.rounds = append(v.rounds, round)

	if failing {
		return Review{}, errFake
	}

	return v.reviews[index], v.err
}

func (v *fakeVerdicts) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()

	return len(v.rounds)
}

// fakeLogins picks byItem[item] when set and login otherwise.
type fakeLogins struct {
	login  string
	byItem map[string]string
	fresh  string
	err    error
}

func (l fakeLogins) PickFresh(_ context.Context, _, _ string) (string, error) {
	return l.fresh, l.err
}

func (l fakeLogins) Pick(_ context.Context, _, item string) (string, error) {
	if login, ok := l.byItem[item]; ok {
		return login, l.err
	}

	return l.login, l.err
}

// loginOnce picks the login for the first call. Every later call returns an empty login and the error in later.
type loginOnce struct {
	mu    sync.Mutex
	calls int
	later error
}

func (l *loginOnce) Pick(context.Context, string, string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls++

	if l.calls == 1 {
		return testLogin, nil
	}

	return "", l.later
}

func (l *loginOnce) PickFresh(context.Context, string, string) (string, error) { return "", nil }

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

func (l *fakeLifecycle) Ready(_ context.Context, project, item string) error {
	return l.step("ready", project, item)
}

func (l *fakeLifecycle) Release(ctx context.Context, project, item, owner string) error {
	l.mu.Lock()
	l.released = append(l.released, owner)
	l.mu.Unlock()

	if l.hangs() {
		<-ctx.Done()
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
}

func (b *fakeBeats) Beat(_ context.Context, project, owner string) (string, error) {
	b.mu.Lock()
	b.calls = append(b.calls, project+"/"+owner)
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
	agents   *fakeAgents
	briefs   *fakeBriefs
	gate     *fakeGate
	verdicts *fakeVerdicts
	logins   fakeLogins
	life     *fakeLifecycle
	merge    *fakeMerge
	forge    *fakeForge
	watch    *fakeWatch
	trees    *fakeTrees
	beats    *fakeBeats
	ticks    chan time.Time
	sleep    time.Duration
	opts     Options
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	return &harness{
		agents:   &fakeAgents{perLogin: map[string]int{}},
		briefs:   &fakeBriefs{},
		gate:     &fakeGate{results: []GateResult{{Passed: true}}},
		verdicts: &fakeVerdicts{reviews: []Review{{Verdict: VerdictApprove}}},
		logins:   fakeLogins{login: testLogin},
		life:     &fakeLifecycle{failing: map[string]bool{}},
		forge:    &fakeForge{state: review.Standing{Number: testPull}},
		watch:    &fakeWatch{outcomes: []review.Outcome{{Kind: review.Merged}}},
		trees:    &fakeTrees{},
		beats:    &fakeBeats{},
		ticks:    make(chan time.Time),
		opts: Options{
			StateDir:      filepath.Join(t.TempDir(), "state"),
			Version:       testVersion,
			MaxConcurrent: 2,
			Env:           map[string]string{},
		},
	}
}

func (h *harness) deps() Deps {
	deps := Deps{
		Briefs: h.briefs, Agents: h.agents, Gate: h.gate, Verdicts: h.verdicts, Logins: h.logins, Lifecycle: h.life, Forge: h.forge, Watch: h.watch, Trees: h.trees, Beats: h.beats,
		Clock: clock{Tick: h.ticks, Poll: storePoll, Watch: cmp.Or(h.sleep, time.Millisecond)},
	}
	if h.merge != nil {
		deps.Merge = h.merge
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

// engine builds and launches an engine over the harness. The cleanup shuts it down.
func (h *harness) engine(t *testing.T) *Engine {
	t.Helper()

	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

// client opens a client over the harness state folder with the given build version.
func (h *harness) client(t *testing.T, version string) *Client {
	t.Helper()

	opts := h.opts
	opts.Version = version

	client, err := OpenClient(t.Context(), opts, ReleaseDeps{Lifecycle: h.life, Trees: h.trees, Clock: clock{Release: time.Second}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	return client
}

// eventually polls check until it returns true or eventTimeout passes.
func eventually(t *testing.T, check func() bool) {
	t.Helper()

	require.Eventually(t, check, eventTimeout, pollEvery)
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
	state review.Standing
	err   error
	asked []string
	// failFrom, when above zero, makes the read of that number and every later read fail with errFake.
	failFrom int
}

func (f *fakeForge) Pull(_ context.Context, _, item string) (review.Standing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, item)

	if f.failFrom > 0 && len(f.asked) >= f.failFrom {
		return review.Standing{}, errFake
	}

	return f.state, f.err
}

// fakeWatch answers each read with the next of outcomes and then repeats the last. A non-nil err fails every read.
// It records the pull requests it read and the draft calls.
type fakeWatch struct {
	mu       sync.Mutex
	outcomes []review.Outcome
	err      error
	errs     []error // the error of read n, the last one repeats; it replaces err
	draftErr error
	reads    []int
	drafts   []int
}

func (f *fakeWatch) Watch(_ context.Context, pr int) (review.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.reads = append(f.reads, pr)

	next := f.outcomes[min(len(f.reads), len(f.outcomes))-1]

	if len(f.errs) > 0 {
		return next, f.errs[min(len(f.reads), len(f.errs))-1]
	}

	return next, f.err
}

func (f *fakeWatch) SetDraft(_ context.Context, pr int, draft bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if draft {
		f.drafts = append(f.drafts, pr)
	}

	return f.draftErr
}

func (f *fakeWatch) seen() (reads, drafts []int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]int(nil), f.reads...), append([]int(nil), f.drafts...)
}

// fakeTrees answers every lookup with dir, or fails with err.
type fakeTrees struct {
	dir string
	err error
}

func (f fakeTrees) Path(context.Context, string, string) (string, error) {
	return f.dir, f.err
}
