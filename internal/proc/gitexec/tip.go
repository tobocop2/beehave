package gitexec

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoTip is the error of a read of a branch tip that gives no commit.
var ErrNoTip = errors.New("the branch has no commit at the remote")

// TipRef is the ref of branch at a remote, which git ls-remote lists.
func TipRef(branch string) string {
	return "refs/heads/" + branch
}

// TipOf returns the commit that the ref of branch names in output, the text of git ls-remote for remote. A listing with no line for the ref, or a line
// whose commit is not a full commit id, gives ErrNoTip.
func TipOf(output, remote, branch string) (string, error) {
	ref := TipRef(branch)

	for _, line := range strings.Split(output, "\n") {
		commit, name, found := strings.Cut(line, "\t")
		if found && name == ref && commit != "" {
			if !fullCommit(commit) {
				return "", fmt.Errorf("%w: %s at %s gives %q, which is not a full commit id", ErrNoTip, ref, remote, commit)
			}

			return commit, nil
		}
	}

	return "", fmt.Errorf("%w: %s at %s", ErrNoTip, ref, remote)
}

// fullCommit is true for a commit id of 40 lowercase hexadecimal digits. A shorter id is a prefix, and git reads a prefix as any commit that starts with it.
func fullCommit(commit string) bool {
	if len(commit) != commitLength {
		return false
	}

	for _, c := range commit {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// commitLength is the length of a commit id in text.
const commitLength = 40
