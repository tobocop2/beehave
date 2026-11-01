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

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

const (
	appName      = "obee"
	executorName = "local"
	queueName    = "agents"
	// reviewsQueueName is the queue of the agent steps of the kind review. The agents queue holds the steps of the kind implement.
	reviewsQueueName = "reviews"
	itemName         = "item"
	treeName         = "tree"
	phaseName        = "phase"
	ownerTopic       = "owner"
	databaseFile     = "dbos.db"
	logFileName      = "flow.log"
	logFileMode      = 0o600
	stateScheme      = "sqlite:"

	shutdownTimeout  = 10 * time.Second
	partitionLimit   = 1
	ownerWaitLimit   = 7 * 24 * time.Hour
	retention        = 7 * 24 * time.Hour
	beatLimit        = 30 * time.Second
	minMaxConcurrent = 1
)

// errBadOptions is returned when the options of the engine or the client are not usable.
var errBadOptions = errors.New("invalid flow options")

// Options holds the settings the engine and the client share.
// Version is the build version that pins each run.
// MaxConcurrent is agents.max_concurrent, the cap of the implement queue. MaxReviewConcurrent is agents.max_review_concurrent, the cap of the review queue.
type Options struct {
	StateDir            string
	Version             string
	MaxConcurrent       int
	MaxReviewConcurrent int
	Env                 map[string]string
	// ReviewPoll is the time between two review polls. 0 means no poll.
	ReviewPoll time.Duration
	// OverCap lets the first agent step of a run take a runner that is over its usage cap when no runner has room. The step records that it did.
	OverCap bool
	// Progress receives one line when a run starts and one when each top-level step of a run starts and ends. A nil Progress prints nothing.
	Progress io.Writer
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
	case o.ReviewPoll < 0:
		return fmt.Errorf("%w: the review poll interval is %s: set it to 0 or more", errBadOptions, o.ReviewPoll)
	}

	return nil
}

// store is the state folder a process opened: the database address and the log.
type store struct {
	dir string
	url string
	log *os.File
}

// openStore creates the state folder and opens the log file at mode 0600.
func openStore(opts Options) (store, error) {
	if err := opts.validate(); err != nil {
		return store{}, err
	}

	if err := config.EnsureStateDir(opts.StateDir); err != nil {
		return store{}, err
	}

	log, err := os.OpenFile(filepath.Join(opts.StateDir, logFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, logFileMode)
	if err != nil {
		return store{}, fmt.Errorf("open the log file: %w", err)
	}

	return store{dir: opts.StateDir, url: stateScheme + filepath.Join(opts.StateDir, databaseFile), log: log}, nil
}

// Engine runs the item workflows. Call NewEngine, then Launch, then Run for each item, then Shutdown.
type Engine struct {
	ctx dbos.Context
	// queue is the agents queue: the steps of the kind implement.
	queue dbos.Queue
	// reviewQueue is the queue of the steps of the kind review.
	reviewQueue dbos.Queue
	// itemQueue is the queue of the runs that spawn starts.
	itemQueue dbos.Queue
	// exclusiveQueue is the queue of the gates and the merges: one run for each repository.
	exclusiveQueue dbos.Queue
	deps           Deps
	// maxConcurrent is agents.max_concurrent, the cap of the items queue. The queue builtin subtracts the open runs from it.
	maxConcurrent int
	// reviewPoll is the time between two review polls. 0 means no poll.
	reviewPoll time.Duration
	// overCap is Options.OverCap.
	overCap bool
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
	down     sync.Once
	downErr  error
}

// NewEngine opens the state folder and registers the workflows and the agents queue. Nothing runs until Launch.
// One engine owns a state folder at a time: a second NewEngine on the same folder fails until Shutdown.
// A Ctrl-C of the caller must not end the runs, so the engine does not take the cancel of ctx: Shutdown stops it and leaves the runs open.
func NewEngine(ctx context.Context, opts Options, deps Deps) (*Engine, error) {
	state, err := openStore(opts)
	if err != nil {
		return nil, err
	}

	held, err := holdEngine(state.dir)
	if err != nil {
		return nil, errors.Join(err, state.log.Close())
	}

	engine := newEngine(opts, deps, state, held)

	if err := engine.open(ctx, opts, state); err != nil {
		return nil, errors.Join(fmt.Errorf("open the workflow store: %w", err), held.Unlock(), state.log.Close())
	}

	return engine.registered(engine.register(opts))
}

// newEngine makes the engine value for an opened store and a held folder lock.
func newEngine(opts Options, deps Deps, state store, held *flock.Flock) *Engine {
	engine := &Engine{
		deps: deps, maxConcurrent: opts.MaxConcurrent, reviewPoll: opts.ReviewPoll, overCap: opts.OverCap, setSchedule: (*Engine).schedulePoll, env: opts.Env, version: applicationVersion(opts.Version), dir: state.dir, log: state.log, lock: held, logger: newLogger(state.log), redact: agent.Redactor(),
		procs: &groups{path: groupsFile(opts.StateDir)}, tickEnd: make(chan struct{}), progress: &progress{out: opts.Progress},
	}
	engine.progress.redact = engine.redact

	return engine
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

// applicationVersion is the version that DBOS stores with each run: the build version and the version of the builtin steps.
func applicationVersion(build string) string { return build + "+" + stepsVersion }

// stepsMark starts the version of the builtin steps in a stored application version.
const stepsMark = "+star"

// splitVersion splits a stored application version into the build version that a user starts and the version of the builtin steps (such as star13).
// A version with no step list part has an empty second value. This is the only place that reads the format of applicationVersion.
func splitVersion(stored string) (build, steps string) {
	at := strings.LastIndex(stored, stepsMark)
	if at < 0 {
		return stored, ""
	}

	return stored[:at], stored[at+1:]
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
// Each agent queue runs one agent per login, at most its own cap in all. The two caps do not lend slots to each other.
func (e *Engine) register(opts Options) error {
	maxConcurrent := opts.MaxConcurrent

	dbos.RegisterWorkflow(e.ctx, e.item, dbos.WithWorkflowName(itemName))
	dbos.RegisterWorkflow(e.ctx, e.phase, dbos.WithWorkflowName(phaseName))
	dbos.RegisterWorkflow(e.ctx, e.exclusive, dbos.WithWorkflowName(exclusiveName))
	dbos.RegisterWorkflow(e.ctx, e.poll, dbos.WithWorkflowName(pollName))

	return errors.Join(
		e.registerQueue(queueName, maxConcurrent, &e.queue), e.registerQueue(reviewsQueueName, opts.MaxReviewConcurrent, &e.reviewQueue),
		e.registerItemQueue(maxConcurrent), e.registerExclusiveQueue(),
	)
}

// registerQueue adds an agent queue with one agent per login and at most limit in all, and keeps it in into.
func (e *Engine) registerQueue(name string, limit int, into *dbos.Queue) error {
	options := []dbos.QueueOption{dbos.WithGlobalConcurrency(limit), dbos.WithPartitionConcurrency(partitionLimit)}
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

// registerItemQueue adds the queue of the runs that spawn starts. It has no partition: the agents queue and the one run per login stay the real limit.
func (e *Engine) registerItemQueue(maxConcurrent int) error {
	options := []dbos.QueueOption{dbos.WithGlobalConcurrency(maxConcurrent)}
	if poll := e.deps.Clock.Poll; poll > 0 {
		options = append(options, dbos.WithQueueBasePollingInterval(poll))
	}

	queue, err := dbos.RegisterQueue(e.ctx, itemsQueueName, options...)
	if err != nil {
		return fmt.Errorf("register the items queue: %w", err)
	}

	e.itemQueue = queue

	return nil
}

// Launch starts the workers and resumes the runs of this version that a crash cut short.
func (e *Engine) Launch() error {
	if err := e.reapLeft(); err != nil {
		return err
	}

	e.launched = time.Now()

	if err := e.launchAndSchedule(); err != nil {
		return err
	}

	e.tidy()

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
// A failed or canceled run stays: obee resume can reopen it.
func (e *Engine) deleteOldRuns() error {
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
	cutoff := time.Now().Add(-cmp.Or(e.deps.Clock.Retain, retention))

	rows, err := dbos.ListWorkflows(e.ctx, dbos.WithFilterStatus(dbos.WorkflowStatusSuccess), dbos.WithFilterWorkflowIDPrefix(runPrefix),
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
func (e *Engine) RunFile(item, project, file string) (ItemResult, error) {
	abs, absErr := filepath.Abs(file)

	pinned, pinErr := workflow.Pin(abs)
	if err := errors.Join(pinErr, absErr); err != nil {
		return ItemResult{}, err
	}

	return e.run(ItemInput{Item: item, Project: project, File: abs, Files: pinned})
}

// check refuses an item that is not an item id, and a missing project.
func (in ItemInput) check() error {
	if model.ItemKind(in.Item) == "" {
		return fmt.Errorf("%w %q: use an item id such as 12 or fix-login", ErrBadItem, in.Item)
	}

	if in.Project == "" {
		return fmt.Errorf("%w %s: name the project", errNoProject, in.Item)
	}

	return nil
}

func (e *Engine) run(in ItemInput) (ItemResult, error) {
	if err := in.check(); err != nil {
		return ItemResult{}, err
	}

	handle, err := e.handleFor(in)
	if err != nil {
		return ItemResult{}, err
	}

	return e.result(handle, in.Item)
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
	ctx := dbos.WithoutCancel(e.ctx)

	last, found, err := latestRun(ctx, item)
	if err != nil || !found || last.Status.Status != dbos.WorkflowStatusSuccess {
		return ItemResult{}, false
	}

	var result ItemResult

	err = json.Unmarshal([]byte(fmt.Sprint(last.Status.Output)), &result) // WithFilterLoadOutput returns the stored JSON as a string

	return result, err == nil
}

// handleFor joins the open run of the item, or starts the next attempt.
// A new attempt starts only after a finished or a canceled one: a failed attempt continues with obee resume.
// A new attempt picks its login here, live, before the workflow exists: a pool with no usable login stores no run.
// The login goes in the input, so a resume or a replay keeps it.
func (e *Engine) handleFor(in ItemInput) (dbos.WorkflowHandle[ItemResult], error) {
	item, project := in.Item, in.Project

	last, found, err := latestRun(e.ctx, item)
	if err != nil {
		return nil, err
	}

	if found && !last.terminal() {
		return e.join(last, item, project)
	}

	if found && e.resumedByLaunch(last) {
		return dbos.RetrieveWorkflow[ItemResult](e.ctx, last.Status.ID)
	}

	if found && last.failed() {
		return nil, failedError(last, item)
	}

	return e.start(in, last.Attempt+1)
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

// busyLogins returns the logins that have an agent step running or queued, and the logins in taken. The library keeps the list: obee counts nothing.
// An agent step is an open phase, which has the login as its partition key on the agents queue. A run that a spawn or the queue started holds its login from its start,
// because its agent step is not queued yet while the run waits in a slot or does other steps. A run that `obee run` started holds none, so the loop parent needs no login of its own.
// The engine registers only the run and the phase, so one listing of the open workflows, filtered to those two names, reads both.
func (e *Engine) busyLogins(taken []string) ([]string, error) {
	open, err := dbos.ListWorkflows(e.ctx, dbos.WithFilterStatus(dbos.WorkflowStatusPending, dbos.WorkflowStatusEnqueued), dbos.WithFilterName(itemName, phaseName), dbos.WithFilterLoadInput(true))
	if err != nil {
		return nil, fmt.Errorf("list the open workflows: %w", err)
	}

	return heldLogins(open, taken)
}

// heldLogins returns the logins that the open workflows in rows hold, and the logins in taken, sorted and without repeats.
func heldLogins(rows []dbos.WorkflowStatus, taken []string) ([]string, error) {
	busy := slices.Clone(taken)

	for _, row := range rows {
		login, err := heldLogin(row)
		if err != nil {
			return nil, err
		}

		if login != "" {
			busy = append(busy, login)
		}
	}

	slices.Sort(busy)

	return slices.Compact(busy), nil
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

// join returns the handle of an open run. It refuses a run of another project or another version.
func (e *Engine) join(open run, item, project string) (dbos.WorkflowHandle[ItemResult], error) {
	held, err := open.project()
	if err != nil {
		return nil, err
	}

	if held != project {
		return nil, conflictError(item, held, project)
	}

	if open.Status.ApplicationVersion != e.version {
		return nil, strandedError(open, e.version)
	}

	return dbos.RetrieveWorkflow[ItemResult](e.ctx, open.Status.ID)
}

// Shutdown stops the workers and closes the log. Runs that are not finished resume at the next Launch.
// A second call returns the result of the first.
func (e *Engine) Shutdown() error {
	e.down.Do(func() { e.downErr = e.shutdown() })

	return e.downErr
}

func (e *Engine) shutdown() error {
	close(e.tickEnd)
	e.tickRun.Wait()

	stopped := make(chan error, 1)

	go func() { stopped <- dbos.Shutdown(e.ctx, shutdownTimeout) }()

	// dbos.Shutdown cancels the runs first and then waits for them. The agents die only after the cancel:
	// an agent that dies before it would end its phase as failed, and the run would not stay open.
	<-e.ctx.Done()

	killed, waited := e.procs.killAll(), <-stopped

	e.holds.clear()

	return errors.Join(killed, waited, e.log.Close(), e.lock.Unlock())
}
