package github

import (
	"context"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	cmdTree = "gh api git/trees"
	idWidth = 40
	// comparisonWords is the number of words of a compared answer: the status, the ahead count and the behind count.
	comparisonWords = 3
)

// treeAnswer is the answer of the tree read. Truncated is true when the list holds less than the whole tree.
type treeAnswer struct {
	Truncated bool `json:"truncated"`
	Tree      []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"tree"`
}

// isID is true for a full object id: 40 lower-case hexadecimal digits. A name that is not one never reaches a URL.
func isID(name string) bool {
	return len(name) == idWidth && strings.Trim(name, "0123456789abcdef") == ""
}

// CommitTree returns the tree id of a commit.
func (f *Forge) CommitTree(ctx context.Context, commit string) (string, error) {
	if !isID(commit) {
		return "", forge.Refuse("%q is not a full commit id", commit)
	}

	tree, err := f.text(ctx, []string{argAPI, "repos/" + f.repo + "/git/commits/" + commit, argJQ, ".tree.sha"}, "cannot read the tree of commit "+commit)
	if err != nil {
		return "", err
	}

	if tree == "" {
		return "", forge.Refuse("cannot read the tree of commit %s: the answer holds no tree", commit)
	}

	return tree, nil
}

// compareFields is the jq program that turns a comparison into its status and both counts, in one line.
const compareFields = `[.status, .ahead_by, .behind_by] | map(tostring) | join(" ")`

// Compare reads how the forge compares commit with base: the status and both counts, from one call.
// The base is ahead of the commit by AheadBy commits, and the commit is ahead of the base by BehindBy commits.
// An answer with an unknown status, or a count that is not a number, is an error.
func (f *Forge) Compare(ctx context.Context, commit, base string) (model.Comparison, error) {
	if !isID(commit) {
		return model.Comparison{}, forge.Refuse("%q is not a full commit id", commit)
	}

	answer, err := f.text(ctx, []string{argAPI, "repos/" + f.repo + "/compare/" + commit + "..." + escapeSegments(base), argJQ, compareFields},
		"cannot compare commit "+commit+" with "+base)
	if err != nil {
		return model.Comparison{}, err
	}

	got, ok := parseComparison(answer)
	if !ok {
		return model.Comparison{}, forge.Refuse("cannot compare commit %s with %s: the answer holds no status and two counts", commit, base)
	}

	return got, nil
}

// parseComparison reads "<status> <ahead_by> <behind_by>". It refuses a status other than the four of the forge and a count that is not a whole number.
func parseComparison(answer string) (model.Comparison, bool) {
	words := strings.Fields(answer)
	if len(words) != comparisonWords {
		return model.Comparison{}, false
	}

	switch words[0] {
	case "ahead", "identical", "behind", "diverged":
	default:
		return model.Comparison{}, false
	}

	ahead, errAhead := strconv.Atoi(words[1])
	behind, errBehind := strconv.Atoi(words[2])

	if errAhead != nil || errBehind != nil || ahead < 0 || behind < 0 {
		return model.Comparison{}, false
	}

	return model.Comparison{Status: words[0], AheadBy: ahead, BehindBy: behind}, true
}

// TreeBlobs returns every file of a tree: its path and the id of its content. A folder is not listed. A tree too large for one answer is refused.
func (f *Forge) TreeBlobs(ctx context.Context, tree string) (map[string]string, error) {
	base := "cannot list the tree " + tree

	if !isID(tree) {
		return nil, forge.Refuse("%q is not a full tree id", tree)
	}

	done := f.run.Run(ctx, []string{argAPI, "repos/" + f.repo + "/git/trees/" + tree + "?recursive=1"})
	if done.ExitCode != 0 {
		return nil, forge.Refuse("%s", base)
	}

	answer, err := model.Decode[treeAnswer](cmdTree, done.Stdout)
	if err != nil {
		return nil, forge.Refusal(base, err)
	}

	return answer.files(base)
}

// files returns the path and content id of each entry that is not a folder. A list cut short is refused with the base text.
func (a treeAnswer) files(base string) (map[string]string, error) {
	if a.Truncated {
		return nil, forge.Refuse("%s: it is too large for one answer", base)
	}

	files := map[string]string{}

	for _, entry := range a.Tree {
		if entry.Type != "tree" {
			files[entry.Path] = entry.SHA
		}
	}

	return files, nil
}

// FailedLog returns the log of the failed steps of the check run at link. The job number is the last part of the link.
// It runs `gh run view --job <number> --log-failed`; the flags are in the manual at https://cli.github.com/manual/gh_run_view
func (f *Forge) FailedLog(ctx context.Context, link string) (string, error) {
	id := link[strings.LastIndex(link, "/")+1:]
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return "", forge.Refuse("the link %q does not end in a job number", link)
	}

	done := f.run.Run(ctx, []string{"run", argView, "--job", id, "--log-failed", argRepo, f.repo})
	if done.ExitCode != 0 {
		return "", forge.Refuse("cannot read the failed log of job %s", id)
	}

	return done.Stdout, nil
}
