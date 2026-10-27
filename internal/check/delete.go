package check

import (
	"path"
	"slices"
	"strings"
)

func isDeleter(name string) bool {
	switch name {
	case "rm", "rmdir", "unlink":
		return true
	}

	return false
}

func isFindRunner(word string) bool {
	switch word {
	case "-exec", "-execdir", "-ok", "-okdir":
		return true
	}

	return false
}

// deleteHit reports whether the command, with its wrappers dropped, deletes files.
func deleteHit(head []string) bool {
	if len(head) == 0 {
		return false
	}

	if isDeleter(path.Base(head[0])) {
		return true
	}

	if path.Base(head[0]) != cmdFind {
		return false
	}

	for k, word := range head[1:] {
		if word == "-delete" || (isFindRunner(word) && k+2 < len(head) && isDeleter(path.Base(head[k+2]))) {
			return true
		}
	}

	return false
}

// stopsLaptop reports whether the delete names no path, or a path that is not a literal absolute path.
func stopsLaptop(head []string) bool {
	paths := deletePaths(head)

	return len(paths) == 0 || slices.ContainsFunc(paths, func(name string) bool { return !literalAbs(name) })
}

// literalAbs reports whether the path is absolute with no variable, glob, home, brace or parent part.
func literalAbs(name string) bool {
	return strings.HasPrefix(name, "/") && len(name) > 1 &&
		!strings.ContainsAny(name, "$`*?[]~{}") && !slices.Contains(strings.Split(name, "/"), "..")
}

// deletePaths returns the paths a delete command names; a `find` with no path gives none.
func deletePaths(head []string) []string {
	if path.Base(head[0]) == cmdFind {
		return findPaths(head[1:])
	}

	var out []string

	for _, word := range head[1:] {
		if !strings.HasPrefix(word, "-") || word == "-" {
			out = append(out, word)
		}
	}

	return out
}

// findPaths returns the words of a find command before its first test.
func findPaths(args []string) []string {
	for i, word := range args {
		if strings.HasPrefix(word, "-") || word == "(" || word == "!" {
			return args[:i]
		}
	}

	return args
}

// findRunsShell reports whether a find command hands each file to a shell, which hides the command it runs.
func findRunsShell(head []string) bool {
	if path.Base(head[0]) != cmdFind {
		return false
	}

	for k, word := range head {
		if isFindRunner(word) && k+1 < len(head) && isShell(path.Base(head[k+1])) {
			return true
		}
	}

	return false
}
