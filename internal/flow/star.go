package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

// builtinFile is the file name that starlark-go gives to the frame of a Go builtin.
const builtinFile = "<builtin>"

// jsonModule is the one name of workflow.Builtins that is a module of the Starlark library, not a step.
const jsonModule = "json"

// stepsVersion changes when a builtin changes its steps. It is part of the application version, so an old run is not replayed by a new build.
const stepsVersion = "star3"

// runStar runs `main(item)` of the workflow file of the run on one thread. The files are the bytes that the run pinned.
// Each builtin does its work in DBOS steps, so a replay of this function makes the same calls and gets the recorded results.
func (e *Engine) runStar(ctx dbos.Context, in ItemInput, login string) (ItemResult, error) {
	sources, err := workflow.Open(in.File, in.Files)
	if err != nil {
		return ItemResult{}, err
	}

	fields, err := runStep(e, ctx, "item", func(c context.Context) (map[string]string, error) {
		return e.deps.Items.Fields(c, in.Project, in.Item)
	})
	if err != nil {
		return ItemResult{}, fmt.Errorf("read the fields of item %s: %w", in.Item, err)
	}

	s := &script{e: e, ctx: ctx, in: in, login: login, counts: map[string]int{}}
	err = s.end(s.main(e.thread(in.Item), sources, fields))

	if finishErr := s.finish(e.thread(in.Item), sources, fields); finishErr != nil {
		err = errors.Join(err, finishErr)
	}

	return ItemResult{Item: in.Item, Merge: s.mergeState, SentBack: s.sentBack, Stopped: s.stopped}, err
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

	_, err = starlark.Call(thread, main, starlark.Tuple{itemArg(fields)}, nil)

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

// steps maps the name of each builtin that is a Go function to that function.
func (s *script) steps() map[string]func(*starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return map[string]func(*starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error){
		"agent": s.agent, "batch_merge": s.batchMerge, "command": s.command, "env": s.env, "gate": s.gate, "gather": s.gather, "host_teardown": s.hostTeardown, "item_comment": s.itemComment, "item_create": s.itemCreate, "item_status": s.itemStatus, "merge": s.merge, "preflight": s.preflight, "queue": s.queue, "spawn": s.spawn, "wait": s.wait,
		"struct": func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			return starlarkstruct.Make(nil, b, args, kwargs)
		},
	}
}

// predeclared returns the builtins of a workflow file, one for each name of workflow.Builtins.
func (s *script) predeclared() starlark.StringDict {
	table := s.steps()
	out := starlark.StringDict{jsonModule: starjson.Module}

	for _, name := range workflow.Builtins() {
		if name == jsonModule {
			continue
		}

		fn := table[name]

		out[name] = starlark.NewBuiltin(name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			return fn(b, args, kwargs)
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

	// The first frame is the innermost. Only it can be a Go builtin: the frame after it is the call in the file.
	at := 0
	if len(eval.CallStack) > 1 && eval.CallStack.At(0).Pos.Filename() == builtinFile {
		at = 1
	}

	return fmt.Errorf("%s: %w", eval.CallStack.At(at).Pos, err)
}
