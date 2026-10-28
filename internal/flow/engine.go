package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	// The sqlite driver registers itself with the workflow store.
	_ "github.com/dbos-inc/dbos-transact-golang/dbos/driver/sqlite"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/filelock"
	"github.com/tobocop2/obee/internal/model"
)

const (
	appName      = "obee"
	executorName = "local"
	queueName    = "agents"
	itemName     = "item"
	phaseName    = "phase"
	ownerTopic   = "owner"
	databaseFile = "dbos.db"
	logFileName  = "flow.log"
	logFileMode  = 0o600
	stateScheme  = "sqlite:"

	shutdownTimeout  = 10 * time.Second
	partitionLimit   = 1
	ownerWaitLimit   = 7 * 24 * time.Hour
	retention        = 7 * 24 * time.Hour
	beatLimit        = 30 * time.Second
	claimLease       = 5 * time.Minute
	minMaxConcurrent = 1
)

// errBadOptions is returned when the options of the engine or the client are not usable.
var errBadOptions = errors.New("invalid flow options")

// Options holds the settings the engine and the client share.
// Version is the build version that pins each run. MaxConcurrent is agents.max_concurrent.
type Options struct {
	StateDir      string
	Version       string
	MaxConcurrent int
	Env           map[string]string
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
	ctx     dbos.Context
	queue   dbos.Queue
	deps    Deps
	version string
	dir     string
	log     io.Closer
	lock    *filelock.Lock
	logger  *slog.Logger
	redact  func(string) string
	cancels cancels
	holds   holds
	procs   *groups
	tickEnd chan struct{}
	tickRun sync.WaitGroup
	down    sync.Once
	downErr error
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

	engine := &Engine{
		deps: deps, version: opts.Version, dir: state.dir, log: state.log, lock: held, logger: newLogger(state.log), redact: agent.Redactor(),
		procs: &groups{path: groupsFile(opts.StateDir)}, tickEnd: make(chan struct{}),
	}

	if err := engine.open(ctx, opts, state); err != nil {
		return nil, errors.Join(fmt.Errorf("open the workflow store: %w", err), held.Release(), state.log.Close())
	}

	return engine.registered(engine.register(opts.MaxConcurrent))
}

// registered returns the engine when the registration worked. When it failed, it shuts the engine down, so the folder lock and the store do not stay open.
func (e *Engine) registered(err error) (*Engine, error) {
	if err == nil {
		return e, nil
	}

	return nil, errors.Join(err, e.Shutdown())
}

// open opens the workflow store under the migrate lock. The engine does not take the cancel of ctx.
func (e *Engine) open(ctx context.Context, opts Options, state store) error {
	return migrating(state.dir, func() error {
		flowCtx, err := dbos.NewContext(context.WithoutCancel(ctx), engineConfig(opts, state, e.logger))
		e.ctx = flowCtx

		return err
	})
}

// engineConfig is the setup of the workflow engine for one state folder.
func engineConfig(opts Options, state store, logger *slog.Logger) dbos.Config {
	return dbos.Config{
		AppName:            appName,
		DatabaseURL:        state.url,
		Logger:             logger,
		ApplicationVersion: opts.Version,
		ExecutorID:         executorName,
	}
}

// register adds the workflows and the agents queue: one agent per login, at most maxConcurrent in all.
func (e *Engine) register(maxConcurrent int) error {
	dbos.RegisterWorkflow(e.ctx, e.item, dbos.WithWorkflowName(itemName))
	dbos.RegisterWorkflow(e.ctx, e.phase, dbos.WithWorkflowName(phaseName))

	return e.registerQueue(maxConcurrent)
}

func (e *Engine) registerQueue(maxConcurrent int) error {
	options := []dbos.QueueOption{dbos.WithGlobalConcurrency(maxConcurrent), dbos.WithPartitionConcurrency(partitionLimit)}
	if poll := e.deps.Clock.Poll; poll > 0 {
		options = append(options, dbos.WithQueueBasePollingInterval(poll))
	}

	queue, err := dbos.RegisterQueue(e.ctx, queueName, options...)
	if err != nil {
		return fmt.Errorf("register the agents queue: %w", err)
	}

	e.queue = queue

	return nil
}

// Launch starts the workers and resumes the runs of this version that a crash cut short.
func (e *Engine) Launch() error {
	killed, err := e.procs.reap(e.ctx)
	if err != nil {
		return err
	}

	if killed > 0 {
		e.logger.Warn("stopped agent processes that a crashed worker left", "groups", killed)
	}

	if err := dbos.Launch(e.ctx); err != nil {
		return fmt.Errorf("start the workflow engine: %w", err)
	}

	e.retain()
	e.sweepCancels()

	e.tickRun.Go(func() { e.tick(tickInterval, e.tickEnd) })

	return nil
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

// Run takes one item to the end of its workflow and returns the result. It joins the run of the item when one is still open.
// The item belongs to the project from this call on; an open run under another project is refused.
func (e *Engine) Run(item, project string) (ItemResult, error) {
	if model.ItemKind(item) == "" {
		return ItemResult{}, fmt.Errorf("%w %q: use a forge number or a beads id", ErrBadItem, item)
	}

	if project == "" {
		return ItemResult{}, fmt.Errorf("%w %s: name the project", errNoProject, item)
	}

	handle, err := e.handleFor(item, project)
	if err != nil {
		return ItemResult{}, err
	}

	result, err := handle.GetResult(e.deps.Clock.resultOptions()...)
	if err != nil {
		return ItemResult{}, fmt.Errorf("item %s: %w", item, err)
	}

	return result, nil
}

// handleFor joins the open run of the item, or starts the next attempt.
// A new attempt starts only after a finished or a canceled one: a failed attempt continues with obee resume.
func (e *Engine) handleFor(item, project string) (dbos.WorkflowHandle[ItemResult], error) {
	last, found, err := latestRun(e.ctx, item)
	if err != nil {
		return nil, err
	}

	if found && !last.terminal() {
		return e.join(last, item, project)
	}

	if found && last.failed() {
		return nil, failedError(last, item)
	}

	// The login is checked before the run exists, so a missing runner claims nothing and stores no failed attempt.
	if err := e.checkLogin(item, project); err != nil {
		return nil, err
	}

	return dbos.RunWorkflow(e.ctx, e.item, ItemInput{Item: item, Project: project}, dbos.WithWorkflowID(runID(item, last.Attempt+1)))
}

// checkLogin refuses an item that has no agent login.
func (e *Engine) checkLogin(item, project string) error {
	login, err := e.deps.Logins.Pick(e.ctx, project, item)
	if err != nil {
		return fmt.Errorf("pick the login of item %s: %w", item, err)
	}

	if login == "" {
		return errEmptyLogin
	}

	return nil
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

	return errors.Join(e.procs.killAll(), <-stopped, e.log.Close(), e.lock.Release())
}
