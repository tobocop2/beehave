package host

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/tobocop2/beehave/internal/proc"
)

// FindRepos lists every repository under root, at any depth. find is the command line of find on the host.
// A repository is a directory that holds `.git`, or a bare repository: a directory that holds `HEAD`, `objects` and `refs`.
// A root that is a symlink is followed. A symlink inside the root is not, so a repository reached only through one is not listed.
// A find that fails, including one that cannot read a directory, and a root with no repository, is a refusal.
func FindRepos(ctx context.Context, find proc.Runner, root string) ([]string, error) {
	result := find.Run(ctx, append([]string{"-H", root}, repoQuery()...))
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("scan %s for repositories: exit %d: %s", root, result.ExitCode, strings.TrimSpace(result.Stderr))
	}

	var repos []string

	for _, found := range strings.Split(result.Stdout, "\x00") {
		if found != "" {
			repos = append(repos, path.Dir(found))
		}
	}

	if len(repos) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoRepos, root)
	}

	return repos, nil
}

// repoQuery is the find expression that prints each `.git` and the `objects` folder of each bare repository, with a NUL after each path.
// Find does not enter either one, so the object data of a repository is never scanned.
func repoQuery() []string {
	return []string{
		"(", "-name", ".git", "-prune", "-print0", ")", "-o",
		"(", "-type", "d", "-name", "objects", "-exec", "test", "-d", "{}/../refs", ";", "-exec", "test", "-f", "{}/../HEAD", ";", "-print0", "-prune", ")",
	}
}
