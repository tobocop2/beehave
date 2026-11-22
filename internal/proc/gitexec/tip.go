package gitexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/tobocop2/beehave/internal/proc"
)

// ErrNoTip is the error of a read of a branch tip that gives no commit.
var ErrNoTip = errors.New("the branch has no commit at the remote")

// absolute is the path of the git program, found once. Head code cannot change which program a later gate runs by writing a file earlier on the PATH.
var absolute = sync.OnceValues(func() (string, error) { return exec.LookPath(Binary) }) //nolint:gochecknoglobals // the one lookup of the git program, made once per process

// tipEnv is the environment of the read of a tip: no system or user git config, and no prompt for a password.
func tipEnv() []string {
	return []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0"}
}

// Tip returns the commit that refs/heads/branch names at remote. It runs ls-remote with the git program at its absolute path, in no repository, so no
// git config of a clone or a worktree reaches it. A remote with no such branch gives ErrNoTip.
func Tip(ctx context.Context, remote, branch string, timeout time.Duration) (string, error) {
	return tipWith(ctx, absolute, remote, branch, timeout)
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

// tipWith is Tip with the lookup of the git program passed in.
func tipWith(ctx context.Context, find func() (string, error), remote, branch string, timeout time.Duration) (string, error) {
	path, err := find()
	if err != nil {
		return "", fmt.Errorf("find the git program: %w", err)
	}

	ref := "refs/heads/" + branch

	out := proc.ExecRunner{Binary: path, Timeout: timeout, Env: tipEnv()}.Run(ctx, Args([]string{"ls-remote", "--", remote, ref}))
	if out.ExitCode != 0 {
		return "", fmt.Errorf("git ls-remote %s %s exited with code %d: %s", remote, ref, out.ExitCode, strings.TrimSpace(out.Stderr))
	}

	return commitOf(out.Stdout, ref, remote)
}

// commitOf finds the line of ref in the output of ls-remote and returns its commit, which must be a full commit id.
func commitOf(output, ref, remote string) (string, error) {
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
