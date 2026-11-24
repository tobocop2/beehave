package model

import (
	"fmt"
	"regexp"
	"strconv"
)

// linkPattern finds a link from a pull request body to an issue: a keyword, then #N, owner/repo#N or the URL of the issue.
var linkPattern = regexp.MustCompile(`(?i)\b(?:closes|fixes|resolves|part of|refs?|related(?: to)?)\b:?\s+(?:https://github\.com/([\w.-]+/[\w.-]+)/issues/|([\w.-]+/[\w.-]+)?#)(\d+)`)

// IssueLink is an issue that a pull request body names. An empty Repo is the repository of the pull request.
type IssueLink struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func (l IssueLink) String() string {
	return l.Repo + "#" + strconv.Itoa(l.Number)
}

// BodyLinks returns the issues the body names, once each, in the order of the body. A number that is not an int is an error that names the link.
func BodyLinks(body string) ([]IssueLink, error) {
	var found []IssueLink

	seen := map[IssueLink]bool{}

	for _, match := range linkPattern.FindAllStringSubmatch(body, -1) {
		number, err := strconv.Atoi(match[3])
		if err != nil {
			return nil, fmt.Errorf("cannot read the link %s#%s in the body: %w", match[1]+match[2], match[3], err)
		}

		next := IssueLink{Repo: match[1] + match[2], Number: number}
		if !seen[next] {
			seen[next] = true
			found = append(found, next)
		}
	}

	return found, nil
}
