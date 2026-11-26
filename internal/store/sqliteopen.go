package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	// The driver registers itself under the name sqlite, as it does for the workflow store.
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	// sqliteFileMode is the mode of a SQLite file of the state folder, and of its -wal and -shm files.
	sqliteFileMode = 0o600
	// primaryCodeMask keeps the primary result code of an extended SQLite result code.
	primaryCodeMask = 0xff
	// privateInfix joins the name of a file and a random text into the private name that a new file is made under.
	privateInfix = ".new-"
)

// SQLiteFile describes a SQLite file of the state folder and the schema it must hold.
// Name is the file name in the folder. Schema is a list of statements that each run again on a file that has them, and that sets user_version to Version.
// Owned names every table the schema creates: a new file that holds another name is not a file of beehave.
// BusyMs is the time that a connection waits for a lock.
type SQLiteFile struct {
	Name    string
	Schema  string
	Version int
	Owned   []string
	BusyMs  int
}

// OpenSQLite opens a SQLite file in dir and makes its tables when they are missing. The folder must exist.
// The file has mode 0600, and so have its -wal and -shm files, also when they exist already with a wider mode.
// It refuses a symbolic link at the path of the file, a file of another schema version, and a file of version 0 that holds tables of another owner.
// A new file is in WAL mode and holds its tables when it first appears. A file at the version of the build with every owned table opens with no write lock.
// Otherwise the version is read and the schema runs in one transaction, so two openers never run the schema between them.
func OpenSQLite(ctx context.Context, dir string, file SQLiteFile) (*sql.DB, error) {
	if err := secureFiles(ctx, dir, file); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", walDSN(filepath.Join(dir, file.Name), file.BusyMs))
	if err == nil {
		err = prepareSQLite(ctx, db, file)
		if err != nil {
			err = errors.Join(adviseDamaged(err, file.Name), db.Close())
		}
	}

	if err != nil {
		return nil, fmt.Errorf("cannot open %s: %w", file.Name, err)
	}

	return db, nil
}

// adviseDamaged adds the advice to move the file away when SQLite reports a damaged file or a file that is not a database. Every other error keeps its own cause.
func adviseDamaged(err error, name string) error {
	var sqliteErr *sqlite.Error

	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() & primaryCodeMask {
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			return fmt.Errorf("%w. If the file is damaged, move %s away and start again", err, name)
		}
	}

	return err
}

// walDSN returns the address of a file of the state folder: WAL mode, and transactions that take the write lock when they begin.
func walDSN(path string, busyMs int) string {
	return SQLiteDSN(path, busyMs, url.Values{"_pragma": {"journal_mode(WAL)"}, "_txlock": {"immediate"}})
}

// secureFiles makes the file when it is missing, and sets mode 0600 on it and on its -wal and -shm files. The folder is the root of every path, so no name leaves it.
func secureFiles(ctx context.Context, dir string, file SQLiteFile) error {
	root, err := os.OpenRoot(dir)
	if err == nil {
		err = errors.Join(placeFile(ctx, root, file), root.Close())
	}

	if err != nil {
		return fmt.Errorf("cannot create %s: %w", file.Name, err)
	}

	return nil
}

// placeFile makes the file when it is missing, refuses a folder at its path, and sets the modes.
func placeFile(ctx context.Context, root *os.Root, file SQLiteFile) error {
	info, err := root.Lstat(file.Name)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		err = makeWalFile(ctx, root, file)
	case err == nil && info.IsDir():
		err = fmt.Errorf("%s is a folder. Move it away, and beehave makes a new file", file.Name)
	}

	if err != nil {
		return err
	}

	return secureEach(root, file.Name)
}

// makeWalFile makes the file in WAL mode with its tables under a private name, and links it to its name. A file that another opener linked first is kept.
func makeWalFile(ctx context.Context, root *os.Root, file SQLiteFile) error {
	private := file.Name + privateInfix + rand.Text()

	made, err := root.OpenFile(private, os.O_CREATE|os.O_EXCL|os.O_WRONLY, sqliteFileMode)
	if err != nil {
		return err
	}

	err = errors.Join(made.Close(), preparePrivate(ctx, filepath.Join(root.Name(), private), file))
	if err == nil {
		err = linkOnce(root, private, file.Name)
	}

	return errors.Join(err, root.Remove(private))
}

// preparePrivate sets WAL mode on a file that no other opener can see and makes its tables. The close writes them into the file itself.
func preparePrivate(ctx context.Context, path string, file SQLiteFile) error {
	db, err := sql.Open("sqlite", walDSN(path, file.BusyMs))
	if err == nil {
		err = errors.Join(prepareSQLite(ctx, db, file), db.Close())
	}

	return err
}

// linkOnce links private to name. A name that exists keeps its file.
func linkOnce(root *os.Root, private, name string) error {
	if err := root.Link(private, name); !errors.Is(err, fs.ErrExist) {
		return err
	}

	return nil
}

// secureEach refuses a symbolic link at the file or at its -wal or -shm file, and sets mode 0600 on each. A file that is missing is skipped.
func secureEach(root *os.Root, name string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := root.Lstat(name + suffix)

		switch {
		case err == nil && info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link. Remove the link, or point the state folder at a folder that holds the file itself", name+suffix)
		case err == nil || errors.Is(err, fs.ErrNotExist):
			err = secure(root, name+suffix)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// secure sets mode 0600 on a file. A file that is gone, such as a -wal or -shm file that SQLite removed, needs no mode and is skipped.
func secure(root *os.Root, name string) error {
	if err := root.Chmod(name, sqliteFileMode); !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return nil
}

// prepareSQLite only reads a file that is up to date; otherwise it reads the version and runs the schema in one transaction, and refuses a file of another schema.
func prepareSQLite(ctx context.Context, db *sql.DB, file SQLiteFile) error {
	current, err := upToDate(ctx, db, file)
	if err != nil || current {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	err = checkSQLite(ctx, tx, file)
	if err == nil {
		_, err = tx.ExecContext(ctx, file.Schema)
	}

	if err != nil {
		return errors.Join(err, tx.Rollback())
	}

	return tx.Commit()
}

// checkSQLite refuses a file of a schema version that the build does not know, and a file of version 0 that holds a table that the schema does not name.
func checkSQLite(ctx context.Context, tx *sql.Tx, file SQLiteFile) error {
	var version int

	err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if err == nil && version != 0 && version != file.Version {
		err = fmt.Errorf("the file has schema version %d, and this build knows version %d. Use a build that knows version %d, or move %s away", version, file.Version, version, file.Name)
	} else if err == nil && version == 0 {
		err = refuseForeign(ctx, tx, file)
	}

	return err
}

// upToDate reports whether the file has the version of the build and every owned table. It reads in one statement and takes no write lock.
func upToDate(ctx context.Context, db *sql.DB, file SQLiteFile) (bool, error) {
	var version, tables int

	list, args := ownedList(file.Owned)
	query := `SELECT (SELECT user_version FROM pragma_user_version), (SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ` + list + `)`

	err := db.QueryRowContext(ctx, query, args...).Scan(&version, &tables)

	return err == nil && version == file.Version && tables == len(file.Owned), err
}

// ownedList returns a parenthesized list of placeholders for the owned names, and the names as arguments. Owned must not be empty.
func ownedList(owned []string) (string, []any) {
	args := make([]any, len(owned))
	for i, name := range owned {
		args[i] = name
	}

	return `(?` + strings.Repeat(`, ?`, len(owned)-1) + `)`, args
}

// foreignQuery returns the query for the first object that is not in owned, and its arguments. Owned must not be empty.
func foreignQuery(owned []string) (string, []any) {
	list, args := ownedList(owned)

	return `SELECT name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND name NOT IN ` + list + ` ORDER BY name LIMIT 1`, args
}

// refuseForeign refuses a file that holds an object that the schema does not name.
func refuseForeign(ctx context.Context, tx *sql.Tx, file SQLiteFile) error {
	var foreign string

	query, args := foreignQuery(file.Owned)

	err := tx.QueryRowContext(ctx, query, args...).Scan(&foreign)
	if err == nil {
		return fmt.Errorf("the file holds %q, which beehave does not own. Move %s away, and beehave makes a new file", foreign, file.Name)
	}

	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}

	return err
}
