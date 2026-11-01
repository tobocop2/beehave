package model

import (
	"errors"
	"fmt"
	"time"
)

// TrackerName names a tracker adapter that a project selects.
type TrackerName string

// The tracker adapters.
const (
	TrackerLocal  TrackerName = "local"
	TrackerBeads  TrackerName = "beads"
	TrackerGithub TrackerName = "github"
)

// ErrUnknownTracker is returned when text names no tracker adapter.
var ErrUnknownTracker = errors.New("unknown tracker")

// ItemFields lists the fields that every tracker declares for an item: id, title and body. Each tracker returns this list from its ItemFields,
// and the check of a workflow file reads it to refuse an item field that no tracker has.
func ItemFields() []string { return []string{"id", "title", "body"} }

// Trackers lists every tracker adapter.
func Trackers() []TrackerName {
	return []TrackerName{TrackerLocal, TrackerBeads, TrackerGithub}
}

// ParseTracker returns the tracker adapter that text names.
func ParseTracker(text string) (TrackerName, error) {
	for _, name := range Trackers() {
		if string(name) == text {
			return name, nil
		}
	}

	return "", fmt.Errorf("%w %q: use one of %s", ErrUnknownTracker, text, joinNames(Trackers()))
}

// ForgeName names a forge adapter that a project selects.
type ForgeName string

// The forge adapters.
const ForgeGithub ForgeName = "github"

// ErrUnknownForge is returned when text names no forge adapter.
var ErrUnknownForge = errors.New("unknown forge")

// Forges lists every forge adapter.
func Forges() []ForgeName {
	return []ForgeName{ForgeGithub}
}

// ParseForge returns the forge adapter that text names.
func ParseForge(text string) (ForgeName, error) {
	for _, name := range Forges() {
		if string(name) == text {
			return name, nil
		}
	}

	return "", fmt.Errorf("%w %q: use one of %s", ErrUnknownForge, text, joinNames(Forges()))
}

// OverlapRequest names what Forge.Overlap compares. Set PR, or set Files to a list of file names and optionally Since.
type OverlapRequest struct {
	PR          string
	Files       string
	Since       string
	Base        string
	Integration string
	Now         time.Time
}

// Overlap is the lines the check prints and how many overlaps it found. A caller exits 1 when Found is above zero.
type Overlap struct {
	Lines []string
	Found int
}
