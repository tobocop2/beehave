package flow

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
)

// priorityQuery sets the place of a queued run. The workflow library has no call that changes the priority of a run that waits in a queue, so this is the one write.
// It names the status ENQUEUED: a run that started keeps the place that it started with. The third value is true for a person: the write goes through. For the
// restart of the engine it is false, and the write skips a run that a person marked, and a run whose parent (the fourth value) a person marked.
const priorityQuery = `UPDATE workflow_status SET priority = ? WHERE workflow_uuid = ? AND status = 'ENQUEUED' AND (? OR (
	workflow_uuid NOT IN (SELECT workflow_uuid FROM beehave_manual_place) AND ? NOT IN (SELECT workflow_uuid FROM beehave_manual_place)))`

// manualTableQuery makes the table of the runs whose place a person set. The restart of the engine leaves these places alone.
const manualTableQuery = `CREATE TABLE IF NOT EXISTS beehave_manual_place (workflow_uuid TEXT PRIMARY KEY)`

// manualMarkQuery records that a person set the place of a run.
const manualMarkQuery = `INSERT OR IGNORE INTO beehave_manual_place (workflow_uuid) VALUES (?)`

// placeWrite is the place for one run. The parent is the gate of a build job, or empty: a job under a gate that a person marked keeps the place that it took from
// the gate.
type placeWrite struct {
	place  uint
	parent string
}

// writePlaces sets the place of each queued run in places and says whether one of them was queued. It is the one write of a place, for a person (manual is true:
// the run is marked, and the restart of the engine keeps its place) and for the restart of the engine (manual is false: a run that a person marked, or whose
// parent a person marked, is skipped). An empty id is skipped.
func writePlaces(dir string, manual bool, places map[string]placeWrite) (bool, error) {
	moved := false

	db, err := sql.Open("sqlite", filepath.Join(dir, databaseFile)+"?_pragma=busy_timeout(30000)")
	if err == nil {
		defer db.Close()

		if _, err = db.ExecContext(context.Background(), manualTableQuery); err == nil {
			moved, err = writeEach(db, manual, places)
		}
	}

	return moved, failed("write the place of a queued run", err)
}

// writeEach is writePlaces on an open store that has the table of manual places.
func writeEach(db *sql.DB, manual bool, places map[string]placeWrite) (moved bool, err error) {
	for id, write := range places {
		if id == "" {
			continue
		}

		var set bool

		if set, err = writePlace(db, manual, id, write); err != nil {
			return moved, err
		}

		moved = moved || set
	}

	return moved, nil
}

// writePlace sets the place of one run, marks the run when a person set it, and says whether the run was queued.
// The place and the mark are one transaction: a place that is stored with no mark would be overwritten at the next start of the engine.
func writePlace(db *sql.DB, manual bool, id string, write placeWrite) (bool, error) {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return false, fmt.Errorf("begin the write of the place: %w", err)
	}

	defer tx.Rollback() //nolint:errcheck // a transaction that committed answers ErrTxDone, and one that failed has nothing to keep

	moved, err := placeAndMark(tx, manual, id, write)
	if err != nil {
		return false, err
	}

	return moved, failed("store the place", tx.Commit())
}

// placeAndMark runs the place query and, for a person, the mark query on tx, and says whether the run was queued.
func placeAndMark(tx *sql.Tx, manual bool, id string, write placeWrite) (bool, error) {
	ctx := context.Background()

	result, err := tx.ExecContext(ctx, priorityQuery, write.place, id, manual, write.parent)
	if err != nil {
		return false, fmt.Errorf("set the place: %w", err)
	}

	if n, counted := result.RowsAffected(); counted != nil || n == 0 || !manual {
		return n > 0, nil //nolint:nilerr // a store that cannot count the rows says that nothing moved
	}

	_, err = tx.ExecContext(ctx, manualMarkQuery, id)

	return true, failed("mark the place as set by a person", err)
}
