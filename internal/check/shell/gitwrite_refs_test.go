package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/check/shell"
	"github.com/tobocop2/beehave/internal/model"
)

func gitWriteRefCases() []shellCase {
	hit := func(command string) shellCase { return shellCase{command + " is a hit", "hit", "git-write", command} }
	clean := func(command string) shellCase { return shellCase{command + " is clean", "clean", "", command} }

	return []shellCase{
		clean("git stash list"), clean("git stash show"), clean("git stash show -p stash@{0}"),
		hit("git stash"), hit("git stash pop"), hit("git stash drop"), hit("git stash push -m x"),
		hit("git branch -D x"), hit("git branch -d x"), hit("git branch -m x y"), hit("git branch x"), hit("git branch --delete x"),
		clean("git branch"), clean("git branch --list"), clean("git branch -a"), clean("git branch -l 'f*'"), clean("git branch --contains abc"),
		hit("git tag v1"), hit("git tag -d v1"), hit("git tag -a v1 -m x"),
		clean("git tag"), clean("git tag -l"), clean("git tag --list 'v*'"),
		hit("git checkout main"), hit("git checkout -b x"), hit("git checkout -- a.txt"), hit("git switch x"), hit("git switch -c x"),
		hit("git config user.name x"), hit("git config --global user.name x"), hit("git config --unset user.name"),
		clean("git config --get user.name"), clean("git config --list"), clean("git config -l"), clean("git config user.name"),
		clean("git config get user.name"), clean("git config list"),
		hit("git config set user.name x"), hit("git config unset user.name"), hit("git config edit"), hit("git config -e"),
		hit("git remote add o url"), hit("git remote remove o"), hit("git remote set-url o url"),
		clean("git remote"), clean("git remote -v"), clean("git remote get-url o"),
		hit("git update-ref refs/heads/x abc"), hit("git commit-tree abc -m x"),
		hit("git notes add -m x"), hit("git notes remove"), clean("git notes list"), clean("git notes show"),
		hit("git init"), hit("git clone url dir"), hit("git submodule update --init"),
		clean("git submodule status"),
		clean("git worktree add ../copy abc"), clean("git worktree add --detach ../copy abc"), clean("git worktree list"),
		hit("git apply fix.patch"),
	}
}

func TestScanFor_GitWriteRefusesTheWritersOfRefsAndTrees(t *testing.T) {
	t.Parallel()

	for _, tc := range gitWriteRefCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := shell.ScanFor(tc.command, []model.FindingKind{model.FindingGitWrite})

			if tc.expect == expectClean {
				assert.Empty(t, findings, tc.command)

				return
			}

			assert.True(t, hasKind(findings, tc.kind), "%q: %v", tc.command, findings)
		})
	}
}
