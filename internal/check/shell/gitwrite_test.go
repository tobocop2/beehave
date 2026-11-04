package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/check/shell"
	"github.com/tobocop2/obee/internal/model"
)

func gitWriteCases() []shellCase {
	return []shellCase{
		{"git commit is a hit", "hit", "git-write", "git commit -m x"},
		{"git push is a hit", "hit", "git-write", "git push origin b"},
		{"git add is a hit", "hit", "git-write", "git add a.txt"},
		{"git apply is a hit", "hit", "git-write", "git apply fix.patch"},
		{"git reset behind -C is a hit", "hit", "git-write", "git -C /x reset --hard"},
		{"git restore is a hit", "hit", "git-write", "git restore a.txt"},
		{"git commit under env is a hit", "hit", "git-write", "env FOO=1 git commit -m x"},
		{"git push in a shell string is a hit", "hit", "git-write", "sh -c 'git push origin b'"},
		{"git commit after a pipe is a hit", "hit", "git-write", "echo x | git commit -F -"},
		{"git log is clean", "clean", "", "git log --oneline"},
		{"git diff is clean", "clean", "", "git diff HEAD~1"},
		{"git show is clean", "clean", "", "git show HEAD"},
		{"git commit in printed text is clean", "clean", "", "echo 'git commit' > /x/log"},
	}
}

func TestScanFor_GitWriteRefusesTheCommandsThatChangeARepository(t *testing.T) {
	t.Parallel()

	for _, tc := range gitWriteCases() {
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

func TestScanFor_GitWriteIsOffUnlessTheStepListsIt(t *testing.T) {
	t.Parallel()

	assert.Empty(t, shell.ScanFor("git commit -m x", []model.FindingKind{model.FindingBulkStaging}))
	assert.Empty(t, shell.Scan("git commit -m x"))
}

func TestScanFor_GitWriteRefusesSendPack(t *testing.T) {
	t.Parallel()

	assert.True(t, hasKind(shell.ScanFor("git send-pack git@h:o/r.git b:b", []model.FindingKind{model.FindingGitWrite}), "git-write"))
}

func TestScanFor_ReturnsOnlyTheListedKinds(t *testing.T) {
	t.Parallel()

	findings := shell.ScanFor("git add -A && git push --force", []model.FindingKind{model.FindingGitWrite})

	assert.True(t, hasKind(findings, "git-write"))
	assert.False(t, hasKind(findings, "bulk-staging"))
	assert.False(t, hasKind(findings, "git-bypass"))
}
