package brief

import (
	"regexp"
	"strings"
)

const (
	slotPrefix = "<S>/"
	pathPrefix = " \t`\"'([=<>"
)

var pathPattern = regexp.MustCompile(`(<S>/|/)[A-Za-z0-9._/@+~-]+`)

// namesIn returns each file path a text names: an absolute path, and an `<S>/` path.
// A path starts at the start of a line or after a blank, a quote, a backtick, a bracket or an equals sign, so the second half of "and/or" and the path of a URL are not paths.
func namesIn(text, root string) []string {
	var found []string

	for _, line := range awkLines(text) {
		found = append(found, namesInLine(line, root)...)
	}

	return found
}

func namesInLine(line, root string) []string {
	var found []string

	for at := pathPattern.FindStringIndex(line); at != nil; at = pathPattern.FindStringIndex(line) {
		path, before := line[at[0]:at[1]], byte(' ')
		if at[0] > 0 {
			before = line[at[0]-1]
		}

		line = line[at[1]:]

		if strings.IndexByte(pathPrefix, before) < 0 {
			continue
		}

		if path = resolve(strings.TrimRight(path, "./"), root); path != "" && !strings.HasPrefix(path, "//") {
			found = append(found, path)
		}
	}

	return found
}

// resolve puts the round folder in place of the `<S>` of a path. With no folder, the path keeps a question mark in front.
func resolve(path, root string) string {
	if !strings.HasPrefix(path, slotPrefix) {
		return path
	}

	if root == "" {
		return "?" + path
	}

	return root + path[len("<S>"):]
}
