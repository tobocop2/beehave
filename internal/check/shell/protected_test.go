package shell_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/check/shell"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	ownerCheckout = "/own/checkout"
	stepFolder    = "/work/step"
)

var protectedKind = []model.FindingKind{model.FindingProtected}

func protectOwner() shell.Protection {
	return shell.Protection{Dirs: []string{ownerCheckout}, Base: stepFolder}
}

func protectedCases() []shellCase {
	return []shellCase{
		{"a redirect into the checkout is a hit", "hit", "", "echo x > /own/checkout/a.txt"},
		{"an append into the checkout is a hit", "hit", "", "echo x >> /own/checkout/a.txt"},
		{"tee into the checkout is a hit", "hit", "", "echo x | tee /own/checkout/a.txt"},
		{"cp to the checkout is a hit", "hit", "", "cp /work/step/a /own/checkout/a"},
		{"cp -t to the checkout is a hit", "hit", "", "cp -t /own/checkout /work/step/a"},
		{"mv into the checkout is a hit", "hit", "", "mv /work/step/a /own/checkout/a"},
		{"mv out of the checkout is a hit", "hit", "", "mv /own/checkout/a /work/step/a"},
		{"rm of a literal path in the checkout is a hit", "hit", "", "rm /own/checkout/a.txt"},
		{"touch in the checkout is a hit", "hit", "", "touch /own/checkout/a.txt"},
		{"mkdir in the checkout is a hit", "hit", "", "mkdir -p /own/checkout/d"},
		{"sed -i in the checkout is a hit", "hit", "", "sed -i s/a/b/ /own/checkout/a.txt"},
		{"dd of= into the checkout is a hit", "hit", "", "dd if=/dev/zero of=/own/checkout/a bs=1 count=1"},
		{"a dot dot path into the checkout is a hit", "hit", "", "touch /own/checkout/../checkout/a"},
		{"a relative path into the checkout is a hit", "hit", "", "touch ../../own/checkout/a"},
		{"a glob under the checkout is a hit", "hit", "", "rm /own/checkout/*"},
		{"a glob over the checkout is a hit", "hit", "", "rm -rf /own/*"},
		{"a write in a subshell string is a hit", "hit", "", "sh -c 'touch /own/checkout/a'"},
		{"a write in eval is a hit", "hit", "", "eval 'touch /own/checkout/a'"},
		{"a write behind env is a hit", "hit", "", "env X=1 touch /own/checkout/a"},
		{"a write after a pipe is a hit", "hit", "", "ls | tee /own/checkout/a"},
		{"a write behind a variable the command sets is a hit", "hit", "", "D=/own/checkout; touch $D/a"},

		{"git -C the checkout commit is a hit", "hit", "", "git -C /own/checkout commit -m x"},
		{"git -C a folder in the checkout push is a hit", "hit", "", "git -C /own/checkout/sub push"},
		{"git -C the checkout checkout is a hit", "hit", "", "git -C /own/checkout checkout -b x"},
		{"git -C the checkout branch delete is a hit", "hit", "", "git -C /own/checkout branch -D x"},
		{"git -C the checkout worktree add is a hit", "hit", "", "git -C /own/checkout worktree add /work/step/t"},
		{"git -C the checkout fetch is a hit", "hit", "", "git -C /own/checkout fetch"},
		{"git -C the checkout with an alias is a hit", "hit", "", "git -C /own/checkout -c alias.x=status x"},
		{"git -C two steps into the checkout is a hit", "hit", "", "git -C /own -C checkout commit -m x"},
		{"git --git-dir in the checkout is a hit", "hit", "", "git --git-dir=/own/checkout/.git commit -m x"},
		{"git --work-tree in the checkout is a hit", "hit", "", "git --work-tree /own/checkout add a"},
		{"GIT_DIR in the checkout is a hit", "hit", "", "GIT_DIR=/own/checkout/.git git commit -m x"},
		{"git -C a path of an unknown variable is a hit", "hit", "", "git -C \"$WHERE\" commit -m x"},
		{"git in the checkout after cd is a hit", "hit", "", "cd /own/checkout && git commit -m x"},
		{"a write after cd into the checkout is a hit", "hit", "", "cd /own/checkout && touch a"},
		{"a write after cd and a relative cd is a hit", "hit", "", "cd /own/checkout; cd sub; touch a"},
		{"a write after cd from above is a hit", "hit", "", "cd /own && touch checkout/a"},
		{"a redirect after cd is a hit", "hit", "", "cd /own/checkout && echo x > a"},
		{"a write after cd in a shell string is a hit", "hit", "", "bash -c 'cd /own/checkout && rm a'"},
		{"a write after cd to an unknown path is a hit", "hit", "", "cd \"$WHERE\" && touch a"},

		{"a bare redirect into the checkout is a hit", "hit", "", "> /own/checkout/a"},
		{"a colon redirect into the checkout is a hit", "hit", "", ": > /own/checkout/a"},
		{"pushd into the checkout then a write is a hit", "hit", "", "pushd /own/checkout && touch a"},
		{"a write in a subshell after cd is a hit", "hit", "", "cd /own/checkout; (touch a)"},
		{"find delete in the checkout is a hit", "hit", "", "find /own/checkout -name x -delete"},
		{"find delete with no path after cd is a hit", "hit", "", "cd /own/checkout && find -name x -delete"},
		{"tar extract into the checkout is a hit", "hit", "", "tar -xf a.tar -C /own/checkout"},
		{"a redirect through a variable is a hit", "hit", "", "D=/own/checkout; echo x > $D/a"},
		{"a write with the git path of the program is a hit", "hit", "", "/usr/bin/touch /own/checkout/a"},
		{"git after cd into the checkout through a link is a hit", "hit", "", "cd /own/checkout/sub/.. && git push"},
		{"a command in backticks is read for its writes", "hit", "", "echo `touch /own/checkout/a`"},
		{"a command in a substitution is read for its writes", "hit", "", "echo $(touch /own/checkout/a)"},

		{"git worktree add into the checkout is a hit", "hit", "", "git worktree add /own/checkout/t"},
		{"git clone into the checkout is a hit", "hit", "", "git clone . /own/checkout/c"},
		{"git --git-dir relative to -C is a hit", "hit", "", "git -C /own --git-dir=checkout/.git commit -m x"},
		{"git diff of a path in the checkout is clean", "clean", "", "git diff /own/checkout/a /work/step/a"},

		{"cat of a file in the checkout is clean", "clean", "", "cat /own/checkout/a.txt"},
		{"grep in the checkout is clean", "clean", "", "grep -r x /own/checkout"},
		{"cp out of the checkout is clean", "clean", "", "cp /own/checkout/a /work/step/a"},
		{"git -C the checkout log is clean", "clean", "", "git -C /own/checkout log --oneline"},
		{"git -C the checkout status is clean", "clean", "", "git -C /own/checkout status"},
		{"git -C the checkout diff is clean", "clean", "", "git -C /own/checkout diff HEAD~1"},
		{"git -C the checkout branch list is clean", "clean", "", "git -C /own/checkout branch --list"},
		{"git -C the checkout worktree list is clean", "clean", "", "git -C /own/checkout worktree list"},
		{"git after cd into the checkout and a read is clean", "clean", "", "cd /own/checkout && git log"},
		{"a read after cd into the checkout is clean", "clean", "", "cd /own/checkout && cat a.txt && ls"},
		{"a write in the step folder is clean", "clean", "", "echo x > /work/step/a.txt"},
		{"a relative write in the step folder is clean", "clean", "", "touch a.txt"},
		{"git in the step folder is clean", "clean", "", "git -C /work/step commit -m x"},
		{"a sibling folder with the same start is clean", "clean", "", "touch /own/checkout-two/a"},
		{"git -C a sibling folder is clean", "clean", "", "git -C /own/checkout-two commit -m x"},
		{"cd out of the checkout before a write is clean", "clean", "", "cd /own/checkout && cd /work/step && touch a"},
		{"printed text that names the checkout is clean", "clean", "", "echo 'rm /own/checkout/a'"},
	}
}

func TestScanWith_ProtectedPathRefusesAWriteInAProtectedDirectoryAndLetsAReadPass(t *testing.T) {
	t.Parallel()

	for _, tc := range protectedCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := shell.ScanWith(tc.command, protectedKind, protectOwner())

			if tc.expect == expectClean {
				assert.Empty(t, findings, tc.command)

				return
			}

			assert.True(t, hasKind(findings, "protected-path"), "%q: %v", tc.command, findings)
		})
	}
}

func TestScanWith_ProtectedPathFindsNothingWhenItIsNotListedOrNamesNoDirectory(t *testing.T) {
	t.Parallel()

	command := "touch /own/checkout/a"

	assert.Empty(t, shell.ScanWith(command, []model.FindingKind{model.FindingBulkStaging}, protectOwner()))
	assert.Empty(t, shell.ScanWith(command, protectedKind, shell.Protection{Base: stepFolder}))
	assert.Empty(t, shell.ScanFor(command, protectedKind))
}

func TestScanWith_ProtectedPathResolvesASymbolicLinkBeforeItCompares(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	real := filepath.Join(root, "real")
	link := filepath.Join(root, "link")

	require.NoError(t, os.Mkdir(real, 0o700))
	require.NoError(t, os.Symlink(real, link))

	byLink := shell.ScanWith("touch "+link+"/a", protectedKind, shell.Protection{Dirs: []string{real}, Base: root})
	assert.True(t, hasKind(byLink, "protected-path"), "a write through a link to the protected directory: %v", byLink)

	byReal := shell.ScanWith("touch "+real+"/a", protectedKind, shell.Protection{Dirs: []string{link}, Base: root})
	assert.True(t, hasKind(byReal, "protected-path"), "a protected link, a write by the real path: %v", byReal)

	gitByLink := shell.ScanWith("git -C "+link+" commit -m x", protectedKind, shell.Protection{Dirs: []string{real}, Base: root})
	assert.True(t, hasKind(gitByLink, "protected-path"), "git through a link: %v", gitByLink)

	elsewhere := shell.ScanWith("touch "+root+"/other/a", protectedKind, shell.Protection{Dirs: []string{real}, Base: root})
	assert.Empty(t, elsewhere)
}

func TestScanWith_ProtectedPathResolvesARelativeBaseAgainstTheDirectoryOfTheCommand(t *testing.T) {
	t.Parallel()

	inside := shell.Protection{Dirs: []string{ownerCheckout}, Base: ownerCheckout}

	assert.True(t, hasKind(shell.ScanWith("touch a", protectedKind, inside), "protected-path"))
	assert.Empty(t, shell.ScanWith("cat a", protectedKind, inside))
}
