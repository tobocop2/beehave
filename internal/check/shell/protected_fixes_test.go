package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/check/shell"
)

func protectedFixCases() []shellCase {
	return []shellCase{
		// A relative cd keeps the directory.
		{"a write after a relative cd is clean", "clean", "", "cd sub && touch f"},
		{"a redirect after a relative cd is clean", "clean", "", "cd sub && go test ./... > out.txt"},
		{"a build in a new folder is clean", "clean", "", "mkdir -p build && cd build && cmake .. > log.txt"},
		{"cd to the checkout by a relative path then a write is a hit", "hit", "", "cd ../../own/checkout && touch x"},
		{"cd up from the checkout then a write is a hit", "hit", "", "cd /own/checkout/sub; cd ..; touch x"},

		// Reads from the checkout.
		{"git clone of the checkout is clean", "clean", "", "git clone /own/checkout out"},
		{"git clone of the checkout with options is clean", "clean", "", "git clone --depth 1 -b main /own/checkout out"},
		{"git fetch of the checkout is clean", "clean", "", "git fetch /own/checkout"},
		{"git pull of the checkout is clean", "clean", "", "git pull /own/checkout master"},
		{"git remote add of the checkout is clean", "clean", "", "git remote add origin /own/checkout"},
		{"git worktree add of a commit from the checkout is clean", "clean", "", "git worktree add ../x /own/checkout"},
		{"tar create from the checkout is clean", "clean", "", "tar -cf a.tar -C /own/checkout ."},
		{"git archive of the checkout is clean", "clean", "", "git -C /own/checkout archive HEAD -o /work/step/a.tar"},
		{"git clone into the checkout is a hit", "hit", "", "git clone /work/step/src /own/checkout/c"},
		{"git clone with options into the checkout is a hit", "hit", "", "git clone --depth 1 /work/step/src /own/checkout/c"},
		{"git clone in the checkout by cd is a hit", "hit", "", "cd /own/checkout && git clone /work/step/src out"},
		{"git archive into the checkout is a hit", "hit", "", "git archive HEAD -o /own/checkout/a.tar"},
		{"git format-patch into the checkout is a hit", "hit", "", "git format-patch -o /own/checkout HEAD~1"},
		{"git bundle into the checkout is a hit", "hit", "", "git bundle create /own/checkout/b.bundle HEAD"},
		{"git diff output into the checkout is a hit", "hit", "", "git diff --output=/own/checkout/d.patch"},

		// Misses that agents produce.
		{"curl -o into the checkout is a hit", "hit", "", "curl -o /own/checkout/f https://example.test/f"},
		{"curl --output into the checkout is a hit", "hit", "", "curl --output /own/checkout/f https://example.test/f"},
		{"curl --output= into the checkout is a hit", "hit", "", "curl --output=/own/checkout/f https://example.test/f"},
		{"curl to a file in the step folder is clean", "clean", "", "curl -o f https://example.test/f"},
		{"wget -O into the checkout is a hit", "hit", "", "wget -O /own/checkout/f https://example.test/f"},
		{"unzip -d into the checkout is a hit", "hit", "", "unzip a.zip -d /own/checkout"},
		{"tar -x -C into the checkout is a hit", "hit", "", "tar -x -f a.tar -C /own/checkout"},
		{"tar xf -C into the checkout is a hit", "hit", "", "tar xf a.tar -C /own/checkout"},
		{"git apply --directory into the checkout is a hit", "hit", "", "git apply --directory=/own/checkout p.diff"},
		{"env -C the checkout then a write is a hit", "hit", "", "env -C /own/checkout touch f"},
		{"env --chdir the checkout then a write is a hit", "hit", "", "env --chdir=/own/checkout touch f"},
		{"env -C the checkout then a read is clean", "clean", "", "env -C /own/checkout ls"},
		{"export GIT_DIR then a commit is a hit", "hit", "", "export GIT_DIR=/own/checkout/.git; git commit -m x"},
		{"export GIT_WORK_TREE then an add is a hit", "hit", "", "export GIT_WORK_TREE=/own/checkout && git add a"},
		{"a GIT_DIR set before a read is clean", "clean", "", "export GIT_DIR=/own/checkout/.git; git log"},

		// Every branch of the target readers.
		{"a glob with no folder in the checkout is a hit", "hit", "", "cd /own/checkout && rm *"},
		{"a glob with no folder in the step folder is clean", "clean", "", "rm *"},
		{"git with no subcommand is clean", "clean", "", "git"},
		{"git with only options is clean", "clean", "", "git -C /own/checkout"},
		{"sed -i with -e reads every plain word as a file", "hit", "", "sed -i -e s/a/b/ /own/checkout/a.txt"},
		{"sed -i with no word is clean", "clean", "", "sed -i"},
		{"cp with no word is clean", "clean", "", "cp"},
		{"curl -o with the value joined is a hit", "hit", "", "curl -o/own/checkout/f https://example.test/f"},
		{"curl -o with no value is clean", "clean", "", "curl -o"},
		{"unzip -d with the value joined is a hit", "hit", "", "unzip a.zip -d/own/checkout"},
		{"wget -qO into the checkout is a hit", "hit", "", "wget -qO /own/checkout/f https://example.test/f"},
		{"wget -qO with no value in the step folder is clean", "clean", "", "wget -qO"},
		{"tar extract with no -C in the checkout is a hit", "hit", "", "cd /own/checkout && tar xf a.tar"},
		{"tar extract with no -C in the step folder is clean", "clean", "", "tar xf a.tar"},
		{"tar --extract -C into the checkout is a hit", "hit", "", "tar --extract -f a.tar -C /own/checkout"},
		{"tar --create into the checkout is a hit", "hit", "", "tar --create --file /own/checkout/a.tar ."},
		{"curl -O in the checkout is a hit", "hit", "", "cd /own/checkout && curl -O https://example.test/f"},
		{"curl -O in the step folder is clean", "clean", "", "curl -O https://example.test/f"},
		{"curl -O with --output-dir into the checkout is a hit", "hit", "", "curl -O --output-dir /own/checkout https://example.test/f"},
		{"wget with no option in the checkout is a hit", "hit", "", "cd /own/checkout && wget https://example.test/f"},
		{"wget with no option in the step folder is clean", "clean", "", "wget https://example.test/f"},
		{"unzip -l of an archive in the checkout is clean", "clean", "", "unzip -l /own/checkout/a.zip"},
		{"unzip with no -d in the checkout is a hit", "hit", "", "cd /own/checkout && unzip a.zip"},
		{"git init in the checkout is a hit", "hit", "", "git init /own/checkout/n"},
		{"git init with a separate git dir in the checkout is a hit", "hit", "", "git init --separate-git-dir /own/checkout/g new"},
		{"git init with no path in the step folder is clean", "clean", "", "git init"},
		{"git bundle verify is clean", "clean", "", "git bundle verify /own/checkout/b.bundle"},
		{"git worktree with no word is clean", "clean", "", "git worktree"},
		{"git worktree add after a double dash into the checkout is a hit", "hit", "", "git worktree add -- /own/checkout/w"},

		// A write target that cannot be read.
		{"a write by a substitution is a hit", "hit", "", "touch $(echo /own/checkout)/f"},
		{"a write by backticks is a hit", "hit", "", "touch `echo /own/checkout`/f"},
		{"a loop variable write is a hit", "hit", "", "cd /work/step; for f in a; do touch $f; done"},
		{"a redirect to an unknown variable is a hit", "hit", "", "echo x > $OUT"},
		{"a copy to an unknown variable is a hit", "hit", "", "cp a $DEST"},
		{"cd by a substitution is a hit", "hit", "", "cd $(mktemp -d)"},
		{"cd by an unknown variable is a hit", "hit", "", "cd \"$WHERE\""},
		{"a read of an unknown variable is clean", "clean", "", "cat $X"},
		{"a read of a substitution is clean", "clean", "", "cat $(echo a)"},
		{"a copy from an unknown variable is clean", "clean", "", "cp $SRC /work/step/a"},
		{"a sed script with a variable is clean", "clean", "", "sed -i \"s/a/$V/\" f.txt"},
	}
}

func TestScanWith_ProtectedPathFixes(t *testing.T) {
	t.Parallel()

	for _, tc := range protectedFixCases() {
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

func TestScanWith_ProtectedPathAfterARelativeCdResolvesAgainstTheNewFolder(t *testing.T) {
	t.Parallel()

	sub := shell.Protection{Dirs: []string{"/work/step/sub"}, Base: stepFolder}

	assert.True(t, hasKind(shell.ScanWith("cd sub && touch f", protectedKind, sub), "protected-path"), "f is /work/step/sub/f")
	assert.Empty(t, shell.ScanWith("cd sub && touch ../f", protectedKind, sub), "../f is /work/step/f")
	assert.Empty(t, shell.ScanWith("cd sub && cd .. && touch f", protectedKind, sub))
}

func TestScanWith_ProtectedPathSaysWhenAPathCannotBeResolved(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"touch $(echo x)/f", "echo x > $OUT", "cd $(mktemp -d)", "cd /work/step; git -C $X commit -m x"} {
		findings := shell.ScanWith(command, protectedKind, protectOwner())

		if assert.NotEmpty(t, findings, command) {
			assert.Contains(t, findings[0].Text, "cannot be resolved", command)
		}
	}

	known := shell.ScanWith("touch /own/checkout/a", protectedKind, protectOwner())

	if assert.NotEmpty(t, known) {
		assert.NotContains(t, known[0].Text, "cannot be resolved")
	}
}

func TestScanWith_ProtectedPathFoldsCaseOnlyForADirectoryOnAFileSystemThatIgnoresCase(t *testing.T) {
	t.Parallel()

	folds := func(string) bool { return true }
	keeps := func(string) bool { return false }

	for _, command := range []string{"touch /OWN/CHECKOUT/f", "touch ../../OWN/Checkout/f", "git -C /Own/checkout commit -m x", "cd /OWN/CHECKOUT && touch f", "echo x > /own/CHECKOUT/f"} {
		on := shell.ScanWith(command, protectedKind, shell.Protection{Dirs: []string{ownerCheckout}, Base: stepFolder, IgnoresCase: folds})
		off := shell.ScanWith(command, protectedKind, shell.Protection{Dirs: []string{ownerCheckout}, Base: stepFolder, IgnoresCase: keeps})

		assert.NotEmpty(t, on, "case ignored: %s", command)
		assert.Empty(t, off, "case kept: %s", command)
	}

	onlyOne := shell.Protection{Dirs: []string{ownerCheckout, "/other/dir"}, Base: stepFolder, IgnoresCase: func(dir string) bool { return dir == "/other/dir" }}

	assert.Empty(t, shell.ScanWith("touch /OWN/CHECKOUT/f", protectedKind, onlyOne), "the case of /own/checkout is kept")
	assert.NotEmpty(t, shell.ScanWith("touch /OTHER/Dir/f", protectedKind, onlyOne), "the case of /other/dir is ignored")
}

func TestScanWith_ProtectedPathCountsAWriteUnderTheHomeDirectoryBecauseItCannotBeResolved(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"touch ~/a", `touch "~/a"`} {
		findings := shell.ScanWith(command, protectedKind, protectOwner())

		if assert.NotEmpty(t, findings, command) {
			assert.Contains(t, findings[0].Text, "cannot be resolved", command)
		}
	}
}

func TestScanWith_ProtectedPathPlacesARelativeDirectoryUnderTheBase(t *testing.T) {
	t.Parallel()

	rel := shell.Protection{Dirs: []string{"checkout"}, Base: "/own"}

	assert.NotEmpty(t, shell.ScanWith("touch /own/checkout/a", protectedKind, rel))
	assert.Empty(t, shell.ScanWith("touch /own/other/a", protectedKind, rel))
}

func TestScanWith_ProtectedPathProbesTheFileSystemWhenNoFunctionIsGiven(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	findings := shell.ScanWith("touch "+dir+"/f", protectedKind, shell.Protection{Dirs: []string{dir}, Base: dir})

	assert.NotEmpty(t, findings)
	assert.Empty(t, shell.ScanWith("touch /nonexistent-root-zz/f", protectedKind, shell.Protection{Dirs: []string{"/nonexistent-root-zz/x"}, Base: dir}))
}
