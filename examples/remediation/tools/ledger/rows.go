package ledger

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/tobocop2/obee/internal/filelock"
)

const (
	fileMode      fs.FileMode = 0o600
	lockSuffix                = ".lock"
	publishSuffix             = ".next"
)

// Rows is a table of rows, each a list of columns. List reads every row. Append writes the text, which holds whole rows.
// Publish adds the text in one step: a reader sees the rows with all of it or with none of it.
// Hold runs a function while no other holder or sharer of the same table runs one. Share runs a function while no holder runs one; sharers run together.
type Rows interface {
	List() ([][]string, error)
	Append(text string) error
	Publish(text string) error
	Hold(run func() error) error
	Share(run func() error) error
}

// Table is a Rows over a tab-separated file. Hold and Share take a lock on a file beside it.
type Table struct {
	path string
}

// NewTable returns the table in the file at path. The file need not exist: it has no rows.
func NewTable(path string) Table {
	return Table{path: path}
}

// List reads the file byte for byte, so a carriage return stays in the last column of a row.
func (t Table) List() ([][]string, error) {
	data, err := os.ReadFile(t.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err = fail("read", t.path, err); err != nil {
		return nil, err
	}

	return splitRows(string(data)), nil
}

// splitRows cuts the text into rows at each newline, and each row into columns at each tab. An empty text has no rows.
func splitRows(text string) [][]string {
	if text == "" {
		return nil
	}

	records := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	rows := make([][]string, len(records))

	for i, record := range records {
		rows[i] = strings.Split(record, "\t")
	}

	return rows
}

// Append adds the text at the end of the file and creates the file at mode 0600.
func (t Table) Append(text string) error {
	file, err := os.OpenFile(t.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return fail("write", t.path, err)
	}

	_, writeErr := file.WriteString(text)

	return fail("write", t.path, errors.Join(writeErr, file.Close()))
}

// Publish writes the old rows and the text to a file beside the table and renames it over the table, so no reader sees part of the text.
// The caller holds the table, so the file beside it has one writer. A failed write leaves that file behind; the next Publish writes it again.
func (t Table) Publish(text string) error {
	old, err := os.ReadFile(t.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail("read", t.path, err)
	}

	next := t.path + publishSuffix

	err = os.WriteFile(next, append(old, text...), fileMode) //nolint:gosec // the path is the ledger file the caller named, plus a fixed suffix
	if err == nil {
		err = os.Rename(next, t.path)
	}

	return fail("write", t.path, err)
}

// Hold runs the function under an exclusive lock that the system drops when the process ends.
func (t Table) Hold(run func() error) error {
	return t.locked(filelock.Exclusive, run)
}

// Share runs the function under a shared lock: other sharers run with it, and a holder does not.
func (t Table) Share(run func() error) error {
	return t.locked(filelock.Shared, run)
}

func (t Table) locked(take func(path string) (*filelock.Lock, error), run func() error) error {
	lockPath := t.path + lockSuffix

	held, err := take(lockPath)
	if err != nil {
		return fail("lock", lockPath, err)
	}

	return errors.Join(run(), held.Release())
}

// fail returns a refusal that names the file and the cause, or nil for a nil error.
func fail(verb, path string, err error) error {
	if err == nil {
		return nil
	}

	var pathErr *fs.PathError

	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}

	return refuse("cannot %s %s: %v", verb, path, err)
}
