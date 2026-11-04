package store

import (
	"context"
	"fmt"
	"strings"
)

// Status is the state that a workflow sets on an item. In progress is not a status: it is the claim.
type Status string

// The three statuses. Hold keeps an item out of the work queue and is not closed.
const (
	StatusOpen   Status = "open"
	StatusClosed Status = "closed"
	StatusHold   Status = "hold"
)

// ParseStatus returns the status that text names, or an error that lists the three names.
func ParseStatus(text string) (Status, error) {
	switch status := Status(text); status {
	case StatusOpen, StatusClosed, StatusHold:
		return status, nil
	}

	return "", fmt.Errorf("status %q is not known. Use \"open\", \"closed\" or \"hold\"", text)
}

// NewItem is the item that Create files. Parent is the id of the parent item, or empty. Assignee is the login or name that the item is assigned to, or empty for no assignee.
// Severity is one of P0 to P3, or empty for an item with no severity. The caller checks the value, so an adapter never sees another one.
type NewItem struct {
	Title    string
	Body     string
	Parent   string
	Assignee string
	Severity string
	// Operator marks an item that only the operator can resolve. A tracker that keeps labels files it as the label "operator"; one that keeps none refuses it.
	Operator bool
}

// OperatorLabel is the label that marks an item for the operator.
const OperatorLabel = "operator"

// KeyPrefix starts the marker line that makes a write safe to repeat.
const KeyPrefix = "obee-key: "

// Marker returns the line that an adapter writes into a body or a comment to record a key. A lookup matches the whole line, so the key r:1 does not match r:10.
func Marker(key string) string { return KeyPrefix + key }

// WithMarker appends the marker line of key to text.
func WithMarker(text, key string) string {
	if text == "" {
		return Marker(key)
	}

	return text + "\n\n" + Marker(key)
}

// Visible returns the text of a comment without its marker lines and without the blank lines that WithMarker put before them.
func Visible(text string) string {
	var kept []string

	for line := range strings.SplitSeq(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), KeyPrefix) {
			kept = append(kept, line)
		}
	}

	return strings.TrimRight(strings.Join(kept, "\n"), "\n")
}

// HasMarker reports whether a line of text is exactly the marker of key.
func HasMarker(text, key string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) == Marker(key) {
			return true
		}
	}

	return false
}

// Writer is a tracker that a workflow can change. It is a separate contract from Tracker, so a tracker with no write support stays valid.
//
// Create files an item once for each key. The same key again returns the id of the first item and created = false: a step that runs twice because of a crash
// writes one item. SetStatus is idempotent: the same status twice leaves one state. Assign sets the one assignee of an item, and the same assignee twice leaves one state.
// A tracker that keeps no assignee refuses Create with an assignee and refuses Assign. Comment adds a comment once for each item and key.
// An adapter records the key in the item body or in the comment text, as the line Marker(key), and looks it up before it writes.
// A key and a call are for one writer: callers pass the run id inside the key, so two runs never share a key.
type Writer interface {
	Create(ctx context.Context, key string, item NewItem) (id string, created bool, err error)
	SetStatus(ctx context.Context, id string, status Status) error
	Comment(ctx context.Context, id, key, text string) error
	Assign(ctx context.Context, id, assignee string) error
}
