package flow

import (
	"context"
	"errors"
	"io/fs"

	"github.com/tobocop2/obee/internal/batch"
	forgepkg "github.com/tobocop2/obee/internal/forge"
	gatepkg "github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/host"
)

// Failure is how a child run returns an error: its text and the kind of each known error in its chain. A child run ends with no error,
// because the workflow library rebuilds a stored error from its text and the rebuilt error is no longer any of the errors it was.
// Kinds holds the text of each known error that errors.Is finds in the chain. The parent finds the same errors again from these texts.
type Failure struct {
	Text  string   `json:"text"`
	Kinds []string `json:"kinds,omitempty"`
}

// failureOf records err. A nil err gives a nil Failure.
func (e *Engine) failureOf(err error) *Failure {
	if err == nil {
		return nil
	}

	failure := &Failure{Text: err.Error()}

	for _, known := range e.knownErrors() {
		if errors.Is(err, known) {
			failure.Kinds = append(failure.Kinds, known.Error())
		}
	}

	return failure
}

// productionKnown lists every exported sentinel error that a caller of a gate or a merge tests with errors.Is, and the errors of the standard
// library that a gate or a merge returns. TestKnownErrors_EachExportedSentinelIsListed reads this function and fails for a sentinel of the
// packages a gate or a merge calls that is not here. The engine cannot import a backend, so the sentinels of a backend come in through Deps.Kept.
func productionKnown() []error {
	return []error{
		context.Canceled, context.DeadlineExceeded, fs.ErrNotExist,
		forgepkg.ErrNoForge, forgepkg.ErrNoPull, ErrMemberNotOpen, batch.ErrGateVoid,
		gatepkg.ErrNoTests, gatepkg.ErrTreeChanged, gatepkg.ErrNoVerdict, gatepkg.ErrNoHead, gatepkg.ErrNoCloneURL, gatepkg.ErrCloneOutsideRoot,
		host.ErrUnbanked, host.ErrNoRepos, host.ErrDiskLow, host.ErrDiskRead, host.ErrInUse, host.ErrInUseRead, host.ErrJobState, host.ErrLost, host.ErrNotDeleted,
	}
}

// knownErrors lists the errors that survive a child run: productionKnown, and Deps.Kept, which adds to it and never replaces it.
func (e *Engine) knownErrors() []error {
	return append(productionKnown(), e.deps.Kept...)
}

// rebuiltError is the error of a failed child run: the text of the original, and each known error that the original held.
type rebuiltError struct {
	text  string
	kinds []error
}

func (r rebuiltError) Error() string   { return r.text }
func (r rebuiltError) Unwrap() []error { return r.kinds }

// err rebuilds the error of the failure, so errors.Is finds each kind again. A nil Failure gives nil.
func (f *Failure) err(known []error) error {
	if f == nil {
		return nil
	}

	rebuilt := rebuiltError{text: f.Text}

	for _, kind := range f.Kinds {
		for _, candidate := range known {
			if candidate.Error() == kind {
				rebuilt.kinds = append(rebuilt.kinds, candidate)
			}
		}
	}

	return rebuilt
}
