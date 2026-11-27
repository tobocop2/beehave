package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/proc/gitexec"
)

// projectGit is the git of the engine in the folders of one project: pinned to its clones when it has a repository, plain and local when it has none.
type projectGit struct {
	clones gitexec.Repositories
	plain  bool
}

// gitAndCommon is the number of lines that `git rev-parse --git-dir --git-common-dir` prints.
const gitAndCommon = 2

// gitOf returns the git of the engine in the folders of the named project, whose clones are in found. Only a project that projects holds with no repository is plain (see repositoryOf).
func gitOf(projects map[string]config.Project, name string, found gitexec.Repositories) projectGit {
	return projectGit{clones: found, plain: !repositoryOf(projects)(name)}
}

// Runner returns the runner of git in dir.
func (g projectGit) Runner(dir string, timeout time.Duration) gitexec.Runner {
	if g.plain {
		return gitexec.New(dir, timeout)
	}

	return g.clones.Runner(dir, timeout)
}

// copyRecord returns the git folders of the gate copy work: from the records of the clones, or from git in work for a project with no repository.
func (g projectGit) copyRecord(ctx context.Context, work string) (gitexec.Repository, error) {
	if !g.plain {
		return g.clones.Of(work)
	}

	result := g.Runner(work, 0).Run(ctx, []string{"rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir"})
	if result.ExitCode != 0 {
		return gitexec.Repository{}, fmt.Errorf("git rev-parse in %s: %s", work, strings.TrimSpace(result.Stderr))
	}

	dirs := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(dirs) != gitAndCommon {
		return gitexec.Repository{}, fmt.Errorf("git rev-parse in %s printed %q, not a git dir and a common dir", work, result.Stdout)
	}

	return gitexec.Repository{GitDir: dirs[0], CommonDir: dirs[1]}, nil
}
