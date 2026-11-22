package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// jsonModule is the one name of workflow.Builtins that is a module of the Starlark library, not a step.
const jsonModule = "json"

// stepsVersion is the single version of the builtin steps that came before the step modules. A run stored under it recorded every module at version 1
// (parseVersions), and the golden tables of this package are held under it. A change to a builtin changes the version of its module in moduleTable.
// The application version holds those module versions, so an old run is not replayed by a build that changed a module that the run used.
const stepsVersion = "star30"

// runStar runs `main(item)` of the workflow file of the run on one thread. The files are the bytes that the run pinned.
// Each builtin does its work in DBOS steps, so a replay of this function makes the same calls and gets the recorded results.
// treeStart is the commit that the recorded step made the worktree of a round item at, and empty for any other item.
func (e *Engine) runStar(ctx dbos.Context, in ItemInput, login, treeStart string) (ItemResult, error) {
	sources, err := workflow.Open(in.File, in.Files)
	if err != nil {
		return ItemResult{}, err
	}

	e.progress.say("run %s workflow %s item %s", workflowID(ctx), in.File, in.Item)

	fields, err := runStep(e, ctx, "item", func(c context.Context) (map[string]string, error) {
		return e.itemFields(c, in.Project, in.Item)
	})
	if err != nil {
		return ItemResult{}, fmt.Errorf("read the fields of item %s: %w", in.Item, err)
	}

	return e.playScript(&script{e: e, ctx: ctx, in: in, login: login, treeStart: treeStart, severity: severityOfItem(fields), counts: map[string]int{}}, sources, fields)
}

// itemFields reads the fields of an item from the tracker. A round item has no tracker issue, so it gets its id as its only field and no read.
func (e *Engine) itemFields(ctx context.Context, project, item string) (map[string]string, error) {
	if model.ItemKind(item) == model.KindRound {
		return map[string]string{"id": item}, nil
	}

	return e.deps.Items.Fields(ctx, project, item)
}

// playScript runs main, then the finish of the workflow file, and returns what the item came to.
func (e *Engine) playScript(s *script, sources *workflow.Sources, fields map[string]string) (ItemResult, error) {
	err := s.end(s.main(e.thread(s.in.Item), sources, fields))

	// A failed run undoes its finished writes. A stop, a cancel and a success undo nothing.
	if err != nil && s.ctx.Err() == nil {
		undone, undoErr := s.undoAll()
		if len(undone) > 0 {
			err = fmt.Errorf("%w (undone: %s)", err, strings.Join(undone, ", "))
		}

		err = errors.Join(err, undoErr)
	}

	if finishErr := s.finish(e.thread(s.in.Item), sources, fields); finishErr != nil {
		err = errors.Join(err, finishErr)
	}

	return ItemResult{Item: s.in.Item, Stopped: s.stopped, Value: s.value}, err
}

// thread makes the thread of a run. It logs print at the debug level and stops after model.StarlarkMaxSteps instructions.
func (e *Engine) thread(item string) *starlark.Thread {
	thread := &starlark.Thread{Name: item, Print: func(_ *starlark.Thread, msg string) { e.logger.Debug(msg) }}
	thread.SetMaxExecutionSteps(model.StarlarkMaxSteps)

	return thread
}

// end turns the error of main into the error of the run: a stop of the context, no error for a command that stopped the item, or an error with its position.
func (s *script) end(err error) error {
	switch {
	case s.ctx.Err() != nil:
		return stopError(s.ctx)
	case errors.Is(err, errStopped):
		return nil
	}

	return where(err)
}

// main loads the file and calls its entry with the item: the function main, or the entry that the spawn call named for an item that spawn started.
func (s *script) main(thread *starlark.Thread, sources *workflow.Sources, fields map[string]string) error {
	s.sources = sources

	if err := s.holdAtStart(); err != nil {
		return err
	}

	main, err := sources.Entry(thread, s.predeclared(), cmp.Or(s.in.Entry, defaultEntry))
	if err != nil {
		return err
	}

	returned, err := starlark.Call(thread, main, starlark.Tuple{itemArg(fields)}, nil)
	if err != nil {
		return err
	}

	s.value, err = returnedValue(cmp.Or(s.in.Entry, defaultEntry), returned)
	if s.in.Entry == "" {
		// A run that a user started keeps the value that its main returns, so the queue shows it. A value that cannot be kept is dropped and never fails the run.
		return nil
	}

	return err
}

// finish calls the optional function finish of the workflow file after main, on every end of the run except a cancel.
// It runs for an item that a user started, not for an item that spawn started. A file with no finish, and a canceled run, call nothing.
// The error that it returns is its own: the caller keeps the error of main next to it.
func (s *script) finish(thread *starlark.Thread, sources *workflow.Sources, fields map[string]string) error {
	if s.ctx.Err() != nil || s.in.Entry != "" || !sources.Defines(finishEntry) {
		return nil
	}

	finish, err := sources.Entry(thread, s.predeclared(), finishEntry)
	if err == nil {
		_, err = starlark.Call(thread, finish, starlark.Tuple{itemArg(fields)}, nil)
	}

	if err = s.end(err); err != nil {
		return fmt.Errorf("%s: %w", finishEntry, err)
	}

	return nil
}

// itemArg builds the argument of main and finish from the fields of the item.
func itemArg(fields map[string]string) *starlarkstruct.Struct {
	item := starlark.StringDict{}
	for name, value := range fields {
		item[name] = starlark.String(value)
	}

	arg := starlarkstruct.FromStringDict(starlarkstruct.Default, item)
	arg.Freeze()

	return arg
}

// hasRepository reports whether the project of the run has a git repository.
func (s *script) hasRepository() bool {
	return s.e.hasRepository(s.in.Project)
}

// tree returns the worktree of the item and makes it when it is missing. A round item gets it at the commit that the recorded step made it at.
// A child item of a round gets it cut at the base of the round.
func (s *script) tree(ctx context.Context) (string, error) {
	return s.e.deps.Trees.Ensure(ctx, s.in.Project, s.in.Item, s.treeStart, s.in.Base)
}

// hasRepository reports whether the project has a repository. A nil Deps.Repository means every project has one.
func (e *Engine) hasRepository(project string) bool {
	return e.deps.Repository == nil || e.deps.Repository(project)
}

// refuseWithoutRepository refuses a call of a builtin that reads git or a forge when the project has no repository, before the call reads its arguments.
// A call whose exempt keyword holds a non-empty program is not refused: a gate that names its own program with run reads no git. The message names the builtin and the fix.
func (s *script) refuseWithoutRepository(b *starlark.Builtin, kwargs []starlark.Tuple, exempt string) error {
	if s.hasRepository() || namesKeyword(kwargs, exempt) {
		return nil
	}

	return fmt.Errorf("%s needs a repository, and project %s has no repository. Set `source` or `forge` in the project file, or remove the %s call", b.Name(), s.in.Project, b.Name())
}

// namesKeyword reports whether a call passes the keyword with a non-empty list or tuple. None and an empty list name no program.
func namesKeyword(kwargs []starlark.Tuple, keyword string) bool {
	return keyword != "" && slices.ContainsFunc(kwargs, func(pair starlark.Tuple) bool {
		name, ok := pair[0].(starlark.String)
		program, isSequence := pair[1].(starlark.Sequence)

		return ok && string(name) == keyword && isSequence && program.Len() > 0
	})
}

// predeclared returns the builtins of a workflow file: json, and one builtin for each builtin of the registry of the engine.
func (s *script) predeclared() starlark.StringDict {
	out := starlark.StringDict{jsonModule: starjson.Module}

	for _, name := range s.e.registry.Names() {
		def, _ := s.e.registry.Builtin(name)

		out[name] = starlark.NewBuiltin(name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			return s.traced(b, kwargs, func() (starlark.Value, error) {
				if def.NeedsRepository {
					if err := s.refuseWithoutRepository(b, kwargs, def.ExemptKeyword); err != nil {
						return nil, err
					}
				}

				return def.Call(s, b, args, kwargs)
			})
		})
	}

	return out
}

// where adds the position of a run-time error: the innermost frame of the file, not the Go builtin, then the message.
// The cause stays in the chain, so errors.Is still finds a cancel or a refused merge.
func where(err error) error {
	var eval *starlark.EvalError
	if !errors.As(err, &eval) {
		return err
	}

	return fmt.Errorf("%s: %w", model.InnermostPosition(eval.CallStack), err)
}
