package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Stored lease values of a ClaimRow that are not a number of seconds.
const (
	LeaseNone       = 0
	LeaseUnreadable = -1
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

// ClaimRow is one claim: Holder holds Item and stored Lease seconds with it, or LeaseNone, or LeaseUnreadable.
// Nonce names one take and never repeats. The store keeps it with the holder, so a guard on the row fails for a later take of the same holder.
type ClaimRow struct {
	Item   string
	Holder string
	Lease  int64
	Nonce  string
}

// RowState is what the lease store holds for one item's claim row. Nonce is "" for a row that was written without one.
type RowState struct {
	Exists bool
	Held   bool
	Holder string
	Nonce  string
}

// ErrRowMoved is returned by ClaimRows.StoreLease and ClaimRows.Free when the row no longer belongs to the take the caller read.
var ErrRowMoved = errors.New("the claim row changed hands")

// ClaimRows stores the claim rows of the project. The lease store keeps one row for each item.
// Only beads fills it, whatever tracker the project uses, because beads has the atomic operations a lease needs.
//
// A method that cannot read or write its backend returns an error and never a guess: a failed read is no answer.
// TryHold sets the holder only when no holder is set. It returns nil when it lost, so a caller reads Row to learn the winner.
// StoreLease and Free change the row only while it still belongs to row.Holder and row.Nonce.
// A row of another holder or another take stays as it is, and the call returns ErrRowMoved. Free on a missing row is not an error.
// A holder with no recorded beat is absent from LastSeen.
type ClaimRows interface {
	HeldRows(ctx context.Context) ([]ClaimRow, error)
	LastSeen(ctx context.Context) (map[string]time.Time, error)
	Beat(ctx context.Context, holder string) error
	Row(ctx context.Context, item string) (RowState, error)
	TryHold(ctx context.Context, row ClaimRow) error
	StoreLease(ctx context.Context, row ClaimRow) error
	Free(ctx context.Context, row ClaimRow) error
}

// Tracker is an issue tracker: work items and their parents. beads and GitHub Issues implement it. The claim rows are not part of it.
type Tracker interface {
	OpenItems(ctx context.Context) ([]Item, error)
	Containers(ctx context.Context) ([]string, error)
	Children(ctx context.Context, container string) ([]string, error)
	Show(ctx context.Context, item string) (string, error)
	Close(ctx context.Context, item, reason string) error
	Create(ctx context.Context, request CreateRequest) (string, error)
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

// Forge is a code host: pull requests, reviews, threads and checks. GitHub implements it.
type Forge interface {
	Reviews(ctx context.Context, pr int) ([]Review, error)
	PRBody(ctx context.Context, pr int) (string, error)
	PRView(ctx context.Context, pr int, fields ...string) (PullRequest, error)
	PRList(ctx context.Context, limit int, args ...string) ([]PullRequest, error)
	PRChecks(ctx context.Context, pr int) ([]Check, error)
	OpenChangeRequests(ctx context.Context, pr int) ([]ChangeRequest, error)
	UnresolvedThreads(ctx context.Context, pr int) ([]ReviewThread, error)
	StackID(ctx context.Context, pr int) (string, error)
	ExistingReview(ctx context.Context, pr int) ([]string, error)
	Overlap(ctx context.Context, req OverlapRequest) (Overlap, error)
}
