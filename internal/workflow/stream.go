package workflow

import (
	"fmt"
	"strconv"
	"strings"

	"go.starlark.net/starlark"
)

const (
	streamName = "stream"
	// afterKeyword is the keyword of stream that holds the cursor of an earlier call.
	afterKeyword = "after"
)

// StreamCursor is the place in the agent streams of a run that a stream call returned: the phase of the last entry and its number in that phase.
// The zero value is the start.
type StreamCursor struct {
	Phase string
	Seq   int
}

// String is the text of the cursor, which is empty at the start.
func (c StreamCursor) String() string {
	if c == (StreamCursor{}) {
		return ""
	}

	return c.Phase + "/" + strconv.Itoa(c.Seq)
}

// ParseCursor reads the text that a stream call returned as next. The empty text is the start.
func ParseCursor(text string) (StreamCursor, error) {
	if text == "" {
		return StreamCursor{}, nil
	}

	phase, digits, found := strings.Cut(text, "/")
	seq, err := strconv.Atoi(digits)

	if !found || phase == "" || err != nil || seq < 1 || strconv.Itoa(seq) != digits {
		return StreamCursor{}, fmt.Errorf("%s is %q, and that is not a cursor that stream returned. Pass \"\" or the next of an earlier call", afterKeyword, text)
	}

	return StreamCursor{Phase: phase, Seq: seq}, nil
}

// cursorRule is the rule of the after keyword of stream.
func cursorRule(v starlark.Value) error {
	text, _ := starlark.AsString(v) // the unpack check has refused a value that is not text
	_, err := ParseCursor(text)

	return err
}

// streamSignatures holds the builtin that reads the agent stream of a child run.
func streamSignatures() map[string][]Param {
	return map[string][]Param{
		streamName: {
			{Name: "run", Kind: KindAny, Required: true, Why: "Give a run that spawn returned."},
			ruled(afterKeyword, KindText, cursorRule),
			{Name: timeoutKeyword, Kind: KindText, Required: true, Rule: deadlineRule(timeoutKeyword), Why: "The call waits at most this long for a new entry, such as \"5s\"."},
			opt(nameKeyword, KindText),
		},
	}
}

// streamResultFields holds the fields of the result of stream.
func streamResultFields() map[string][]string {
	return map[string][]string{streamName: {"entries", "next", "ended"}}
}
