package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"
)

const (
	// reportMode is the mode of a report file that beehave writes into the real work folder.
	reportMode = 0o600
	// reportFolderMode is the mode of a folder that beehave makes in the real work folder for a report.
	reportFolderMode = 0o750
)

// reportCarry carries the test reports that a gate command wrote in the copy of the work folder to the same names in the real work folder, where the
// gate reads them. The zero value carries nothing. Both folders are opened as roots, so a link in either one cannot lead a read or a write out of it.
type reportCarry struct {
	pattern  string
	from, to string
	// before is the stamp of each report file that the copy held before the command ran. An unchanged file is not carried, so a stale report stays stale.
	before map[string]reportStamp
}

// reportStamp is the modification time and the size of a report file.
type reportStamp struct {
	modTime time.Time
	size    int64
}

// startCarry reads the report files that the copy in from holds before the command runs. A test_report that is not inside the work folder is refused.
// A project with no test_report, and a gate that works in the real folder itself, carry nothing.
func startCarry(pattern, from, to string) (reportCarry, error) {
	if pattern == "" || from == to {
		return reportCarry{}, nil
	}

	if !filepath.IsLocal(pattern) {
		return reportCarry{}, fmt.Errorf("test_report %q is not inside the work folder, so the gate cannot read its report from the sandbox", pattern)
	}

	carry := reportCarry{pattern: path.Clean(filepath.ToSlash(pattern)), from: from, to: to}

	err := withRoots(from, to, func(src, _ *os.Root) (err error) {
		carry.before, err = reportStamps(src, carry.pattern)

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

// reportStamps returns the stamp of each regular file in root that the pattern matches. A link that leaves root is not followed.
func reportStamps(root *os.Root, pattern string) (map[string]reportStamp, error) {
	names, err := fs.Glob(root.FS(), pattern)
	if err != nil {
		return nil, err
	}

	found := map[string]reportStamp{}

	for _, name := range names {
		if info, err := fs.Stat(root.FS(), name); err == nil && info.Mode().IsRegular() {
			found[name] = reportStamp{modTime: info.ModTime(), size: info.Size()}
		}
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
		return out + "beehave sandbox: " + err.Error() + "\n", sandboxFailed
	}

	return out, code
}

// finish writes each report file that the command wrote or changed in the copy to the real folder. It reads only inside the copy and writes only inside the
// real folder, and only at the names that test_report matches.
func (c reportCarry) finish() error {
	if c.pattern == "" {
		return nil
	}

	return withRoots(c.from, c.to, func(src, dst *os.Root) error {
		after, err := reportStamps(src, c.pattern)
		if err == nil {
			err = c.carryChanged(src, dst, after)
		}

		return err
	})
}

// carryChanged carries each file of after that is new or whose stamp differs from the stamp before the command.
func (c reportCarry) carryChanged(src, dst *os.Root, after map[string]reportStamp) error {
	for name, stamp := range after {
		if before, seen := c.before[name]; seen && before == stamp {
			continue
		}

		if err := carryFile(src, dst, name); err != nil {
			return fmt.Errorf("write the test report %s to the work folder: %w", name, err)
		}
	}

	return nil
}

// carryFile copies the file name from src to dst, with the folders above it.
func carryFile(src, dst *os.Root, name string) error {
	data, err := fs.ReadFile(src.FS(), name)
	if err != nil {
		return err
	}

	if err := dst.MkdirAll(path.Dir(name), reportFolderMode); err != nil {
		return err
	}

	return dst.WriteFile(name, data, reportMode)
}
