package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// RerunMark ends the name of an execution that runs a call again, such as a new try or the rerun after a provider limit. A call counts as one lane whatever its reruns.
const RerunMark = "/rerun"

// Call is one agent execution as the spend table records it. ID is the execution id that the agent step made.
type Call struct {
	ID          string
	Tree        string
	Run         string
	Step        int
	Item        string
	PullRequest int
	Name        string
	At          time.Time
}

// Sum is the cost of a set of spend rows. Unknown is true when a row of the set has no cost.
type Sum struct {
	Dollars float64
	Unknown bool
}

// StartCall inserts the row of an agent execution that starts now. Its cost is unknown until EndCall sets it.
func (l *Ledger) StartCall(ctx context.Context, call Call) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO spend (call, tree, run_id, step, session, item, pull_request, call_name, cost, at) VALUES (?, ?, ?, ?, '', ?, ?, ?, NULL, ?)`,
		call.ID, call.Tree, call.Run, call.Step, call.Item, call.PullRequest, call.Name, call.At.UnixMilli())
	if err != nil {
		return fmt.Errorf("record the agent call %s in the ledger: %w", call.ID, err)
	}

	l.mu.Lock()
	l.running[call.ID] = struct{}{}
	l.mu.Unlock()

	return nil
}

// EndCall sets the session and the cost of the execution id. A cost that is not valid leaves the cost unknown.
// The execution is no longer running, whether or not the write works: a row that keeps no cost is unknown.
func (l *Ledger) EndCall(ctx context.Context, id, session string, cost sql.NullFloat64) error {
	l.mu.Lock()
	delete(l.running, id)
	l.mu.Unlock()

	_, err := l.db.ExecContext(ctx, `UPDATE spend SET session = ?, cost = ? WHERE call = ?`, session, cost, id)
	if err != nil {
		return fmt.Errorf("set the cost of the agent call %s in the ledger: %w", id, err)
	}

	return nil
}

// TreeSpend sums the cost of every execution of one item tree. The tree is the id of its root run.
// An execution that runs in this process adds 0 until it ends. A row of another process with no cost is unknown.
func (l *Ledger) TreeSpend(ctx context.Context, tree string) (Sum, error) {
	return l.sum(ctx, `SELECT COALESCE(SUM(cost), 0), COALESCE(SUM(cost IS NULL AND call NOT IN (SELECT value FROM json_each(?))), 0) FROM spend WHERE tree = ?`, tree)
}

// SpendSince sums the cost of every execution that started at since or later. An execution that runs in this process adds 0 until it ends.
func (l *Ledger) SpendSince(ctx context.Context, since time.Time) (Sum, error) {
	return l.sum(ctx, `SELECT COALESCE(SUM(cost), 0), COALESCE(SUM(cost IS NULL AND call NOT IN (SELECT value FROM json_each(?))), 0) FROM spend WHERE at >= ?`, since.UnixMilli())
}

// sum runs a query whose first parameter is the JSON list of the running executions and whose second is arg.
func (l *Ledger) sum(ctx context.Context, query string, arg any) (Sum, error) {
	var (
		sum     Sum
		unknown int
	)

	l.mu.Lock()
	running := make([]string, 0, len(l.running))

	for id := range l.running {
		running = append(running, id)
	}
	l.mu.Unlock()

	raw, _ := json.Marshal(running) //nolint:errcheck,errchkjson // a list of strings always encodes

	if err := l.db.QueryRowContext(ctx, query, string(raw), arg).Scan(&sum.Dollars, &unknown); err != nil {
		return Sum{}, fmt.Errorf("sum the spend in the ledger: %w", err)
	}

	sum.Unknown = unknown > 0

	return sum, nil
}

// CountCalls counts the lanes of an item: the executions whose call name starts with prefix and does not end a rerun. It leaves out the executions whose id is in skip.
func (l *Ledger) CountCalls(ctx context.Context, item, prefix string, skip []string) (int, error) {
	// The copy is never nil, so the list encodes as [] and not as null, which json_each reads as one NULL row that excludes every call.
	raw, _ := json.Marshal(append([]string{}, skip...)) //nolint:errcheck,errchkjson // a list of strings always encodes

	var count int

	err := l.db.QueryRowContext(ctx,
		`SELECT count(*) FROM spend WHERE item = ? AND instr(call_name, ?) = 1 AND instr(call_name, ?) = 0 AND call NOT IN (SELECT value FROM json_each(?))`,
		item, prefix, RerunMark, string(raw)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count the agent calls of item %s in the ledger: %w", item, err)
	}

	return count, nil
}
