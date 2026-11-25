package store

import (
	"context"
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
// The version is read and the schema runs in one transaction, so two openers never run the schema between them.
func OpenSQLite(ctx context.Context, dir string, file SQLiteFile) (*sql.DB, error) {
	if err := secureFiles(dir, file.Name); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", SQLiteDSN(filepath.Join(dir, file.Name), file.BusyMs, url.Values{"_pragma": {"journal_mode(WAL)"}, "_txlock": {"immediate"}}))
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

// secureFiles makes the file when it is missing, and sets mode 0600 on it and on its -wal and -shm files. The folder is the root of every path, so no name leaves it.
func secureFiles(dir, name string) error {
	root, err := os.OpenRoot(dir)
	if err == nil {
		err = errors.Join(secureEach(root, name), root.Close())
	}

	if err != nil {
		return fmt.Errorf("cannot create %s: %w", name, err)
	}

	return nil
}

// secureEach is secureFiles on an open folder. A -wal or -shm file that is missing is skipped.
func secureEach(root *os.Root, name string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := root.Lstat(name + suffix)

		switch {
		case err == nil && info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link. Remove the link, or point the state folder at a folder that holds the file itself", name+suffix)
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return err
		}

		if err = secure(root, name+suffix, suffix == ""); err != nil {
			return err
		}
	}

	return nil
}

// secure sets mode 0600 on a file. It makes the file first when create is true and the file is missing. A file that is not made and is gone, such as a -wal or -shm file that SQLite removed, needs no mode and is skipped.
func secure(root *os.Root, name string, create bool) error {
	var err error

	if create {
		var file *os.File

		if file, err = root.OpenFile(name, os.O_CREATE|os.O_RDWR, sqliteFileMode); err == nil {
			err = file.Close()
		}
	}

	if err == nil {
		err = root.Chmod(name, sqliteFileMode)
	}

	if !create && errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return err
}

// prepareSQLite creates the tables of a new file and refuses a file that is not of this schema. The read of the version and the schema share one transaction.
func prepareSQLite(ctx context.Context, db *sql.DB, file SQLiteFile) error {
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

// foreignQuery returns the query for the first object that is not in owned, and its arguments. Owned must not be empty.
func foreignQuery(owned []string) (string, []any) {
	args := make([]any, len(owned))
	for i, name := range owned {
		args[i] = name
	}

	return `SELECT name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND name NOT IN (?` + strings.Repeat(`, ?`, len(owned)-1) + `) ORDER BY name LIMIT 1`, args
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
