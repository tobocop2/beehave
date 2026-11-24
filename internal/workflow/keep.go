package workflow

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	// keepWord starts an owner line that keeps issues. It takes any case.
	keepWord = "keep"
	// repoNameChars are the characters of an owner and of a repository name in an issue of another repository, after the change to lower case.
	repoNameChars = "abcdefghijklmnopqrstuvwxyz0123456789_.-"
	decimalDigits = "0123456789"
)

// KeepIssues returns the issues of an owner line in the form "keep #N[, owner/repo#N ...]", and nil for any other text.
func KeepIssues(text string) []model.IssueRef {
	rest, ok := afterKeepWord(text)
	if !ok {
		return nil
	}

	parts := strings.Split(rest, ",")
	issues := make([]model.IssueRef, 0, len(parts))

	for _, part := range parts {
		issue, ok := keptIssue(strings.TrimSpace(part))
		if !ok {
			return nil
		}

		issues = append(issues, issue)
	}

	return issues
}

// afterKeepWord returns the text after the word keep at the start of the trimmed line, and false when the line does not start with the word and a space.
func afterKeepWord(text string) (string, bool) {
	line := strings.TrimSpace(text)
	if !strings.HasPrefix(strings.ToLower(line), keepWord) {
		return "", false
	}

	rest := line[len(keepWord):]

	return rest, rest != strings.TrimLeftFunc(rest, unicode.IsSpace)
}

// keptIssue reads "#N" or "owner/repo#N" with a decimal N that fits in 64 bits. The repository is read in lower case.
func keptIssue(text string) (model.IssueRef, bool) {
	repo, digits, found := strings.Cut(text, "#")
	if !found || digits == "" || strings.Trim(digits, decimalDigits) != "" {
		return "", false
	}

	number, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return "", false
	}

	if repo == "" {
		return model.OwnIssue(int(number)), true
	}

	owner, name, slash := strings.Cut(strings.ToLower(repo), "/")
	if !slash || owner == "" || name == "" || strings.Trim(owner+name, repoNameChars) != "" {
		return "", false
	}

	return model.ElsewhereIssue(repo, int(number)), true
}
