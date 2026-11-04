package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/check/shell"
)

func policyCases() []shellCase {
	return []shellCase{
		{"a commit with --author is a hit", "hit", "git-author", "git commit --author='A <a@x>' -m x"},
		{"a commit with a short --auth is a hit", "hit", "git-author", "git commit --auth=a -m x"},
		{"a -c user.name is a hit", "hit", "git-author", "git -c user.name=me commit -m x"},
		{"a -c user.email is a hit", "hit", "git-author", "git -c user.email=me@x commit -m x"},
		{"a config write of user.name is a hit", "hit", "git-author", "git config --global user.name me"},
		{"GIT_AUTHOR_NAME before a commit is a hit", "hit", "git-author", "GIT_AUTHOR_NAME=me git commit -m x"},
		{"GIT_COMMITTER_EMAIL exported is a hit", "hit", "git-author", "export GIT_COMMITTER_EMAIL=me@x"},
		{"a read of user.name is clean", "clean", "", "git config user.name"},
		{"a plain commit is clean", "clean", "", "git commit -m x"},

		{"git commit --no-verify is a hit", "hit", "git-bypass", "git commit --no-verify -m x"},
		{"git commit -n is a hit", "hit", "git-bypass", "git commit -n -m x"},
		{"git commit -nm is a hit", "hit", "git-bypass", "git commit -nm x"},
		{"git push --no-verify is a hit", "hit", "git-bypass", "git push --no-verify"},
		{"git push --force is a hit", "hit", "git-bypass", "git push --force origin b"},
		{"git push -f is a hit", "hit", "git-bypass", "git push -f"},
		{"git push --force-with-lease is a hit", "hit", "git-bypass", "git push --force-with-lease origin b"},
		{"git push with a plus refspec is a hit", "hit", "git-bypass", "git push origin +b"},
		{"git push --delete is a hit", "hit", "git-bypass", "git push origin --delete b"},
		{"git push -d is a hit", "hit", "git-bypass", "git push -d origin b"},
		{"git push with an empty-source refspec is a hit", "hit", "git-bypass", "git push origin :b"},
		{"git push --prune is a hit", "hit", "git-bypass", "git push --prune origin 'refs/heads/*:refs/heads/*'"},
		{"git push --mirror is a hit", "hit", "git-bypass", "git push --mirror origin"},
		{"git push of a src:dst refspec is clean", "clean", "", "git push origin b:b"},
		{"git send-pack --force is a hit", "hit", "git-bypass", "git send-pack --force git@h:o/r.git b"},
		{"git send-pack with a plus refspec is a hit", "hit", "git-bypass", "git send-pack git@h:o/r.git +b"},
		{"git send-pack with an empty-source refspec is a hit", "hit", "git-bypass", "git send-pack git@h:o/r.git :b"},
		{"git send-pack --mirror is a hit", "hit", "git-bypass", "git send-pack --mirror git@h:o/r.git"},
		{"git send-pack of a src:dst refspec is clean", "clean", "", "git send-pack git@h:o/r.git b:b"},
		{"gh api -X DELETE on a ref is a hit", "hit", "remote-delete", "gh api -X DELETE repos/o/r/git/refs/heads/b"},
		{"gh api --method DELETE on a ref is a hit", "hit", "remote-delete", "gh api repos/o/r/git/refs/heads/b --method DELETE"},
		{"gh api --method=delete on a ref is a hit", "hit", "remote-delete", "gh api --method=delete /repos/o/r/git/refs/tags/v1"},
		{"gh api -XDELETE on a ref is a hit", "hit", "remote-delete", "gh api -XDELETE repos/o/r/git/refs/heads/b"},
		{"gh api PATCH with force on a ref is a hit", "hit", "git-bypass", "gh api -X PATCH repos/o/r/git/refs/heads/b -f sha=abc -F force=true"},
		{"gh api --method PATCH with a force field on a ref is a hit", "hit", "git-bypass", "gh api repos/o/r/git/refs/heads/b --method PATCH --field force=true"},
		{"gh api PATCH with no force on a ref is clean", "clean", "", "gh api -X PATCH repos/o/r/git/refs/heads/b -f sha=abc"},
		{"gh api graphql deleteRef is a hit", "hit", "remote-delete", "gh api graphql -f query='mutation{deleteRef(input:{refId:\"x\"}){clientMutationId}}'"},
		{"gh api graphql updateRef is a hit", "hit", "git-bypass", "gh api graphql -f query='mutation{updateRef(input:{refId:\"x\",oid:\"y\",force:true}){clientMutationId}}'"},
		{"gh api graphql of a read is clean", "clean", "", "gh api graphql -f query='query{viewer{login}}'"},
		{"gh api DELETE on a branch is clean", "clean", "", "gh api -X DELETE repos/o/r/issues/comments/9"},
		{"gh api GET on a ref is clean", "clean", "", "gh api repos/o/r/git/refs/heads/b"},
		{"git merge --no-verify is a hit", "hit", "git-bypass", "git merge --no-verify b"},
		{"git rebase --no-verify is a hit", "hit", "git-bypass", "git rebase --no-verify main"},
		{"git stash is a hit", "hit", "git-bypass", "git stash"},
		{"git stash pop is a hit", "hit", "git-bypass", "git -C /x stash pop"},
		{"git config write is a hit", "hit", "git-bypass", "git config core.editor vi"},
		{"git config --unset is a hit", "hit", "git-bypass", "git config --unset core.editor"},
		{"git config --add is a hit", "hit", "git-bypass", "git config --add remote.origin.fetch x"},
		{"a -c core.hooksPath is a hit", "hit", "git-bypass", "git -c core.hooksPath=/dev/null commit -m x"},
		{"a -c remote mirror setting is a hit", "hit", "git-bypass", "git -c remote.origin.mirror=true push origin"},
		{"a -c remote push setting is a hit", "hit", "git-bypass", "git -c remote.origin.push=:b push origin"},
		{"a -c remote push setting of a dotted remote name is a hit", "hit", "git-bypass", "git -cremote.my.fork.push=:b push my.fork"},
		{"a -c remote url setting is clean", "clean", "", "git -c remote.origin.url=x fetch origin"},
		{"git push -n is clean", "clean", "", "git push -n origin b"},
		{"git push is clean", "clean", "", "git push origin b"},
		{"git config --get is clean", "clean", "", "git config --get core.editor"},
		{"git stash in a message is clean", "clean", "", "git commit -m 'git stash'"},

		{"BASH_ENV before a command is a hit", "hit", "env-override", "BASH_ENV=/x/f make check"},
		{"PATH before a command is a hit", "hit", "env-override", "PATH=/x:$PATH make"},
		{"export PATH is a hit", "hit", "env-override", "export PATH=/x:$PATH"},
		{"LD_PRELOAD under env is a hit", "hit", "env-override", "env LD_PRELOAD=/x/lib.so ls"},
		{"DYLD_INSERT_LIBRARIES is a hit", "hit", "env-override", "DYLD_INSERT_LIBRARIES=/x ls"},
		{"a redirect into .zshenv is a hit", "hit", "env-override", "echo 'alias a=b' >> ~/.zshenv"},
		{"tee into .bashrc is a hit", "hit", "env-override", "echo x | tee -a ~/.bashrc"},
		{"cp onto .profile is a hit", "hit", "env-override", "cp /x/p ~/.profile"},
		{"GOFLAGS before a build is clean", "clean", "", "GOFLAGS=-count=1 go test ./..."},
		{"a redirect into a log is clean", "clean", "", "make check > /x/log"},
		{"reading .bashrc is clean", "clean", "", "grep alias ~/.bashrc"},

		{"cat of .netrc is a hit", "hit", "config-read", "cat ~/.netrc"},
		{"head of an env file is a hit", "hit", "config-read", "head -3 /x/.env.local"},
		{"cat of a key file is a hit", "hit", "config-read", "cat /x/deploy.pem"},
		{"cat of the gh hosts file is a hit", "hit", "config-read", "cat ~/.config/gh/hosts.yml"},
		{"git config --list is a hit", "hit", "config-read", "git config --list"},
		{"git config -l is a hit", "hit", "config-read", "git config --global -l"},
		{"printenv alone is a hit", "hit", "config-read", "printenv"},
		{"env alone is a hit", "hit", "config-read", "env"},
		{"printenv of a named key is clean", "clean", "", "printenv HOME"},
		{"env that runs a command is clean", "clean", "", "env FOO=1 ls"},
		{"cat of a source file is clean", "clean", "", "cat /x/main.go"},
	}
}

func evasionCases() []shellCase {
	return []shellCase{
		{"git stash list is clean", "clean", "", "git stash list"},
		{"git stash show is clean", "clean", "", "git stash show -p"},
		{"git stash drop is a hit", "hit", "git-bypass", "git stash drop"},
		{"git stash push is a hit", "hit", "git-bypass", "git stash push -m x"},
		{"git stash apply is a hit", "hit", "git-bypass", "git stash apply"},

		{"an attached -c user.name is a hit", "hit", "git-author", "git -cuser.name=x commit -m a"},
		{"an attached -c core.hooksPath is a hit", "hit", "git-bypass", "git -ccore.hooksPath=/x commit -m a"},
		{"--config-env with user.name is a hit", "hit", "git-author", "git --config-env=user.name=FOO commit -m a"},
		{"--config-env as two words is a hit", "hit", "git-author", "git --config-env user.name=FOO commit -m a"},
		{"commit --reset-author is a hit", "hit", "git-author", "git commit --reset-author -m x"},
		{"an attached -c of another key is clean", "clean", "", "git -ccolor.ui=never status"},

		{"a bare PATH assignment is a hit", "hit", "env-override", "PATH=/x"},
		{"a bare PATH assignment after set -a is a hit", "hit", "env-override", "set -a; PATH=/x"},
		{"a bare LD_PRELOAD assignment is a hit", "hit", "env-override", "LD_PRELOAD=/x/l.so"},
		{"a bare other assignment is clean", "clean", "", "FOO=1"},
		{"a bare MYPATH assignment is clean", "clean", "", "MYPATH=/x"},

		{"export with no name is a hit", "hit", "config-read", "export"},
		{"export -p is a hit", "hit", "config-read", "export -p"},
		{"declare -p is a hit", "hit", "config-read", "declare -p"},
		{"a bare set is a hit", "hit", "config-read", "set"},
		{"cat of the process environment is a hit", "hit", "config-read", "cat /proc/self/environ"},
		{"grep of a process environment is a hit", "hit", "config-read", "grep . /proc/1234/environ"},
		{"git config list is a hit", "hit", "config-read", "git config list"},
		{"export of a name is clean", "clean", "", "export FOO=1"},
		{"export of a bare name is clean", "clean", "", "export FOO"},
		{"declare -p of a name is clean", "clean", "", "declare -p FOO"},
		{"set with an option is clean", "clean", "", "set -e"},
		{"cat of the process status is clean", "clean", "", "cat /proc/self/status"},
	}
}

func TestScanPolicyCases(t *testing.T) {
	t.Parallel()

	for _, tc := range append(policyCases(), evasionCases()...) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			findings := shell.Scan(tc.command)

			if tc.expect == expectClean {
				assert.Empty(t, findings, tc.command)

				return
			}

			assert.True(t, hasKind(findings, tc.kind), "%q: %v", tc.command, findings)
		})
	}
}
