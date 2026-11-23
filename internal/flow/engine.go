package flow

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	// The sqlite driver registers itself with the workflow store.
	_ "github.com/dbos-inc/dbos-transact-golang/dbos/driver/sqlite"
	"github.com/gofrs/flock"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/step"
	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	appName      = "beehave"
	executorName = "local"
	queueName    = "agents"
	// reviewsQueueName is the queue of the agent steps of the kind review. The agents queue holds the steps of the kind implement.
	reviewsQueueName = "reviews"
	itemName         = "item"
	treeName         = "tree"
	phaseName        = "phase"
	databaseFile     = "dbos.db"
	logFileName      = "flow.log"
	logFileMode      = 0o600
	stateScheme      = "sqlite:"

	shutdownTimeout  = 10 * time.Second
	partitionLimit   = 1
	receiveWindow    = 7 * 24 * time.Hour
	beatLimit        = 30 * time.Second
	minMaxConcurrent = 1
)

// errBadOptions is returned when the options of the engine or the client are not usable.
var errBadOptions = errors.New("invalid flow options")

// errEngineShutDown is returned when a run asks for its item after the engine began to shut down. The engine starts nothing then.
var errEngineShutDown = errors.New("the engine has shut down")

// Options holds the settings the engine and the client share.
// Version is the build version that pins each run.
// MaxConcurrent is agents.max_concurrent, the cap of the implement queue. MaxReviewConcurrent is agents.max_review_concurrent, the cap of the review queue.
type Options struct {
	StateDir            string
	Version             string
	MaxConcurrent       int
	MaxReviewConcurrent int
	Env                 map[string]string
	// MaxRunBudget is agents.max_run_budget_usd: the dollars that the agent steps of one item run may spend in all. 0 means no cap.
	// A child workflow that the run spawns starts its own sum. A step may pass the cap by its own cost, because the check runs when the step starts.
	// An agent step does not start when the recorded costs of the earlier agent steps of its run reach the cap.
	MaxRunBudget float64
	// MaxTreeBudget is agents.max_tree_budget_usd: the dollars that the agent steps of the root run and every run it spawns may spend in all. 0 means no cap.
	MaxTreeBudget float64
	// MaxSpawnDepth is the depth of the deepest items queue: the highest max_spawn_depth of the projects. 0 means config.DefaultMaxSpawnDepth.
	MaxSpawnDepth int
	// ReviewPoll is the time between two review polls. 0 means no poll.
	ReviewPoll time.Duration
	// ReviewLookbackDays is agents.review_lookback_days: the days that the review poll looks back for replies. 0 means config.DefaultReviewLookbackDays.
	ReviewLookbackDays int
	// PerLogin is agents.per_login, the agent steps that one login runs at once in each agent queue. 0 means config.DefaultPerLogin.
	PerLogin int
	// Concurrency is agents.concurrency, the mode of the cap of the agents queue. Empty means config.ConcurrencyStatic.
	Concurrency config.Concurrency
	// Retention is the age at which Launch deletes a finished run: agents.retention_days. 0 means config.DefaultRetention.
	Retention time.Duration
	// KeepRuns turns the deletion off, so every finished run stays: agents.retention_days set to 0.
	KeepRuns bool
	// OverCap lets the first agent step of a run take a runner that is over its usage cap when no runner has room. The step records that it did.
	OverCap bool
	// Progress receives one line when a run starts and one when each top-level step of a run starts and ends.
	// A client writes there the runs that a cancel of a canceled run stopped. A nil Progress prints nothing.
	Progress io.Writer
	// Mode is ModeRun or ModeServe: what beehave status says about the engine that holds the state folder. The empty value means ModeRun.
	Mode string
	// Modules are the step modules that the engine runs. The engine builds its builtins, its step kinds and its tables of signatures and results from them.
	// A run that calls a builtin needs the module of that builtin in the list. Two modules that name the same builtin or the same step kind fail NewEngine.
	Modules []step.Module
}

func (o Options) validate() error {
	if err := config.RefuseDBOSVariables(o.Env); err != nil {
		return err
	}

	switch {
	case o.Version == "":
		return fmt.Errorf("%w: the build version is empty", errBadOptions)
	case !filepath.IsAbs(o.StateDir):
		return fmt.Errorf("%w: the state folder %q is not an absolute path", errBadOptions, o.StateDir)
	case o.MaxConcurrent < minMaxConcurrent:
		return fmt.Errorf("%w: agents.max_concurrent is %d: set it to 1 or more", errBadOptions, o.MaxConcurrent)
	case o.MaxReviewConcurrent < minMaxConcurrent:
		return fmt.Errorf("%w: agents.max_review_concurrent is %d: set it to 1 or more", errBadOptions, o.MaxReviewConcurrent)
	}

	if err := errors.Join(o.validateReviewPoll(), o.validateConcurrency()); err != nil {
		return err
	}

	return o.validatePerLogin()
}

// validateConcurrency refuses a concurrency mode that is neither empty nor one of the two that the engine knows.
func (o Options) validateConcurrency() error {
	if o.Concurrency == "" || o.Concurrency.Known() {
		return nil
	}

	return fmt.Errorf("%w: agents.concurrency is %q: set it to %q or %q. Set it in runners.star or in BEEHAVE_AGENTS_CONCURRENCY", errBadOptions, string(o.Concurrency), config.ConcurrencyStatic, config.ConcurrencyAdaptive)
}

// validateReviewPoll refuses a negative poll interval and a negative look-back.
func (o Options) validateReviewPoll() error {
	switch {
	case o.ReviewPoll < 0:
		return fmt.Errorf("%w: the review poll interval is %s: set it to 0 or more", errBadOptions, o.ReviewPoll)
	case o.ReviewLookbackDays < 0:
		return fmt.Errorf("%w: agents.review_lookback_days is %d: set it to 1 or more", errBadOptions, o.ReviewLookbackDays)
	}

	return nil
}

// validatePerLogin refuses a negative per_login, and one above the cap of an agent queue: DBOS refuses a queue whose partition limit is above its global limit.
func (o Options) validatePerLogin() error {
	switch {
	case o.PerLogin < 0:
		return fmt.Errorf("%w: agents.per_login is %d: set it to 1 or more", errBadOptions, o.PerLogin)
	case o.PerLogin > min(o.MaxConcurrent, o.MaxReviewConcurrent):
		return fmt.Errorf("%w: agents.per_login is %d: set it to agents.max_concurrent (%d) or agents.max_review_concurrent (%d), whichever is lower, or less", errBadOptions, o.PerLogin, o.MaxConcurrent, o.MaxReviewConcurrent)
	}

	return nil
}

// store is the state folder a process opened: the database address and the log.
type store struct {
	dir string
	url string
	log *os.File
}

// openStore creates the state folder, resolves it to its real path and opens the log file at mode 0600. Every path of the engine is built from that real path.
func openStore(opts Options) (store, error) {
	if err := opts.validate(); err != nil {
		return store{}, err
	}

	if err := config.EnsureStateDir(opts.StateDir); err != nil {
		return store{}, err
	}

	return openStoreAt(opts.StateDir, filepath.EvalSymlinks)
}

// openStoreAt opens the log file of a state folder that exists. evalLinks resolves every link of the folder path.
// The OS compares real paths, so a path with a link in it opens nothing in the sandbox of an agent step.
func openStoreAt(state string, evalLinks func(string) (string, error)) (store, error) {
	dir, err := evalLinks(state)
	if err != nil {
		return store{}, fmt.Errorf("resolve the state folder %s: %w", state, err)
	}

	log, err := os.OpenFile(filepath.Clean(filepath.Join(dir, logFileName)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, logFileMode)
	if err != nil {
		return store{}, fmt.Errorf("open the log file: %w", err)
	}

	return store{dir: dir, url: stateScheme + filepath.Join(dir, databaseFile), log: log}, nil
}

// Engine runs the item workflows. Call NewEngine, then Launch, then Run for each item, then Shutdown.
type Engine struct {
	ctx dbos.Context
	// registry holds the builtins, the step kinds and the tables of signatures and results of the modules that this engine runs.
	registry step.Registry
	// queue is the agents queue: the steps of the kind implement.
	queue dbos.Queue
	// reviewQueue is the queue of the steps of the kind review.
	reviewQueue dbos.Queue
	// itemQueues are the queues of the item runs: the queue of depth d is itemQueues[d], and itemQueues[0] holds the runs that a user started.
	itemQueues []dbos.Queue
	// exclusiveQueue is the queue of the gates and the merges: one run for each repository.
	exclusiveQueue dbos.Queue
	// hostQueue is the queue of the build jobs of the gates: one job for each build host, whatever the project.
	hostQueue dbos.Queue
	deps      Deps
	// maxConcurrent is agents.max_concurrent, the cap of the items queue. The queue builtin subtracts the open runs from it.
	maxConcurrent int
	// perLogin is the agent steps that one login runs at once in each agent queue: Options.PerLogin, or the default of the config.
	perLogin int
	// maxRunBudget is Options.MaxRunBudget.
	maxRunBudget float64
	// maxTreeBudget is Options.MaxTreeBudget.
	maxTreeBudget float64
	// reviewPoll is the time between two review polls. 0 means no poll.
	reviewPoll time.Duration
	// reviewLookbackDays is the days that the review poll looks back for replies: Options.ReviewLookbackDays, or the default of the config.
	reviewLookbackDays int
	// retention and keepRuns are Options.Retention and Options.KeepRuns.
	retention time.Duration
	keepRuns  bool
	// concurrency is Options.Concurrency: the mode of the cap of the agents queue.
	concurrency config.Concurrency
	// limited carries each phase that ends at a provider limit to the controller of an adaptive engine. A static engine has no controller to read it, so the channel keeps the first signals and drops the rest.
	limited chan limitSignal
	// overCap is Options.OverCap.
	overCap bool
	// serving is true for the engine of beehave serve, which says the off line once at its start for each open run that it resumes with the sandbox off.
	serving bool
	// setSchedule sets or deletes the schedule of the review poll at launch. A test replaces it to fail the call.
	setSchedule func(*Engine) error
	version     string

	dir     string
	log     io.Closer
	lock    *flock.Flock
	logger  *slog.Logger
	env     map[string]string
	redact  func(string) string
	cancels cancels
	holds   holds
	procs   *groups
	tickEnd chan struct{}
	tickRun sync.WaitGroup
	// progress writes the lines of the terminal: the start of a run and the start and end of each of its steps.
	progress *progress
	// launched is the time of the last Launch. A run that was open then and ended after it was resumed by this engine.
	launched time.Time
	// starts orders the start of a run against the shutdown: a start holds it for reading, and the shutdown takes it to set stopped.
	starts  sync.RWMutex
	stopped bool
	down    sync.Once
	downErr error
}

// NewEngine opens the state folder and registers the workflows and the agents queue. Nothing runs until Launch.
// One engine owns a state folder at a time: a second NewEngine on the same folder fails until Shutdown.
// A Ctrl-C of the caller must not end the runs, so the engine does not take the cancel of ctx: Shutdown stops it and leaves the runs open.
func NewEngine(ctx context.Context, opts Options, deps Deps) (*Engine, error) {
	registry, err := buildRegistry(opts.Modules)
	if err != nil {
		return nil, err
	}

	state, held, err := openFolder(opts)
	if err != nil {
		return nil, err
	}

	engine := newEngine(opts, deps, state, held, registry)

	if err := engine.open(ctx, opts, state); err != nil {
		return nil, errors.Join(fmt.Errorf("open the workflow store: %w", err), removeEngineFiles(state.dir), held.Unlock(), state.log.Close())
	}

	return engine.registered(engine.register(opts))
}

// buildRegistry builds the registry of the step modules. It refuses an empty list, because an engine with no builtins fails every workflow.
func buildRegistry(modules []step.Module) (step.Registry, error) {
	if len(modules) == 0 {
		return step.Registry{}, errors.New("the composition root passed no step modules, so the engine has no builtins. Pass at least one module in Options.Modules")
	}

	registry, err := step.NewRegistry(modules)
	if err != nil {
		return step.Registry{}, fmt.Errorf("the step modules clash: %w", err)
	}

	return registry, nil
}

// openFolder opens the state folder and takes the engine lock of it. It closes the log when it fails.
func openFolder(opts Options) (store, *flock.Flock, error) {
	state, err := openStore(opts)
	if err != nil {
		return store{}, nil, err
	}

	held, err := claimFolder(state.dir, applicationVersion(opts.Version), cmp.Or(opts.Mode, ModeRun))
	if err != nil {
		return store{}, nil, errors.Join(err, state.log.Close())
	}

	return state, held, nil
}

// claimFolder takes the engine lock of the state folder and writes the version and the description of this engine, all under probe.lock, so no check reads the files of an engine that stopped. It leaves the engine lock free when it fails.
func claimFolder(dir, version, mode string) (*flock.Flock, error) {
	probe, err := holdProbe(dir)
	if err != nil {
		return nil, err
	}

	held, err := holdEngine(dir)
	if err != nil {
		return nil, errors.Join(err, probe.Unlock())
	}

	err = errors.Join(announceEngine(dir, version), writeEngineInfo(dir, mode, os.Getpid(), time.Now()))
	if err != nil {
		err = errors.Join(err, held.Unlock())
	}

	if err = errors.Join(err, probe.Unlock()); err != nil {
		return nil, err
	}

	return held, nil
}

// newEngine makes the engine value for an opened store and a held folder lock.
func newEngine(opts Options, deps Deps, state store, held *flock.Flock, registry step.Registry) *Engine {
	redact := runRedactor(opts.Env)
	if deps.Commands != nil {
		deps.Commands = redactedCommands{inner: deps.Commands, redact: redact}
	}

	engine := &Engine{
		registry: registry, deps: deps, maxConcurrent: opts.MaxConcurrent, perLogin: cmp.Or(opts.PerLogin, config.DefaultPerLogin), maxRunBudget: opts.MaxRunBudget, maxTreeBudget: opts.MaxTreeBudget, reviewPoll: opts.ReviewPoll, reviewLookbackDays: cmp.Or(opts.ReviewLookbackDays, config.DefaultReviewLookbackDays), retention: opts.Retention, keepRuns: opts.KeepRuns, concurrency: opts.Concurrency, limited: make(chan limitSignal, limitSignals), overCap: opts.OverCap, serving: opts.Mode == ModeServe, setSchedule: (*Engine).schedulePoll, env: opts.Env, version: applicationVersion(opts.Version), dir: state.dir, log: state.log, lock: held, redact: redact, logger: newLogger(state.log, redact),
		procs: &groups{path: groupsFile(state.dir)}, tickEnd: make(chan struct{}), progress: &progress{out: opts.Progress, redact: redact},
	}
	warnShortSecrets(engine.logger, opts.Env)

	return engine
}

// facts is the project as the config holds it. An engine whose deps have no Facts has no facts for any project.
func (e *Engine) facts(project string) model.Project {
	if e.deps.Facts == nil {
		return model.Project{}
	}

	return e.deps.Facts(project)
}

// skillLine is the line that makes the agent of the runner load the skill before its first turn. It is empty when the runner has none, and for an engine whose deps have no SkillLine.
func (e *Engine) skillLine(runner, skill string) string {
	if e.deps.SkillLine == nil {
		return ""
	}

	return e.deps.SkillLine(runner, skill)
}

// registered returns the engine when the registration worked. When it failed, it shuts the engine down, so the folder lock and the store do not stay open.
func (e *Engine) registered(err error) (*Engine, error) {
	if err == nil {
		return e, nil
	}

	return nil, errors.Join(err, e.Shutdown())
}

// open opens the workflow store. The engine does not take the cancel of ctx.
func (e *Engine) open(ctx context.Context, opts Options, state store) error {
	flowCtx, err := dbos.NewContext(context.WithoutCancel(ctx), engineConfig(opts, state, e.logger))
	e.ctx = flowCtx

	return err
}

// engineConfig is the setup of the workflow engine for one state folder.
func engineConfig(opts Options, state store, logger *slog.Logger) dbos.Config {
	return dbos.Config{
		AppName:            appName,
		DatabaseURL:        state.url,
		Logger:             logger,
		ApplicationVersion: applicationVersion(opts.Version),
		ExecutorID:         executorName,
	}
}

// applicationVersion is the version that DBOS stores with each run: the build version and one version for each step module (moduleVersions).
func applicationVersion(build string) string { return build + "+" + moduleVersions() }

// splitVersion splits a stored application version into the build version that a user starts and the steps part: the list of module versions
// (such as core1.agent1) or the single version of the builtin steps that came before the modules (such as star13).
// A version with no steps part has an empty second value. This is the only place that reads the format of applicationVersion.
func splitVersion(stored string) (build, steps string) {
	at := strings.LastIndex(stored, "+")
	if at < 0 {
		return stored, ""
	}

	tail := stored[at+1:]
	if _, listed := parseVersions(tail); listed || strings.HasPrefix(tail, "star") || looksLikeModuleList(tail) {
		return stored[:at], tail
	}

	return stored, ""
}

// buildVersion is the version that a user starts: a stored version without the version of the builtin steps.
func buildVersion(stored string) string {
	build, _ := splitVersion(stored)

	return build
}

// stepListOf is the version of the builtin steps in a stored version, or an empty text when it holds none.
func stepListOf(stored string) string {
	_, steps := splitVersion(stored)

	return steps
}

// describeStepList names a step list version for a message: "no step list" when the stored version holds none.
func describeStepList(steps string) string {
	if steps == "" {
		return "no step list"
	}

	return "step list " + steps
}

// register adds the workflows, the agents queue (implement steps), the reviews queue (review steps) and the items queue of spawn.
// Each agent queue runs at most agents.per_login agents for each login, at most its own cap in all. The two caps do not lend slots to each other.
func (e *Engine) register(opts Options) error {
	maxConcurrent := opts.MaxConcurrent

	dbos.RegisterWorkflow(e.ctx, e.item, dbos.WithWorkflowName(itemName))
	dbos.RegisterWorkflow(e.ctx, e.phase, dbos.WithWorkflowName(phaseName))
	dbos.RegisterWorkflow(e.ctx, e.exclusive, dbos.WithWorkflowName(exclusiveName))
	dbos.RegisterWorkflow(e.ctx, e.hostJob, dbos.WithWorkflowName(hostJobName))
	dbos.RegisterWorkflow(e.ctx, e.poll, dbos.WithWorkflowName(pollName))

	// An adaptive engine registers the agents queue at the floor, so no burst runs before the controller writes the floor at Launch.
	agentsLimit := maxConcurrent
	if e.adaptive() {
		agentsLimit = e.perLogin
	}

	return errors.Join(
		e.registerQueue(queueName, agentsLimit, &e.queue), e.registerQueue(reviewsQueueName, opts.MaxReviewConcurrent, &e.reviewQueue),
		e.registerItemQueues(maxConcurrent, cmp.Or(opts.MaxSpawnDepth, config.DefaultMaxSpawnDepth)), e.registerExclusiveQueue(), e.registerHostQueue(),
	)
}

// deepestKept returns the depth of the children of the deepest item run that can run again: one that is open, or canceled or failed and so open to a resume.
// A project file that lowered max_spawn_depth must not leave such a run on a queue that no worker reads, and the replay of its spawn names the queue of its children.
func (e *Engine) deepestKept() (int, error) {
	// The depth comes from the input: a canceled row has lost its queue name.
	states := dbos.WithFilterStatus(dbos.WorkflowStatusPending, dbos.WorkflowStatusEnqueued, dbos.WorkflowStatusCancelled, dbos.WorkflowStatusError)

	rows, err := e.store().List(dbos.WithFilterName(itemName), states, dbos.WithFilterLoadInput(true))
	if err != nil {
		return 0, fmt.Errorf("read the item runs that can run again: %w", err)
	}

	deepest := 0

	// A run whose input does not decode cannot run again, so it needs no queue.
	for _, row := range rows {
		if in, err := (run{Status: row}).input(); err == nil {
			deepest = max(deepest, in.Depth+1)
		}
	}

	return deepest, nil
}

// registerQueue adds an agent queue with at most perLogin agents for each login and at most limit in all, and keeps it in into.
func (e *Engine) registerQueue(name string, limit int, into *dbos.Queue) error {
	options := []dbos.QueueOption{dbos.WithGlobalConcurrency(limit), dbos.WithPartitionConcurrency(e.perLogin)}
	if poll := e.deps.Clock.Poll; poll > 0 {
		options = append(options, dbos.WithQueueBasePollingInterval(poll))
	}

	queue, err := dbos.RegisterQueue(e.ctx, name, options...)
	if err != nil {
		return fmt.Errorf("register the %s queue: %w", name, err)
	}

	*into = queue

	return nil
}

// registerItemQueues adds one queue of item runs for each depth from 0 to depth, or to the deepest run that can run again when that is deeper,
// so that a parent that waits never holds a slot that its children need. A queue has no partition: the agents queue and the one run per login stay the real limit.
func (e *Engine) registerItemQueues(maxConcurrent, depth int) error {
	kept, err := e.deepestKept()
	if err != nil {
		return err
	}

	return e.registerDepths(maxConcurrent, max(depth, kept))
}

// registerDepths adds the items queues of the depths from 0 to deepest.
func (e *Engine) registerDepths(maxConcurrent, deepest int) error {
	options := []dbos.QueueOption{dbos.WithGlobalConcurrency(maxConcurrent)}
	if poll := e.deps.Clock.Poll; poll > 0 {
		options = append(options, dbos.WithQueueBasePollingInterval(poll))
	}

	e.itemQueues = make([]dbos.Queue, deepest+1)

	for level := range e.itemQueues {
		queue, err := dbos.RegisterQueue(e.ctx, itemsQueueOf(level), options...)
		if err != nil {
			return fmt.Errorf("register the items queue of depth %d: %w", level, err)
		}

		e.itemQueues[level] = queue
	}

	return nil
}

// Launch starts the workers and resumes the runs of this version that a crash cut short.
func (e *Engine) Launch() error {
	if err := e.reapLeft(); err != nil {
		return err
	}

	e.launched = time.Now()

	if err := e.sayResumedOff(); err != nil {
		return err
	}

	if err := e.launchAndSchedule(); err != nil {
		return err
	}

	e.tidy()

	return e.startController()
}

// sayResumedOff writes the off line for each open run that uses the sandbox mode off. Only the engine of beehave serve says it, once at its start,
// before it launches and resumes these runs. A run with no stored mode uses the live default of its project.
func (e *Engine) sayResumedOff() error {
	if !e.serving {
		return nil
	}

	open, err := e.store().Open("", dbos.WithFilterName(itemName), dbos.WithFilterLoadInput(true))
	if err != nil {
		return fmt.Errorf("list the open runs: %w", err)
	}

	for _, row := range open {
		if in, err := (run{Status: row}).input(); err == nil && in.UsedSandbox(e.deps.SandboxDefault) == model.SandboxOff {
			e.progress.say("run %s: %s", row.ID, model.SandboxOffLine)
		}
	}

	return nil
}

// launchAndSchedule launches the workflow engine, then sets the schedule of the review poll. A schedule that fails does not fail the launch.
func (e *Engine) launchAndSchedule() error {
	if err := dbos.Launch(e.ctx); err != nil {
		return fmt.Errorf("start the workflow engine: %w", err)
	}

	e.recordSchedule(e.setSchedule(e))

	return nil
}

// reapLeft stops the agent processes that a crashed worker left.
func (e *Engine) reapLeft() error {
	killed, err := e.procs.reap(e.ctx)
	if err != nil {
		return err
	}

	if killed > 0 {
		e.logger.Warn("stopped agent processes that a crashed worker left", "groups", killed)
	}

	return nil
}

// tidy applies the retention, finishes the cancels that a crash cut short, and starts the tick.
func (e *Engine) tidy() {
	e.retain()
	e.sweepCancels()

	e.tickRun.Go(func() { e.tick(tickInterval, e.tickEnd) })
}

// retain applies the retention. A store that cannot delete does not stop the engine: the next Launch tries again.
func (e *Engine) retain() {
	if err := e.deleteOldRuns(); err != nil {
		e.logger.Warn("cannot delete the finished runs", "error", err)
	}
}

// deleteOldRuns deletes the runs that finished more than the retention ago, with the phases they started.
// A failed or canceled run stays: beehave resume can reopen it.
func (e *Engine) deleteOldRuns() error {
	if e.keepRuns {
		return nil
	}

	ids, err := e.oldRuns()
	if err != nil || len(ids) == 0 {
		return err
	}

	return e.forgetDeleted(ids, dbos.DeleteWorkflows(e.ctx, ids, dbos.WithDeleteChildren()))
}

// forgetDeleted returns the error of a delete. When the delete worked, the files of the runs end with them: a new run that reuses the run id must not read a token file.
// A run that stays in the store keeps its files.
func (e *Engine) forgetDeleted(ids []string, deleteErr error) error {
	if deleteErr == nil {
		for _, id := range ids {
			e.forget(id)
		}
	}

	return deleteErr
}

// oldRuns lists the ids of the runs that finished with success more than the retention ago.
func (e *Engine) oldRuns() ([]string, error) {
	cutoff := e.deps.Clock.wall().Add(-cmp.Or(e.deps.Clock.Retain, e.retention, config.DefaultRetention))

	rows, err := e.store().List(dbos.WithFilterStatus(dbos.WorkflowStatusSuccess), dbos.WithFilterWorkflowIDPrefix(runPrefix),
		dbos.WithFilterName(itemName), dbos.WithFilterCompletedBefore(cutoff))
	if err != nil {
		return nil, fmt.Errorf("list the finished runs: %w", err)
	}

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}

	return ids, nil
}

// RunFile takes one item to the end of its workflow file and returns the result. It joins the run of the item when one is still open.
// The item belongs to the project from this call on; an open run under another project is refused.
// The run pins the SHA-256 of the file, and a replay refuses a changed file.
func (e *Engine) RunFile(item, project, file string, options ...RunOption) (ItemResult, error) {
	return e.runEntry(item, project, file, "", options...)
}

// runEntry runs the pinned file for the item at the named entry. An empty entry runs main.
func (e *Engine) runEntry(item, project, file, entry string, options ...RunOption) (ItemResult, error) {
	in, err := pinnedInput(item, project, file, options...)
	if err != nil {
		return ItemResult{}, err
	}

	in.Entry = entry

	return e.run(in)
}

// pinnedInput reads the workflow file once and returns the input of a run that executes only those bytes, with the options applied.
func pinnedInput(item, project, file string, options ...RunOption) (ItemInput, error) {
	abs, absErr := filepath.Abs(file)

	pinned, pinErr := workflow.Pin(abs)
	if err := errors.Join(pinErr, absErr); err != nil {
		return ItemInput{}, err
	}

	in := ItemInput{Item: item, Project: project, File: abs, Files: pinned}
	for _, option := range options {
		option(&in)
	}

	return in, nil
}

// check refuses an item that is not an item id, a missing project, and a run with no sandbox mode.
func (in ItemInput) check() error {
	if model.ItemKind(in.Item) == "" {
		return fmt.Errorf("%w %q: use an item id such as 12 or fix-login", ErrBadItem, in.Item)
	}

	if in.Project == "" {
		return fmt.Errorf("%w %s: name the project", errNoProject, in.Item)
	}

	if in.Sandbox == "" {
		return fmt.Errorf("%w: item %s was not started. The run must store sandbox on or off", errNoSandboxMode, in.Item)
	}

	return nil
}

func (e *Engine) run(in ItemInput) (ItemResult, error) {
	if err := in.check(); err != nil {
		return ItemResult{}, err
	}

	handle, err := e.joinOrStart(in)
	if err != nil {
		return ItemResult{}, err
	}

	return e.result(handle, in.Item)
}

// joinOrStart joins or starts the run of the item while it holds off the shutdown. After the shutdown began, it starts nothing.
func (e *Engine) joinOrStart(in ItemInput) (dbos.WorkflowHandle[ItemResult], error) {
	e.starts.RLock()
	defer e.starts.RUnlock()

	if e.stopped {
		return nil, fmt.Errorf("%w: item %s was not started: run beehave run %s again", errEngineShutDown, in.Item, in.Item)
	}

	return e.handleFor(in)
}

// result waits for the run and returns its result.
func (e *Engine) result(handle dbos.WorkflowHandle[ItemResult], item string) (ItemResult, error) {
	result, err := handle.GetResult(e.deps.Clock.resultOptions()...)
	if err != nil {
		// The library selects between the outcome and the stop of the engine, so a stop that lands in the instant the run completes hides the result.
		if finished, ok := e.finishedResult(item); ok {
			return finished, nil
		}

		return ItemResult{}, fmt.Errorf("item %s: %w", item, err)
	}

	return result, nil
}

// finishedResult reads the result of the latest run of the item once from the store. ok is false when the run did not end with success.
// The read ignores the stop of the engine: the store is still open while the stopped runs drain.
func (e *Engine) finishedResult(item string) (ItemResult, bool) {
	last, found, err := latestRun(viewOf(dbos.WithoutCancel(e.ctx), e.dir), item)
	if err != nil || !found || last.Status.Status != dbos.WorkflowStatusSuccess {
		return ItemResult{}, false
	}

	var result ItemResult

	err = json.Unmarshal([]byte(fmt.Sprint(last.Status.Output)), &result) // WithFilterLoadOutput returns the stored JSON as a string

	return result, err == nil
}

// handleFor joins the open run of the item, or starts the next attempt.
// A new attempt starts only after a finished or a canceled one: a failed attempt continues with beehave resume.
// A new attempt picks its login here, live, before the workflow exists: a pool with no usable login stores no run.
// The login goes in the input, so a resume or a replay keeps it.
func (e *Engine) handleFor(in ItemInput) (dbos.WorkflowHandle[ItemResult], error) {
	last, found, err := latestRun(e.store(), in.Item)
	if err != nil {
		return nil, err
	}

	plan, err := planOf(last, found, in, e.version, found && e.resumedByLaunch(last))
	if err != nil {
		return nil, err
	}

	return e.startOrJoin(plan, in, last, found)
}

// startOrJoin does what the plan says: it moves the run of another build to this build and joins it, joins the open run, or starts the next attempt.
func (e *Engine) startOrJoin(plan attemptPlan, in ItemInput, last run, found bool) (dbos.WorkflowHandle[ItemResult], error) {
	if err := e.asClient().moveIfOtherBuild(e.ctx, plan, in.Item); err != nil {
		return nil, err
	}

	if plan.Join != "" {
		return retrieve[ItemResult](e.store(), plan.Join)
	}

	in, err := in.withBaseOf(last, found)
	if err != nil {
		return nil, err
	}

	return e.start(in, plan.Attempt)
}

// asClient returns a client over the store of this engine, so that a move of a run to this build runs the code of beehave resume --on-this-build.
func (e *Engine) asClient() *Client {
	give := ReleaseDeps{Lifecycle: e.deps.Lifecycle, Beats: e.deps.Beats, Trees: e.deps.Trees, Clock: e.deps.Clock, Repository: e.deps.Repository, Merge: e.deps.Merge}

	return &Client{flow: e.ctx, version: e.version, dir: e.dir, give: give, logger: e.logger, redact: e.redact}
}

// resumedByLaunch reports whether the Launch of this engine resumed the run and the run ended since.
// The caller asked for the item while the run was open, so the result of that run answers the call: a second attempt would do the work twice.
func (e *Engine) resumedByLaunch(last run) bool {
	return last.Status.Status == dbos.WorkflowStatusSuccess && last.Status.CreatedAt.Before(e.launched) && !last.Status.CompletedAt.Before(e.launched)
}

// start starts the attempt. It reads no usage: the run picks its login at its first agent step.
func (e *Engine) start(in ItemInput, attempt int) (dbos.WorkflowHandle[ItemResult], error) {
	return dbos.RunWorkflow(e.ctx, e.item, in, dbos.WithWorkflowID(runID(in.Item, attempt)))
}

// pickLogin reads the pool once. It refuses an empty pick.
// The pick prefers a login that has no agent step running or queued. taken lists the logins that the caller picked for earlier runs that it has not started yet.
// overCap lets the pick take a runner that is over a cap.
func (e *Engine) pickLogin(ctx context.Context, in ItemInput, overCap bool, taken ...string) (string, error) {
	busy, err := e.busyLogins(taken)
	if err != nil {
		return "", fmt.Errorf("read the busy logins for item %s: %w", in.Item, err)
	}

	login, err := e.deps.Logins.Pick(ctx, in.Project, in.Item, busy, overCap)
	if err != nil {
		return "", fmt.Errorf("pick the login of item %s: %w", in.Item, err)
	}

	if login == "" {
		return "", errEmptyLogin
	}

	return login, nil
}

// busyLogins returns the logins that have agents.per_login agent steps running or queued, counting the logins in taken. The library lists the open workflows, and beehave counts the agents of each login in that list.
// An agent step is an open phase, which has the login as its partition key on the agents queue. A run that a spawn or the queue started holds its login from its start,
// because its agent step is not queued yet while the run waits in a slot or does other steps. A run that `beehave run` started holds none, so the loop parent needs no login of its own.
// The engine registers only the run and the phase, so one listing of the open workflows, filtered to those two names, reads both.
func (e *Engine) busyLogins(taken []string) ([]string, error) {
	open, err := e.store().Open("", dbos.WithFilterName(itemName, phaseName), dbos.WithFilterLoadInput(true))
	if err != nil {
		return nil, fmt.Errorf("list the open workflows: %w", err)
	}

	return heldLogins(open, taken, e.perLogin)
}

// heldLogins returns the logins that hold limit agents or more, sorted. The open workflows in rows and the logins in taken each hold one agent.
// A phase is a child of its run: a run that has an open phase holds its login through the phase and counts once.
func heldLogins(rows []dbos.WorkflowStatus, taken []string, limit int) ([]string, error) {
	phased := runsWithAPhase(rows)
	held := countNames(taken)

	for _, row := range rows {
		login, err := heldLogin(row)
		if err != nil {
			return nil, err
		}

		if !phased[row.ID] {
			held[login]++
		}
	}

	delete(held, "")

	return atLeast(held, limit), nil
}

// countNames counts each name in names.
func countNames(names []string) map[string]int {
	counts := map[string]int{}

	for _, name := range names {
		counts[name]++
	}

	return counts
}

// runsWithAPhase returns the ids of the runs that have an open phase in rows.
func runsWithAPhase(rows []dbos.WorkflowStatus) map[string]bool {
	phased := map[string]bool{}

	for _, row := range rows {
		if row.Name == phaseName && row.ParentWorkflowID != "" {
			phased[row.ParentWorkflowID] = true
		}
	}

	return phased
}

// atLeast returns the keys of counts whose count reaches limit, sorted.
func atLeast(counts map[string]int, limit int) []string {
	busy := []string{}

	for key, count := range counts {
		if count >= limit {
			busy = append(busy, key)
		}
	}

	slices.Sort(busy)

	return busy
}

// heldLogin returns the login that an open workflow holds, or "" when it holds none. A phase holds its login. A run holds it only when a spawn or the queue started it: its input has an entry.
// A run that a user started has no entry and holds no login, so its login reads free between two phases. The library returns the stored JSON of the input as a string.
func heldLogin(row dbos.WorkflowStatus) (string, error) {
	var in struct {
		Login string `json:"login"`
		Entry string `json:"entry"`
	}

	if err := json.Unmarshal([]byte(fmt.Sprint(row.Input)), &in); err != nil {
		return "", fmt.Errorf("read the input of workflow %s: %w", row.ID, err)
	}

	if row.Name != phaseName && in.Entry == "" {
		return "", nil
	}

	return in.Login, nil
}

// Shutdown stops the workers and closes the log. Runs that are not finished resume at the next Launch.
// A second call returns the result of the first.
func (e *Engine) Shutdown() error {
	e.down.Do(func() { e.downErr = e.shutdown() })

	return e.downErr
}

func (e *Engine) shutdown() error {
	e.refuseStarts()
	close(e.tickEnd)
	e.tickRun.Wait()

	stopped := make(chan error, 1)

	go func() { stopped <- dbos.Shutdown(e.ctx, shutdownTimeout) }()

	// dbos.Shutdown cancels the runs first and then waits for them. The agents die only after the cancel:
	// an agent that dies before it would end its phase as failed, and the run would not stay open.
	<-e.ctx.Done()

	killed, waited := e.procs.killAll(), <-stopped

	e.holds.clear()

	return errors.Join(killed, waited, e.log.Close(), removeEngineFiles(e.dir), e.lock.Unlock())
}

// refuseStarts waits for each start in flight to hand its run to the library, and makes every later start fail.
func (e *Engine) refuseStarts() {
	e.starts.Lock()
	defer e.starts.Unlock()

	e.stopped = true
}
