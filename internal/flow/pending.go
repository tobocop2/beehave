package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/atomicfile"
)

const cancelsDir = "canceled"

// A cancel marker is a file named for a run, in the canceled folder of the state folder. It holds the item and the project of the run.
// obee cancel writes it before it cancels the run. The marker ends when the release of the worktree and the claim ends.
// A marker that is still there at the next start shows a release that no process finished.

func cancelPath(dir, id string) string {
	return filepath.Join(dir, cancelsDir, id)
}

// tempPrefix starts the name of a marker that is not complete. The sweep skips such a file.
const tempPrefix = "."

// markCancel writes the cancel marker of a run. It writes a temporary file in the same folder, then renames it, so a crash leaves no half marker.
func markCancel(dir, id string, in ItemInput) error {
	if err := writeMarker(dir, id, in); err != nil {
		return fmt.Errorf("mark the cancel of run %s: %w", id, err)
	}

	return nil
}

func writeMarker(dir, id string, in ItemInput) error {
	folder := filepath.Join(dir, cancelsDir)

	if err := os.MkdirAll(folder, ownersMode); err != nil {
		return err
	}

	data, err := json.Marshal(in)
	if err == nil {
		err = atomicfile.Write(cancelPath(dir, id), append(data, '\n'), ownerMode)
	}

	return err
}

// pendingCancels reads every cancel marker, by run id. A marker that cannot be read is logged and skipped: it must not stop the sweep of the other runs.
func pendingCancels(dir string, logger *slog.Logger) (map[string]ItemInput, error) {
	entries, err := os.ReadDir(filepath.Join(dir, cancelsDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read the cancel markers: %w", err)
	}

	return readMarkers(dir, entries, logger), nil
}

func readMarkers(dir string, entries []os.DirEntry, logger *slog.Logger) map[string]ItemInput {
	pending := make(map[string]ItemInput, len(entries))

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tempPrefix) {
			continue
		}

		in, err := readCancel(dir, entry.Name())
		if err != nil {
			logger.Warn("skipped a cancel marker that cannot be read", "error", err)

			continue
		}

		pending[entry.Name()] = in
	}

	return pending
}

// readCancel reads the cancel marker of one run.
func readCancel(dir, id string) (ItemInput, error) {
	var in ItemInput

	data, err := os.ReadFile(cancelPath(dir, id))
	if err == nil {
		err = json.Unmarshal(data, &in)
	}

	if err != nil {
		return ItemInput{}, fmt.Errorf("read the cancel marker of run %s: %w", id, err)
	}

	return in, nil
}

// forgetRun removes the files that belong to one run: its token file and its cancel marker. A missing file is not an error.
func forgetRun(dir, id string) error {
	return errors.Join(removeFile(ownerPath(dir, id)), removeFile(cancelPath(dir, id)))
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

// warnOn logs a failure to remove a file of a run.
func (e *Engine) warnOn(err error, id string) {
	if err != nil {
		e.logger.Warn("cannot remove the files of a finished run", "run", id, "error", err)
	}
}

// sweepCancels gives back every run that obee cancel canceled while an engine ran, when that engine stopped before its release ended.
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

	for id, in := range pending {
		e.sweepOne(ctx, openRun{id: id, in: in})
	}
}

// sweepOne gives back the run of one marker, or drops the marker.
func (e *Engine) sweepOne(ctx context.Context, open openRun) {
	canceled, superseded, err := e.markerState(open.id, open.in.Item)

	switch {
	case err != nil:
		e.logger.WarnContext(ctx, "cannot read a canceled run", "run", open.id, "error", err)
	case !canceled || superseded:
		e.warnOn(removeFile(cancelPath(e.dir, open.id)), open.id)
	default:
		// The engine stopped after the cancel and before the run woke, so no live wait turned off the queued merge.
		list := func(opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
			return dbos.ListWorkflows(e.ctx, opts...)
		}
		if err := disarmCanceled(ctx, list, e.deps.Merge, open.id); err != nil {
			e.logger.WarnContext(ctx, "cannot turn off the queued merge of a canceled run", "run", open.id, "error", err)
		}

		e.giveBack(ctx, open)
	}
}

// attemptOf returns the attempt number in a run id. ok is false when the id has none.
func attemptOf(id, prefix string) (attempt int, ok bool) {
	attempt, err := strconv.Atoi(strings.TrimPrefix(id, prefix))

	return attempt, err == nil
}

// markerState reads the runs of the item that a cancel marker names. canceled is true when the store marks that run as canceled.
// superseded is true when the item has a run with a higher attempt number.
func (e *Engine) markerState(id, item string) (canceled, superseded bool, err error) {
	prefix := runPrefix + item + attemptMarker

	rows, err := dbos.ListWorkflows(e.ctx, dbos.WithFilterWorkflowIDPrefix(prefix), dbos.WithFilterName(itemName))
	if err != nil {
		return false, false, err
	}

	own, _ := attemptOf(id, prefix) // A name with no number counts as attempt 0, so every other attempt is newer.

	for _, row := range rows {
		attempt, numbered := attemptOf(row.ID, prefix)

		canceled = canceled || (row.ID == id && row.Status == dbos.WorkflowStatusCancelled)
		superseded = superseded || (numbered && attempt > own)
	}

	return canceled, superseded, nil
}
