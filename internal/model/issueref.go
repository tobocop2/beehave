package model

import (
	"cmp"
	"encoding/json"
	"strconv"
	"strings"
)

// IssueRef names an issue that a pull request closes: "#12" for an issue of the repository of the pull request, "owner/repo#12" (in lower case) for an issue of another repository.
type IssueRef string

// OwnIssue is the reference of an issue of the repository of the pull request.
func OwnIssue(number int) IssueRef { return IssueRef("#" + strconv.Itoa(number)) }

// ElsewhereIssue is the reference of an issue of another repository. Repository is "owner/repo"; its case does not count.
func ElsewhereIssue(repository string, number int) IssueRef {
	return IssueRef(strings.ToLower(repository) + "#" + strconv.Itoa(number))
}

// ParseIssue reads "#12" or "owner/repo#12". A number too large for int, and a text of any other form, give false. The case of the repository does not count.
func ParseIssue(text string) (IssueRef, bool) {
	repository, digits, found := strings.Cut(text, "#")

	number, err := strconv.Atoi(digits)
	if !found || err != nil {
		return "", false
	}

	if repository == "" {
		return OwnIssue(number), true
	}

	return ElsewhereIssue(repository, number), true
}

// CompareIssues orders references: the issues of the own repository first by number, then the issues of other repositories by repository and number.
func CompareIssues(a, b IssueRef) int {
	repoA, numberA := a.parts()
	repoB, numberB := b.parts()

	return cmp.Or(strings.Compare(repoA, repoB), cmp.Compare(numberA, numberB))
}

// parts splits a reference into its repository (empty for an own issue) and its number. A text that holds no number gives 0.
func (r IssueRef) parts() (string, int) {
	repository, digits, _ := strings.Cut(string(r), "#")

	number, err := strconv.Atoi(digits)
	if err != nil {
		return repository, 0
	}

	return repository, number
}

// UnmarshalJSON reads a reference as text, and a bare number as the issue of the own repository.
// A scope that an earlier run recorded holds numbers only.
func (r *IssueRef) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) == nil {
		*r = IssueRef(text)

		return nil
	}

	var number int
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}

	*r = OwnIssue(number)

	return nil
}
