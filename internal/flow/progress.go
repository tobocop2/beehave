package flow

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"go.starlark.net/starlark"
)

// stepClock is the finest duration that a progress line shows.
const stepClock = 100 * time.Millisecond

// progress writes the lines that the run command shows on the terminal. Runs that spawn starts write at once, so a lock keeps each line whole.
// A progress with no writer prints nothing.
type progress struct {
	mu     sync.Mutex
	out    io.Writer
	redact func(string) string
}

// say writes one line. A failed write to the terminal has no recovery and does not stop the run.
func (p *progress) say(format string, args ...any) {
	if p == nil || p.out == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	_, _ = fmt.Fprintf(p.out, format+"\n", args...)
}

// text removes secrets from the text of an error before it goes to the terminal.
func (p *progress) text(err error) string {
	if p == nil || p.redact == nil {
		return err.Error()
	}

	return p.redact(err.Error())
}

// traced runs one builtin call of a workflow file and writes a line when it starts and a line when it ends.
func (s *script) traced(b *starlark.Builtin, kwargs []starlark.Tuple, call func() (starlark.Value, error)) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	if b.Name() == structBuiltin {
		return call()
	}

	run, step := workflowID(s.ctx), stepLabel(b.Name(), kwargs)
	s.e.progress.say("%s step %s started", run, step)

	started := time.Now()
	value, err := call()
	s.e.progress.ended(run, step, time.Since(started).Round(stepClock), err)

	return value, err
}

// ended writes the line that closes a step: stopped, failed or done.
func (p *progress) ended(run, step string, took time.Duration, err error) {
	switch {
	case errors.Is(err, errStopped):
		p.say("%s step %s stopped the item after %s", run, step, took)
	case err != nil:
		p.say("%s step %s failed after %s: %s", run, step, took, strings.Join(strings.Fields(p.text(err)), " "))
	default:
		p.say("%s step %s done in %s", run, step, took)
	}
}

// structBuiltin is the builtin that makes a struct. It records no step, so a workflow file may call it any number of times without a line.
const structBuiltin = "struct"

// stepLabel names a call for the terminal: the builtin, then the label of the call when it has one.
func stepLabel(builtin string, kwargs []starlark.Tuple) string {
	for _, pair := range kwargs {
		key, isKey := pair[0].(starlark.String)
		label, isText := pair[1].(starlark.String)

		if isKey && string(key) == nameKeyword && isText && label != "" {
			return builtin + ":" + string(label)
		}
	}

	return builtin
}
