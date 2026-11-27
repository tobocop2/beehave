package flow

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehest/internal/proc"
	"github.com/tobocop2/beehest/internal/workflow"
)

const (
	// streamKind is the step kind of the stream builtin.
	streamKind = "stream"
	// afterKeyword is the keyword of stream that holds the cursor of an earlier call.
	afterKeyword = "after"
	// streamPageEntries is the most entries that one stream call returns. The text of the entries is capped by valueLimit.
	streamPageEntries = 200
	// entryKindField is the field of an entry that holds its kind.
	entryKindField = "kind"
)

// streamRead is the record of a stream step: the entries after the cursor, the cursor of the last entry returned, and whether the child has no entry left.
type streamRead struct {
	Entries []AgentEntry `json:"entries"`
	Next    string       `json:"next"`
	Ended   bool         `json:"ended"`
}

// stream returns the redacted entries of the agent stream of a run that this run spawned, written after the cursor, and waits up to the timeout for a new one.
// The call is one recorded step after the recorded step of its deadline, so a replay returns the same entries and the same cursor.
func (s *script) stream(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call, err := s.streamCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	window, err := runStep(s.e, s.ctx, s.nextName(streamKind, call.label)+deadlineSuffix, func(context.Context) (streamWindow, error) {
		return streamWindow{Until: s.e.deps.Clock.reading().Add(call.wait), Run: call.run}, nil
	})
	if err != nil {
		return nil, err
	}

	read, err := countedStep(s, streamKind, call.label, func(ctx context.Context) (streamRead, error) {
		return s.e.streamWait(ctx, call.run, call.from, window.Until)
	})
	if err != nil {
		return nil, err
	}

	return streamValue(read), nil
}

// streamWindow is the record of the deadline step of one stream call: the time the wait ends and the run it reads. Status reads it while the call waits.
type streamWindow struct {
	Until time.Time `json:"until"`
	Run   string    `json:"run"`
}

// streamArgs is a checked stream call: the child run, the cursor, how long to wait and the label.
type streamArgs struct {
	run   string
	from  workflow.StreamCursor
	wait  time.Duration
	label string
}

// streamCall reads and checks the arguments of a stream call. The run must be a child that this run spawned, and the call reads its latest attempt.
func (s *script) streamCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (streamArgs, error) {
	var (
		target                starlark.Value
		after, timeout, label string
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{runKeyword: &target, afterKeyword: &after, timeoutKeyword: &timeout, nameKeyword: &label}); err != nil {
		return streamArgs{}, err
	}

	from, wait, err := streamLimits(after, timeout)
	if err != nil {
		return streamArgs{}, err
	}

	ref, err := s.childRef(target)

	return streamArgs{run: ref.Run, from: from, wait: wait, label: label}, err
}

// streamLimits checks the cursor and the timeout of a stream call.
func streamLimits(after, timeout string) (workflow.StreamCursor, time.Duration, error) {
	from, err := workflow.ParseCursor(after)
	if err != nil {
		return from, 0, fmt.Errorf("stream: %w", err)
	}

	wait, err := workflow.Deadline(timeoutKeyword, timeout)
	if err != nil {
		return from, 0, fmt.Errorf("stream: %w", err)
	}

	return from, wait, nil
}

// streamWait reads the entries after the cursor until some exist, the child has ended, or the deadline passes. At the deadline it returns no entry and the cursor it got.
func (e *Engine) streamWait(ctx context.Context, run string, from workflow.StreamCursor, until time.Time) (streamRead, error) {
	client := viewOf(dbos.WithoutCancel(e.ctx), e.dir)

	for {
		got, err := streamAfter(client, run, from)
		if err != nil || len(got.Entries) > 0 || got.Ended || !e.deps.Clock.reading().Before(until) {
			return got, err
		}

		if err := proc.Sleep(ctx, cmp.Or(e.deps.Clock.Poll, followPoll)); err != nil {
			return streamRead{}, err
		}
	}
}

// streamAfter reads one page of the entries of the agent streams of a run after the cursor, with the one reader that status --follow uses.
// The state of the run is read before its phases, so a run that ended before the read has no entry that this call misses. A cursor in a phase that the run does not have
// starts the run at its first entry, as for a run that gather started again after a failure.
func streamAfter(c storeView, run string, from workflow.StreamCursor) (streamRead, error) {
	over, phases, err := lookAt(c, run)
	if err != nil {
		return streamRead{}, err
	}

	if at := slices.Index(phases, from.Phase); at >= 0 {
		phases = phases[at:]
	} else {
		from = workflow.StreamCursor{}
	}

	page := streamRead{Entries: []AgentEntry{}, Next: from.String()}
	partial, err := fillPage(c, &page, phases, from)
	page.Ended = over && !partial

	return page, err
}

// fillPage adds to the page the entries of the phases that follow the cursor, until the page is full. It reports whether an entry is left that the page does not hold.
func fillPage(c storeView, page *streamRead, phases []string, from workflow.StreamCursor) (bool, error) {
	size := 0

	for _, phase := range phases {
		entries, err := readSnapshot(c, phase)
		if err != nil {
			return false, err
		}

		if page.take(phase, entries, from, &size) {
			return true, nil
		}
	}

	return false, nil
}

// take adds the entries of one phase that follow the cursor, and reports whether it met an entry that the page has no room for.
func (r *streamRead) take(phase string, entries []AgentEntry, from workflow.StreamCursor, size *int) bool {
	for _, entry := range entries {
		switch {
		case phase == from.Phase && entry.Seq <= from.Seq:
		case !r.fits(*size, entry):
			return true
		default:
			*size += len(entry.Text)
			r.Entries = append(r.Entries, entry)
			r.Next = workflow.StreamCursor{Phase: phase, Seq: entry.Seq}.String()
		}
	}

	return false
}

// fits reports whether the page, with size bytes of text, has room for the entry. A page always takes its first entry.
func (r *streamRead) fits(size int, entry AgentEntry) bool {
	return len(r.Entries) == 0 || len(r.Entries) < streamPageEntries && size+len(entry.Text) <= valueLimit
}

// streamValue makes the struct that stream returns: the entries, each with the fields seq, attempt, kind and text, the next cursor and ended.
func streamValue(read streamRead) *starlarkstruct.Struct {
	entries := make([]starlark.Value, len(read.Entries))

	for i, entry := range read.Entries {
		entries[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			"seq": starlark.MakeInt(entry.Seq), "attempt": starlark.MakeInt(entry.Try), entryKindField: starlark.String(entry.Kind), "text": starlark.String(entry.Text),
		})
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"entries": starlark.NewList(entries), "next": starlark.String(read.Next), "ended": starlark.Bool(read.Ended),
	})
}
