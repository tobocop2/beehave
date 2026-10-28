package model

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// CreateRequest holds the arguments of Tracker.Create. Kind, Parent and Assignee are optional.
type CreateRequest struct {
	Title          string
	BodyFile       string
	Severity       Severity
	Kind           string
	Parent         string
	Assignee       string
	AllowDuplicate bool
}

// ErrInvalidRequest is returned when a CreateRequest lacks a title, a body file or a severity.
var ErrInvalidRequest = errors.New("invalid create request")

type requestError string

func (e requestError) Error() string { return string(e) }

func (requestError) Is(target error) bool { return target == ErrInvalidRequest }

// Validate refuses a request without a title, a non-empty body file or a severity from P0 to P3.
func (r CreateRequest) Validate() error {
	if r.Title == "" {
		return requestError("create needs --title")
	}

	if !nonEmptyFile(r.BodyFile) {
		return requestError("create needs --body-file with the finding and its reproduction")
	}

	if _, err := ParseSeverity(string(r.Severity)); err != nil {
		return requestError(fmt.Sprintf("create needs --severity P0, P1, P2 or P3, got '%s'", r.Severity))
	}

	return nil
}

func nonEmptyFile(path string) bool {
	if path == "" {
		return false
	}

	info, err := os.Stat(path)

	return err == nil && info.Size() > 0
}

// TrackerName names a tracker adapter that a project selects.
type TrackerName string

// The tracker adapters.
const (
	TrackerBeads  TrackerName = "beads"
	TrackerGithub TrackerName = "github"
)

// ErrUnknownTracker is returned when text names no tracker adapter.
var ErrUnknownTracker = errors.New("unknown tracker")

// Trackers lists every tracker adapter.
func Trackers() []TrackerName {
	return []TrackerName{TrackerBeads, TrackerGithub}
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
