package model

import (
	"errors"
	"fmt"
	"strings"
)

// Severity ranks a work item from P0 (worst) to P3, or SeverityUnknown.
type Severity string

// The severities of a work item.
const (
	SeverityP0      Severity = "P0"
	SeverityP1      Severity = "P1"
	SeverityP2      Severity = "P2"
	SeverityP3      Severity = "P3"
	SeverityUnknown Severity = "P?"
)

// FieldSeparator splits the columns of Item.Line. A shell reader folds a tab, so the unit separator is used.
const FieldSeparator = "\x1f"

// ErrUnknownSeverity is returned when text names no severity from P0 to P3.
var ErrUnknownSeverity = errors.New("unknown severity")

// Severities lists the severities a person can file, worst first.
func Severities() []Severity {
	return []Severity{SeverityP0, SeverityP1, SeverityP2, SeverityP3}
}

// ParseSeverity returns the severity that text names. P? is not a severity a person can set.
func ParseSeverity(text string) (Severity, error) {
	for _, severity := range Severities() {
		if string(severity) == text {
			return severity, nil
		}
	}

	return "", fmt.Errorf("%w %q: use one of %s", ErrUnknownSeverity, text, joinNames(Severities()))
}

// Item is one open work item.
type Item struct {
	ID       string
	Severity Severity
	Hold     bool
	Parent   string
	Assignee string
	Title    string
}

// Line returns the item as one line of unit-separated columns.
func (i Item) Line() string {
	hold := "0"
	if i.Hold {
		hold = "1"
	}

	return strings.Join([]string{i.ID, string(i.Severity), hold, i.Parent, i.Assignee, i.Title}, FieldSeparator)
}
