package batch

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

const (
	minGitMajor = 2
	minGitMinor = 38
	decimal     = 10
	// exitConflict is the exit code of git merge-tree when the merge has conflicts.
	exitConflict = 1
	// exitNoObject and exitFatal are the exit codes of git cat-file -e for an object that is missing or not a valid name.
	exitNoObject = 1
	exitFatal    = 128
)

var (
	versionPattern = regexp.MustCompile(`(\d{1,6})\.(\d{1,6})`)
	treePattern    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// CheckGit fails when the git on the gate host is older than 2.38, the first version with merge-tree --write-tree.
func CheckGit(ctx context.Context, g Git) error {
	text, err := g.Version(ctx)
	if err != nil {
		return err
	}

	found := versionPattern.FindStringSubmatch(text)
	if found == nil {
		return fmt.Errorf("git 2.38 or newer is needed for batch_merge; git --version printed %q. Upgrade git on the gate host", text)
	}

	major, minor := number(found[1]), number(found[2])

	if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return fmt.Errorf("git 2.38 or newer is needed for batch_merge; found %s.%s. Upgrade git on the gate host", found[1], found[2])
	}

	return nil
}

// number reads a run of up to six digits, which the version pattern guarantees.
func number(digits string) int {
	value := 0

	for _, digit := range digits {
		value = value*decimal + int(digit-'0')
	}

	return value
}

// Exec runs git in the repository with the arguments, without the binary name. proc.Runner has this method.
type Exec interface {
	Run(ctx context.Context, args []string) model.CommandResult
}

// GitCLI is Git over the git command line.
type GitCLI struct {
	Exec Exec
}

// Version returns the text of git --version.
func (g GitCLI) Version(ctx context.Context) (string, error) {
	res := g.Exec.Run(ctx, []string{"--version"})
	if res.ExitCode != 0 {
		return "", failure("git --version", res)
	}

	return strings.TrimSpace(res.Stdout), nil
}

// HasCommit asks git cat-file whether the id names a commit that the repository holds.
// Exit code 1 and 128 mean no. Any other code is an error.
func (g GitCLI) HasCommit(ctx context.Context, id string) (bool, error) {
	res := g.Exec.Run(ctx, []string{"cat-file", "-e", id + "^{commit}"})

	switch res.ExitCode {
	case 0:
		return true, nil
	case exitNoObject, exitFatal:
		return false, nil
	default:
		return false, failure("git cat-file", res)
	}
}

// MergeTree merges theirs into ours without a working copy. Exit code 1 is a conflict, and the first output line is still a tree id.
func (g GitCLI) MergeTree(ctx context.Context, ours, theirs string) (MergeResult, error) {
	res := g.Exec.Run(ctx, []string{"merge-tree", "--write-tree", ours, theirs})
	if res.ExitCode != 0 && res.ExitCode != exitConflict {
		return MergeResult{}, failure("git merge-tree", res)
	}

	tree := strings.Split(res.Stdout, "\n")[0]
	if !treePattern.MatchString(tree) {
		return MergeResult{}, fmt.Errorf("git merge-tree printed %q, not a tree id", tree)
	}

	return MergeResult{Tree: tree, Conflict: res.ExitCode == exitConflict}, nil
}

// NewCommit makes a commit of the tree with the parents and prints its id. The caller supplies the author in the environment of Exec.
func (g GitCLI) NewCommit(ctx context.Context, tree string, parents ...string) (string, error) {
	args := []string{"commit-tree", tree, "-m", "obee batch union"}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}

	res := g.Exec.Run(ctx, args)
	if res.ExitCode != 0 {
		return "", failure("git commit-tree", res)
	}

	return strings.TrimSpace(res.Stdout), nil
}

func failure(what string, res model.CommandResult) error {
	return fmt.Errorf("%s failed with exit code %d: %s", what, res.ExitCode, strings.TrimSpace(res.Stderr))
}
