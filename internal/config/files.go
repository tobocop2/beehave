package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/tobocop2/obee/internal/hclfile"
)

const (
	// runnersFileName is the file with the agent logins, the agent limit and the pool caps.
	runnersFileName = "runners.hcl"
	// projectsDirName is the folder with one HCL file for each project.
	projectsDirName = "projects"
	// projectExt is the file extension of a project file.
	projectExt = ".hcl"
	// LocalHost is the build host that means this machine.
	LocalHost = "local"
)

// The value goes into an ssh command, so a cluster name is a letter, then letters, digits, hyphens.
var clusterName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

func isFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

// decodeFile reads an HCL file into target and reports each problem with its position.
func decodeFile(path string, target any) error {
	if err := hclfile.Decode(path, target); err != nil {
		return refusal{err: err}
	}

	return nil
}

// refuseAt returns a refusal that starts with the file, line and column of at.
// An %w cause stays reachable with errors.Is.
func refuseAt(at hcl.Range, format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	diag := hclfile.At(at, err.Error())
	diag.Cause = errors.Unwrap(err)

	return refusal{err: diag}
}

// keyAt returns the range of a key. A key that is not in the file has no range, so the range of
// the block that should hold it stands in.
func keyAt(key, holder hcl.Range) hcl.Range {
	if key.Filename == "" {
		return holder
	}

	return key
}

func requireText(at hcl.Range, key, value string) error {
	if strings.TrimSpace(value) == "" {
		return refuseAt(at, "`%s` must be a non-empty string. Set it.", key)
	}

	return nil
}

// expandHome replaces a leading "~" with the home folder.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", refuse("cannot expand `%s`: %w. Set HOME, or write the full path.", path, err)
	}

	return filepath.Join(home, path[1:]), nil
}
