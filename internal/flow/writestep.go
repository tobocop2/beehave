package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

// envKeyword is the keyword env of a call.
const envKeyword = "env"

// writeKeyEnv is the variable that holds the key of a write in the environment of its program, its read-back and its undo.
// The prefix BEEHAVE_ belongs to the engine, so a file cannot set the variable.
const writeKeyEnv = "BEEHAVE_WRITE_KEY"

// writeKey makes the key of a write from the run id and the name of its recorded step. Both are the same on a replay, so the key is the same.
func writeKey(run, step string) string {
	sum := sha256.Sum256([]byte(run + "\x00" + step))

	return hex.EncodeToString(sum[:16])
}

// undoStep is a write that finished and has an undo program. The undo runs as a recorded step if a later step of the run fails.
type undoStep struct {
	write   string
	request CommandRequest
}

// writePlan holds the requests of one write call. The read-back and the undo are empty when the call names none.
type writePlan struct {
	run, readBack, undo CommandRequest
	key                 string
}

// writeRaw are the arguments of a write call as the file wrote them.
type writeRaw struct {
	commandRaw
	readBack, undo starlark.Value
}

// writeCall unpacks the arguments of the write builtin.
func writeCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (writeRaw, error) {
	var w writeRaw

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		runKeyword: &w.run, "read_back": &w.readBack, "undo": &w.undo, "stdin": &w.stdin, envKeyword: &w.own, timeoutKeyword: &w.timeout, nameKeyword: &w.label,
	})

	return w, err
}

// writePlan makes the requests of a write call. Each request is built by the rules of command, and all of them carry the key of the step.
// It also returns the name of the step.
func (s *script) writePlan(w writeRaw) (string, writePlan, error) {
	step, err := s.name(builtinWrite, w.label)
	if err != nil {
		return "", writePlan{}, err
	}

	request, _, err := s.commandRequest(w.commandRaw)
	if err != nil {
		return "", writePlan{}, err
	}

	plan := writePlan{run: request, key: writeKey(workflowID(s.ctx), step)}
	plan.run.Env = append(plan.run.Env, writeKeyEnv+"="+plan.key)

	if plan.readBack, err = sibling(plan.run, "read_back", w.readBack); err != nil {
		return step, plan, err
	}

	plan.undo, err = sibling(plan.run, "undo", w.undo)

	return step, plan, err
}

// sibling makes the request of the read-back or the undo of a write: its own program, and the project, item, environment and timeout of the write.
// A call that names no program gives an empty request.
func sibling(write CommandRequest, keyword string, run starlark.Value) (CommandRequest, error) {
	if run == nil || run == starlark.None {
		return CommandRequest{}, nil
	}

	// The signature check has made sure that the value is a list of text and that the timeout is a duration, so only an empty list is left to refuse.
	words, err := workflow.Words(keyword, run)
	if err != nil || len(words) == 0 {
		return CommandRequest{}, fmt.Errorf("%s must name a program. Write %s = [\"program\", \"argument\"]", keyword, keyword)
	}

	write.Run = words

	return write, nil
}

// writeOutside runs a program that changes a system outside the run. The engine gives the program a key in the variable BEEHAVE_WRITE_KEY. The key is the
// same on every replay of the step, so the outside system can drop a repeat. A read-back program runs first: if it exits with zero, the write is
// already done and does not run. A write that exits with zero and names an undo is undone, in reverse order, if a later step of the run fails.
func (s *script) writeOutside(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	raw, err := writeCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	step, plan, err := s.writePlan(raw)
	if err != nil {
		return nil, err
	}

	done, err := runStep(s.e, s.ctx, step, func(ctx context.Context) (stepResult, error) { return s.runWrite(ctx, plan) })
	if err != nil {
		return nil, err
	}

	s.remember(step, plan, done.Outputs)

	return result(done.Outputs)
}

// remember adds the undo of a write to the undo list of the run. A write that the read-back skipped has no undo: someone else made it.
// A write that exited with a code other than zero keeps its undo, because the engine cannot tell how much of it happened.
func (s *script) remember(step string, plan writePlan, outputs map[string]any) {
	skipped, isBool := outputs["skipped"].(bool)
	if len(plan.undo.Run) > 0 && (!isBool || !skipped) {
		s.undos = append(s.undos, undoStep{write: step, request: plan.undo})
	}
}

// runWrite runs the read-back, then the write when the read-back did not report it done, and records the output with the key.
func (s *script) runWrite(ctx context.Context, plan writePlan) (stepResult, error) {
	if len(plan.readBack.Run) > 0 {
		found, err := s.e.deps.Commands.Run(ctx, plan.readBack)
		if err != nil {
			return stepResult{}, err
		}

		if found.RC == 0 {
			return stepResult{Outputs: writeOutputs(found.Stdout, found.Stderr, 0, plan.key, true)}, nil
		}
	}

	out, err := s.e.deps.Commands.Run(ctx, plan.run)
	if err != nil {
		return stepResult{}, err
	}

	return stepResult{Outputs: writeOutputs(out.Stdout, out.Stderr, out.RC, plan.key, false)}, nil
}

// undoAll runs the undo of each finished write, last write first. Each undo is a recorded step named after its write, so a replay after a
// crash reads the finished undos and runs the rest. A failed undo does not stop the others: the errors come back joined.
// It returns the names of the writes whose undo finished, in the order the undos ran, and the joined errors of the others.
func (s *script) undoAll() (undone []string, err error) {
	var errs []error

	for i := len(s.undos) - 1; i >= 0; i-- {
		if failure := s.undoOne(s.undos[i]); failure != nil {
			errs = append(errs, fmt.Errorf("undo of %s: %w", s.undos[i].write, failure))
		} else {
			undone = append(undone, s.undos[i].write)
		}
	}

	return undone, errors.Join(errs...)
}

// undoOne runs one undo as a recorded step. An undo that exits with a code other than zero fails the step, and a replay reads the same failure.
func (s *script) undoOne(undo undoStep) error {
	_, err := runStep(s.e, s.ctx, "undo:"+undo.write, func(ctx context.Context) (map[string]any, error) {
		out, err := s.e.deps.Commands.Run(ctx, undo.request)
		if err != nil {
			return nil, err
		}

		if out.RC != 0 {
			return nil, fmt.Errorf("exited with code %d. %s", out.RC, out.Stderr)
		}

		return commandOutputs(out.Stdout, out.Stderr, 0), nil
	})

	return err
}
