package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/tobocop2/obee/internal/proc"
)

const (
	pgidFileName = "agents.pgid"
	pgidFileMode = 0o600
	rowFields    = 2
	rowSeparator = "\t"
)

// groupRow is one agent process group: its id, the start time of its leader and the id of the run that owns it.
type groupRow struct {
	pgid    int
	started string
	run     string
}

// groups records the process group of each running agent in a file in the state folder.
// The file lets the next worker kill the groups that a crashed worker left.
type groups struct {
	mu   sync.Mutex
	path string
	rows []groupRow
}

// add records the group of an agent of a run that started with the given pid. An agent that already ended leaves no row.
func (g *groups) add(ctx context.Context, run string, pid int) error {
	started, err := proc.StartTime(ctx, pid)
	if err != nil {
		return fmt.Errorf("read the start time of agent process %d: %w", pid, err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.rows = append(g.rows, groupRow{pgid: pid, started: started, run: run})

	return g.write()
}

// release forgets every group of the run and kills none: the end of the claim context kills a live agent, and the runner kills the leftovers of one that exited.
// The file rows are crash cleanup only. It never reads an item: two runs of one item own different groups.
func (g *groups) release(run string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	kept := g.rows[:0]

	for _, row := range g.rows {
		if row.run != run {
			kept = append(kept, row)
		}
	}

	g.rows = kept

	return g.write()
}

// killAll kills every group this process recorded.
func (g *groups) killAll() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, row := range g.rows {
		// ESRCH means the group has no member left.
		_ = proc.KillGroupID(row.pgid) //nolint:errcheck // nothing is left to kill
	}

	g.rows = nil

	return g.write()
}

// write replaces the file with the rows in memory.
func (g *groups) write() error {
	var text strings.Builder

	for _, row := range g.rows {
		text.WriteString(strconv.Itoa(row.pgid) + rowSeparator + row.started + "\n")
	}

	if err := os.WriteFile(g.path, []byte(text.String()), pgidFileMode); err != nil {
		return fmt.Errorf("write the agent process file: %w", err)
	}

	return nil
}

// reap kills the groups that an earlier worker left in the file, then empties the file.
// A group whose leader runs with another start time belongs to an unrelated process that reused the id: it stays.
func (g *groups) reap(ctx context.Context) (int, error) {
	data, err := os.ReadFile(g.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}

	if err != nil {
		return 0, fmt.Errorf("read the agent process file: %w", err)
	}

	killed := reapRows(ctx, string(data))

	g.mu.Lock()
	defer g.mu.Unlock()

	return killed, g.write()
}

// reapRows kills the group of each line of the file text and counts the groups it killed.
func reapRows(ctx context.Context, text string) int {
	killed := 0

	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if reapRow(ctx, line) {
			killed++
		}
	}

	return killed
}

// reapRow kills the group that one file line names and reports whether it did. A line that does not parse is skipped.
func reapRow(ctx context.Context, line string) bool {
	fields := strings.SplitN(line, rowSeparator, rowFields)
	if len(fields) != rowFields {
		return false
	}

	pgid, err := strconv.Atoi(fields[0])
	if err != nil || pgid <= 0 {
		return false
	}

	if started, err := proc.StartTime(ctx, pgid); err == nil && started != fields[1] {
		return false
	}

	return proc.KillGroupID(pgid) == nil
}

// groupsFile returns the path of the process file in a state folder.
func groupsFile(stateDir string) string {
	return filepath.Join(stateDir, pgidFileName)
}
