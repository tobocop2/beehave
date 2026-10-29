package flow

import (
	"context"
	"errors"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/workflow"
)

// maxSteps is the number of Starlark instructions that one run may execute. The count is the same on a replay, so a run that hits it hits it every time.
const maxSteps = 1_000_000

// builtinFile is the file name that starlark-go gives to the frame of a Go builtin.
const builtinFile = "<builtin>"

// stepsVersion changes when a builtin changes its steps. It is part of the application version, so an old run is not replayed by a new build.
const stepsVersion = "star1"

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

	return ItemResult{Item: in.Item, Merge: s.mergeState, SentBack: s.sentBack, Stopped: s.stopped}, err
}

// thread makes the thread of a run. It logs print at the debug level and stops after maxSteps instructions.
func (e *Engine) thread(item string) *starlark.Thread {
	thread := &starlark.Thread{Name: item, Print: func(_ *starlark.Thread, msg string) { e.logger.Debug(msg) }}
	thread.SetMaxExecutionSteps(maxSteps)

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

// main loads the file and calls its function main with the item.
func (s *script) main(thread *starlark.Thread, sources *workflow.Sources, fields map[string]string) error {
	main, err := sources.Main(thread, s.predeclared())
	if err != nil {
		return err
	}

	item := starlark.StringDict{}
	for name, value := range fields {
		item[name] = starlark.String(value)
	}

	arg := starlarkstruct.FromStringDict(starlarkstruct.Default, item)
	arg.Freeze()

	_, err = starlark.Call(thread, main, starlark.Tuple{arg}, nil)

	return err
}

// predeclared returns the builtins of a workflow file, one for each name of workflow.Builtins.
func (s *script) predeclared() starlark.StringDict {
	table := map[string]func(*starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error){
		"agent": s.agent, "command": s.command, "env": s.env, "gate": s.gate, "merge": s.merge, "wait": s.wait,
		"struct": func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			return starlarkstruct.Make(nil, b, args, kwargs)
		},
	}

	out := starlark.StringDict{}

	for _, name := range workflow.Builtins() {
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
