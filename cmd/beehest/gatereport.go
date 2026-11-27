package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"
)

const (
	// reportMode is the mode of a report file that beehest writes into the real work folder.
	reportMode = 0o600
	// reportFolderMode is the mode of a folder that beehest makes in the real work folder for a report.
	reportFolderMode = 0o750
)

// errNotPlainReport is the reason that beehest does not carry a report file that is a link, a folder, or a file with more than one name.
var errNotPlainReport = errors.New("the file is not a plain file with one name, or a link leads to it, so beehest does not carry it")

// reportCarry carries the test reports that a gate command wrote in the copy of the work folder to the same names in the real work folder, where the
// gate reads them. The zero value carries nothing. Only a name that the copy did not hold before the command is carried, so a tracked file is never rewritten.
// Only a plain file with one name is carried. Both folders are opened as roots, so a symbolic link cannot lead a read or a write out of either one.
type reportCarry struct {
	pattern  string
	from, to string
	// before is the set of the names that the pattern matched in the copy before the command ran.
	before map[string]bool
}

// startCarry reads the names that the pattern matches in the copy in from before the command runs. A test_report that is not inside the work folder is
// refused. A gate with no test_report, and a gate that works in the real folder itself, carry nothing.
func startCarry(pattern, from, to string) (reportCarry, error) {
	if pattern == "" || from == to {
		return reportCarry{}, nil
	}

	if !filepath.IsLocal(pattern) {
		return reportCarry{}, fmt.Errorf("test_report %q is not inside the work folder. Use a path below the work folder, such as reports/*.xml", pattern)
	}

	carry := reportCarry{pattern: path.Clean(filepath.ToSlash(pattern)), from: from, to: to}

	err := withRoots(from, to, func(src, _ *os.Root) (err error) {
		carry.before, err = reportNames(src, carry.pattern)

		return err
	})
	if err != nil {
		return reportCarry{}, fmt.Errorf("read the test_report %q in the copy: %w", pattern, err)
	}

	return carry, nil
}

// withRoots opens both folders as roots, calls use with them and closes them.
func withRoots(from, to string, use func(src, dst *os.Root) error) error {
	src, err := os.OpenRoot(from)
	if err != nil {
		return fmt.Errorf("open the work folder for the test report: %w", err)
	}

	defer src.Close()

	dst, err := os.OpenRoot(to)
	if err != nil {
		return fmt.Errorf("open the work folder for the test report: %w", err)
	}

	defer dst.Close()

	return use(src, dst)
}

// reportNames returns the set of the names in root that the pattern matches.
func reportNames(root *os.Root, pattern string) (map[string]bool, error) {
	names, err := fs.Glob(root.FS(), pattern)
	if err != nil {
		return nil, err
	}

	found := make(map[string]bool, len(names))
	for _, name := range names {
		found[name] = true
	}

	return found, nil
}

// settle is the output and the code of a gate command after its reports are carried. Only a command that exited 0 has its reports carried, because the gate
// reads them only then. A report that cannot be carried fails the gate with the reason in the output.
func (c reportCarry) settle(out string, code int) (string, int) {
	if code != 0 {
		return out, code
	}

	if err := c.finish(); err != nil {
		return out + "beehest sandbox: " + err.Error() + "\n", sandboxFailed
	}

	return out, code
}

// finish writes each file that the pattern matches and that the command made new in the copy to the real folder. It reads only inside the copy and writes
// only inside the real folder.
func (c reportCarry) finish() error {
	if c.pattern == "" {
		return nil
	}

	return withRoots(c.from, c.to, func(src, dst *os.Root) error {
		after, err := reportNames(src, c.pattern)
		if err == nil {
			err = c.carryNew(src, dst, after)
		}

		return err
	})
}

// carryNew carries each name of after that the copy did not hold before the command.
func (c reportCarry) carryNew(src, dst *os.Root, after map[string]bool) error {
	for name := range after {
		if c.before[name] {
			continue
		}

		if err := carryFile(src, dst, name); err != nil {
			return fmt.Errorf("carry the test report %s to the work folder: %w", name, err)
		}
	}

	return nil
}

// carryFile copies the file name from src to dst, with the folders above it. It refuses a name that is not a plain file with one name, or that lies
// below a link.
func carryFile(src, dst *os.Root, name string) error {
	if !plainFile(src, name) {
		return errNotPlainReport
	}

	data, err := fs.ReadFile(src.FS(), name)
	if err != nil {
		return err
	}

	if err := dst.MkdirAll(path.Dir(name), reportFolderMode); err != nil {
		return err
	}

	return dst.WriteFile(name, data, reportMode)
}

// plainFile says whether name in root is a regular file that nothing else names. A hard link to a file outside the folder has a second name. The glob does
// not enter a folder that is a link, so no name below a link reaches this check.
func plainFile(root *os.Root, name string) bool {
	info, err := root.Lstat(name)

	return err == nil && info.Mode().IsRegular() && hasOneName(info)
}

// hasOneName says whether the file has no other name. A file that has no link count in its stat is not trusted.
func hasOneName(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)

	return ok && uint64(stat.Nlink) == 1 //nolint:unconvert // Nlink is 16 bits wide on macOS and 64 bits wide on Linux
}
