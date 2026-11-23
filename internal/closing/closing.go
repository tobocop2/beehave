// Package closing reads the closing keywords of GitHub in a text.
package closing

import (
	"regexp"
	"strings"
)

// keyword finds a closing keyword of GitHub in a text and the issue it names, as "#N", "owner/repo#N" or an issue URL.
// The keywords are the nine that GitHub documents, in any case, with an optional colon:
// https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue
// A plain "#N" with no keyword closes nothing, so it does not match.
var keyword = regexp.MustCompile(`(?i)\b(?:close|closes|closed|fix|fixes|fixed|resolve|resolves|resolved):?\s+((?:[a-z0-9_.-]+/[a-z0-9_.-]+)?#\d+|https?://github\.com/[a-z0-9_.-]+/[a-z0-9_.-]+/issues/\d+)`)

// References lists the issues that a text closes with a closing keyword, each as "#N" or "owner/repo#N", in the order of the text.
func References(text string) []string {
	var refs []string

	for _, found := range keyword.FindAllStringSubmatch(text, -1) {
		refs = append(refs, issueRef(found[1]))
	}

	return refs
}

// issueRef writes an issue URL as "owner/repo#N" and returns any other reference unchanged.
func issueRef(found string) string {
	path, ok := strings.CutPrefix(strings.ToLower(found), "https://github.com/")
	if !ok {
		path, ok = strings.CutPrefix(strings.ToLower(found), "http://github.com/")
	}

	if !ok {
		return found
	}

	repo, number, _ := strings.Cut(path, "/issues/")

	return repo + "#" + number
}
