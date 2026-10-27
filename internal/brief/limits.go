package brief

import (
	"bytes"
	"errors"
	"os"
)

// ReadText returns the text of a file byte for byte: a carriage return stays and bad UTF-8 survives.
// A file that cannot be read, or that holds a NUL byte, is a refusal.
func ReadText(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the caller names the file: the rules, a slots file or a listed path
	if err != nil {
		// os.ReadFile wraps the cause in a path error that repeats the path.
		return "", refuse("cannot read %s: %s. Check that the file exists and that you can read it.", path, errors.Unwrap(err))
	}

	if bytes.IndexByte(data, 0) >= 0 {
		return "", refuse("%s holds a NUL byte. Rewrite it as plain text.", path)
	}

	return string(data), nil
}

// isFile reports whether path names a file, or a link to one.
func isFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

// ReadBytes returns the sum of the sizes of the files named, in bytes. A blank path is skipped.
// A path that is not a file is a refusal, because its size is unknown.
func ReadBytes(paths []string) (int, error) {
	total := 0

	for _, path := range paths {
		if path == "" {
			continue
		}

		if !isFile(path) {
			return 0, refuse("the required reading names %s, which is not a file here, so its size is unknown.", path)
		}

		text, err := ReadText(path)
		if err != nil {
			return 0, err
		}

		total += len(text)
	}

	return total, nil
}

// SizeGate refuses a brief whose required reading is above limit bytes: the brief itself plus the files under its read-first list.
func SizeGate(briefBytes, otherBytes, limit int) error {
	total := briefBytes + otherBytes
	if total <= limit {
		return nil
	}

	return refuse("the required reading is %d bytes (the brief %d, the files under '## Read first' %d), above %d bytes, about %d tokens. "+
		"Cut the list: a fix lane reads the plan, the handoff and the open findings, not the whole report.",
		total, briefBytes, otherBytes, limit, limit/BytesPerToken)
}

// CheckCap refuses a context cap that is not a whole number of tokens above zero.
func CheckCap(tokens int) error {
	if tokens > 0 {
		return nil
	}

	return refuse("the lane context cap is %d. It must be a whole number of tokens above zero.", tokens)
}
