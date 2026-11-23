package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/atomicfile"
)

const cancelsDir = "canceled"

// A cancel marker is a file named for a run, in the canceled folder of the state folder. It holds the item and the project of the run, and the claim token the run held at the cancel.
// beehave cancel writes it before it cancels the run. The marker ends when the release of the worktree and the claim ends.
// A marker that is still there at the next start shows a release that no process finished.

func cancelPath(dir, id string) string {
	return filepath.Join(dir, cancelsDir, runFile(id))
}

// runFile is the file name of a run id. The id of a round run holds "/", so the name carries the escaped form; runOfFile reads it back.
func runFile(id string) string { return url.PathEscape(id) }

// runOfFile returns the run id that a file name from runFile holds.
func runOfFile(name string) (string, error) { return url.PathUnescape(name) }

// tempPrefix starts the name of a marker that is not complete. The sweep skips such a file.
const tempPrefix = "."

// cancelMark is what a cancel marker holds: the item and the project of the run, and the token of the claim that the run held at the cancel, or none.
type cancelMark struct {
	ItemInput

	Owner string `json:"owner,omitempty"`
}

// attempt returns the attempt that the give-back of the marker releases: the run of the marker with the token of the marker.
func (m cancelMark) attempt(id string) *attempt {
	return &attempt{id: id, in: m.ItemInput, owner: m.Owner}
}

// markCancel writes the cancel marker of a run. It writes a temporary file in the same folder, then renames it, so a crash leaves no half marker.
func markCancel(dir, id string, mark cancelMark) error {
	if err := writeMarker(dir, id, mark); err != nil {
		return fmt.Errorf("mark the cancel of run %s: %w", id, err)
	}

	return nil
}

func writeMarker(dir, id string, mark cancelMark) error {
	folder := filepath.Join(dir, cancelsDir)

	if err := os.MkdirAll(folder, ownersMode); err != nil {
		return err
	}

	data, err := json.Marshal(mark)
	if err == nil {
		err = atomicfile.Write(cancelPath(dir, id), append(data, '\n'), ownerMode)
	}

	return err
}

// pendingCancels reads every cancel marker, by run id. A marker that cannot be read is logged and skipped: it must not stop the sweep of the other runs.
func pendingCancels(dir string, logger *slog.Logger) (map[string]cancelMark, error) {
	entries, err := os.ReadDir(filepath.Join(dir, cancelsDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read the cancel markers: %w", err)
	}

	return readMarkers(dir, entries, logger), nil
}

func readMarkers(dir string, entries []os.DirEntry, logger *slog.Logger) map[string]cancelMark {
	pending := make(map[string]cancelMark, len(entries))

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tempPrefix) {
			continue
		}

		id, mark, err := readMarker(dir, entry.Name())
		if err != nil {
			logger.Warn("skipped a cancel marker", "error", err)

			continue
		}

		pending[id] = mark
	}

	return pending
}

// readMarker reads the cancel marker with this file name and returns the run id it holds.
func readMarker(dir, name string) (string, cancelMark, error) {
	id, err := runOfFile(name)
	if err != nil {
		return "", cancelMark{}, fmt.Errorf("bad file name: %w", err)
	}

	mark, err := readCancel(dir, id)
	if err != nil {
		return "", cancelMark{}, fmt.Errorf("cannot be read: %w", err)
	}

	return id, mark, nil
}

// readCancel reads the cancel marker of one run.
func readCancel(dir, id string) (cancelMark, error) {
	var mark cancelMark

	data, err := os.ReadFile(cancelPath(dir, id))
	if err == nil {
		err = json.Unmarshal(data, &mark)
	}

	if err != nil {
		return cancelMark{}, fmt.Errorf("read the cancel marker of run %s: %w", id, err)
	}

	return mark, nil
}

// forgetRun removes the files that belong to one run that the store deleted: its token file and its cancel marker. A missing file is not an error.
func forgetRun(dir, id string) error {
	return errors.Join(removeFile(ownerPath(dir, id)), removeFile(cancelPath(dir, id)))
}

// forgetAttempt removes the files of a run that belong to the claim token owner: the token file when it holds owner, and the cancel marker when it names owner
// or no token. A token that a later resume saved, and the marker of a later cancel, stay.
func forgetAttempt(dir, id, owner string) error {
	return underOwners(dir, func() error {
		return errors.Join(removeOwnedToken(dir, id, owner), removeOwnedMarker(dir, id, owner))
	})
}

// removeOwnedMarker removes the cancel marker of a run unless it names a token other than owner. The caller holds the owners lock.
func removeOwnedMarker(dir, id, owner string) error {
	if mark, err := readCancel(dir, id); err == nil && mark.Owner != "" && mark.Owner != owner {
		return nil
	}

	return removeFile(cancelPath(dir, id))
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}

	return nil
}

// forget removes the files of a run and logs a failure: the run is over, so the failure stops nothing.
func (e *Engine) forget(id string) {
	e.warnOn(forgetRun(e.dir, id), id)
}

// forgetAttempt removes the files of a run that belong to the claim token owner and logs a failure.
func (e *Engine) forgetAttempt(id, owner string) {
	e.warnOn(forgetAttempt(e.dir, id, owner), id)
}

// markRunCancel writes the cancel marker of a run with the token of the claim that the run holds now. A run that recorded no claim gets a marker with no token.
// It reads the token and writes the marker under the owners lock, so a give-back that compares a marker and removes it never removes this one.
func markRunCancel(client dbos.Client, dir, id string, in ItemInput) (cancelMark, error) {
	mark := cancelMark{ItemInput: in}

	err := underOwners(dir, func() error {
		owner, err := currentOwner(client, dir, id)
		if err != nil && !errors.Is(err, errNoOwner) {
			return fmt.Errorf("mark the cancel of run %s: %w", id, err)
		}

		mark.Owner = owner

		return markCancel(dir, id, mark)
	})

	return mark, err
}

// warnOn logs a failure to remove a file of a run.
func (e *Engine) warnOn(err error, id string) {
	if err != nil {
		e.logger.Warn("cannot remove the files of a finished run", "run", id, "error", err)
	}
}

// sweepCancels gives back every run that beehave cancel canceled while an engine ran, when that engine stopped before its release ended.
// Launch calls it before the tick starts, so all the markers share one release timeout: a tracker that hangs delays the start once, not once for each marker.
// A marker whose run is no longer canceled belongs to a run that was resumed, deleted or never canceled.
// A marker whose item has a newer run is dropped: the give-back would stop the agents of the newer run.
func (e *Engine) sweepCancels() {
	pending, err := pendingCancels(e.dir, e.logger)
	if err != nil {
		e.logger.Warn("cannot read the canceled runs", "error", err)

		return
	}

	ctx, cancel := ReleaseDeps{Clock: e.deps.Clock}.limit(context.WithoutCancel(e.ctx))
	defer cancel()

	for id, mark := range pending {
		e.sweepOne(ctx, mark.attempt(id), true)
	}
}

// sweepOne gives back the run of one marker, or drops the marker. A marker of a run that is not canceled belongs to a resumed run.
// At the start the marker goes. While the engine runs, it stays only for a run that is still open, because the cancel may still come.
func (e *Engine) sweepOne(ctx context.Context, a *attempt, atStart bool) {
	state, err := e.markerState(a.id, a.in.Item)

	switch {
	case err != nil:
		e.logger.WarnContext(ctx, "cannot read a canceled run", "run", a.id, "error", err)
	case state.superseded || (!state.canceled && (atStart || !state.open)):
		e.warnOn(underOwners(e.dir, func() error { return removeOwnedMarker(e.dir, a.id, a.owner) }), a.id)
	case !state.canceled:
		// beehave cancel writes the marker before it cancels the run, so the cancel may still come.
	default:
		// The engine stopped after the cancel and before the run woke, so no live wait turned off the queued merge.
		if err := disarmCanceled(ctx, e.store().List, e.deps.Merge, a.id); err != nil {
			e.logger.WarnContext(ctx, "cannot turn off the queued merge of a canceled run", "run", a.id, "error", err)
		}

		e.giveBack(ctx, a)
	}
}

// attemptOf returns the attempt number in a run id. ok is false when the id has none.
func attemptOf(id, prefix string) (attempt int, ok bool) {
	attempt, err := strconv.Atoi(strings.TrimPrefix(id, prefix))

	return attempt, err == nil
}

// markerState reads the runs of the item that a cancel marker names. canceled is true when the store marks that run as canceled.
// superseded is true when the item has a run with a higher attempt number.
func (e *Engine) markerState(id, item string) (state markerRun, err error) {
	prefix := runPrefix + item + attemptMarker

	rows, err := e.store().List(dbos.WithFilterWorkflowIDPrefix(prefix), dbos.WithFilterName(itemName))
	if err != nil {
		return markerRun{}, err
	}

	own, _ := attemptOf(id, prefix) // A name with no number counts as attempt 0, so every other attempt is newer.

	for _, row := range rows {
		attempt, numbered := attemptOf(row.ID, prefix)

		state.canceled = state.canceled || (row.ID == id && row.Status == dbos.WorkflowStatusCancelled)
		state.open = state.open || (row.ID == id && !run{Status: row}.terminal())
		state.superseded = state.superseded || (numbered && attempt > own)
	}

	return state, nil
}

// markerRun is what the store says about the run of a cancel marker.
type markerRun struct {
	// canceled is true when the store marks the run as canceled.
	canceled bool
	// open is true when the run has not ended.
	open bool
	// superseded is true when the item has a run with a higher attempt number.
	superseded bool
}
