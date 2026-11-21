package flow

import (
	"context"
	"errors"
	"sync"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
)

// agentStream is the key of the stream in which a phase writes what its agent says. maxStreamBytes caps the text of one phase.
const (
	agentStream    = "agent"
	maxStreamBytes = 4 << 20
	streamCutText  = "[output cut: the stream of this phase passed 4 MiB]"
)

// errNoStep is the error of a stream that has no workflow step to write in.
var errNoStep = errors.New("the agent does not run in a workflow step")

// EntryKind is what an entry of the agent stream holds.
type EntryKind string

// The kinds of an entry.
const (
	EntryText EntryKind = "text"
	EntryCall EntryKind = "call"
)

// AgentEntry is one entry of the stream of an agent phase. Seq counts the entries of the phase from 1. Try counts the starts of the agent in the phase from 1:
// a phase that a lost run started again appends its entries under the next try.
type AgentEntry struct {
	Seq  int       `json:"seq"`
	Try  int       `json:"try"`
	Kind EntryKind `json:"kind"`
	Text string    `json:"text"`
}

// streamer writes the entries of one start of an agent from a goroutine of its own, so a write that waits for the store never stops the reading of the output.
// Every entry passes the redactor before it is queued. The text that it queues is capped, so the queue is too.
type streamer struct {
	write  func(AgentEntry) error
	redact func(string) string
	warn   func(error)

	mu      sync.Mutex
	pending []AgentEntry
	seq     int
	try     int
	size    int
	cut     bool
	failed  bool
	// wake holds one signal that entries wait. stop ends the goroutine after it writes them, and done closes when it ended.
	wake, stop, done chan struct{}
}

// openStream starts the streamer of the agent start of a phase from the step of the agent. It reads the entries that earlier starts of the phase wrote,
// so this start takes the next try, goes on from the last number and counts their text against the cap. The caller must call finish once.
func (e *Engine) openStream(ctx context.Context, in PhaseInput) *streamer {
	step := stepContext(ctx)

	warn := func(err error) {
		e.logger.WarnContext(ctx, "cannot keep the output of the agent in its stream; the stream leaves it out", "item", in.Item, "step", in.Call, "error", err)
	}

	s := newStreamer(func(entry AgentEntry) error { return dbos.WriteStream(step, agentStream, entry) }, e.redact, warn)

	earlier, err := earlierEntries(step)
	if err != nil {
		warn(err)
	}

	s.resume(earlier)

	go s.run()

	return s
}

// stepContext returns the context of the workflow step that ctx is. The library runs every step body with a context of its own. Any other context gives nil,
// and the read and the writes of the stream then fail and are logged.
func stepContext(ctx context.Context) dbos.Context { //nolint:ireturn // the library hands the step out as its interface
	step, _ := ctx.(dbos.Context) //nolint:errcheck // a context that is no step reads as nil

	return step
}

// newStreamer makes a streamer at its first try, with no entry written.
func newStreamer(write func(AgentEntry) error, redact func(string) string, warn func(error)) *streamer {
	return &streamer{write: write, redact: redact, warn: warn, try: 1, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
}

// earlierEntries reads the entries that the phase wrote before this start.
func earlierEntries(step dbos.Context) ([]AgentEntry, error) {
	if step == nil {
		return nil, errNoStep
	}

	var entries []AgentEntry

	id, err := dbos.GetWorkflowID(step)
	if err == nil {
		entries, _, err = dbos.ReadStream[AgentEntry](step, id, agentStream, dbos.WithReadStreamSnapshot())
	}

	return entries, err
}

// resume sets the try, the number and the size of the streamer from the entries of the earlier starts. An earlier cut keeps the stream cut.
func (s *streamer) resume(earlier []AgentEntry) {
	for _, entry := range earlier {
		s.seq, s.try = max(s.seq, entry.Seq), max(s.try, entry.Try+1)
		s.size += len(entry.Text)
		s.cut = s.cut || entry.Text == streamCutText
	}
}

// say queues one message text or tool call. Text past the cap of the phase queues one entry that says the stream was cut, and then nothing.
func (s *streamer) say(said agent.Said) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cut {
		return
	}

	s.seq++
	s.pending = append(s.pending, s.entryOf(said))

	select {
	case s.wake <- struct{}{}:
	default: // a signal waits already, and the write that it starts reads every entry that waits
	}
}

// entryOf makes the entry of the next number from what the agent said, with the text redacted. An entry that passes the cap says the stream was cut instead.
// The caller holds the lock.
func (s *streamer) entryOf(said agent.Said) AgentEntry {
	entry := AgentEntry{Seq: s.seq, Try: s.try, Kind: EntryText, Text: s.redact(said.Text)}
	if said.Call {
		entry.Kind = EntryCall
	}

	s.size += len(entry.Text)
	if s.size > maxStreamBytes {
		entry.Kind, entry.Text, s.cut = EntryText, streamCutText, true
	}

	return entry
}

// run writes the entries that wait after each signal, and ends after the write that follows finish.
func (s *streamer) run() {
	defer close(s.done)

	for {
		stopping := s.stopped()

		for _, entry := range s.take() {
			s.send(entry)
		}

		if stopping {
			return
		}

		select {
		case <-s.wake:
		case <-s.stop:
		}
	}
}

// stopped reports whether finish was called.
func (s *streamer) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// take returns the entries that wait, and empties the queue.
func (s *streamer) take() []AgentEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries := s.pending
	s.pending = nil

	return entries
}

// send writes one entry. After a failed write it writes no more and logs once: the agent goes on, and the stream holds what came before the failure.
func (s *streamer) send(entry AgentEntry) {
	if s.failed {
		return
	}

	if err := s.write(entry); err != nil {
		s.failed = true

		s.warn(err)
	}
}

// finish writes the entries that wait and ends the goroutine. The agent has ended, so nothing calls say after it.
func (s *streamer) finish() {
	close(s.stop)
	<-s.done
}
