package audit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobocop2/beehave/internal/model"
)

// repo is a scratch git repository driven through the git command line. It is also the runner the audit reads through.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{t: t, dir: filepath.Join(t.TempDir(), "r")}
	if err := os.Mkdir(r.dir, 0o750); err != nil {
		t.Fatal(err)
	}

	r.git("init", "-q", "-b", "main")
	r.commit("base", map[string]string{"shared.txt": "base\n", "x.txt": "x\n"})

	return r
}

// Run answers the audit with the real git in the scratch repository.
func (r *repo) Run(ctx context.Context, args []string) model.CommandResult {
	cmd := r.command(ctx, args...)

	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	code := 0

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			r.t.Fatal(err)
		}

		code = exit.ExitCode()
	}

	return model.CommandResult{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// command is git in the scratch repository, cut off from the git settings of the machine.
func (r *repo) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // test helper, fixed binary
	cmd.Dir = r.dir
	cmd.Env = []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "HOME=" + r.dir, "PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin"}

	return cmd
}

// identity is the git identity and settings of every call that writes.
func identity() []string {
	return []string{"-c", "user.name=t", "-c", "user.email=t@x", "-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}
}

func (r *repo) git(args ...string) string {
	r.t.Helper()

	out, err := r.command(context.Background(), append(identity(), args...)...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

func (r *repo) write(name, data string) {
	r.t.Helper()

	target := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		r.t.Fatal(err)
	}

	if err := os.WriteFile(target, []byte(data), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(message string, files map[string]string) string {
	r.t.Helper()

	for name, data := range files {
		r.write(name, data)
	}

	r.git("add", "-A")
	r.git("commit", "-q", "-m", message)

	return r.git("rev-parse", "HEAD")
}

func (r *repo) branch(name string) { r.git("checkout", "-q", "-b", name, "main") }

func (r *repo) checkout(name string) { r.git("checkout", "-q", name) }

// merge merges without a fast-forward. The edits are written into the merge commit itself.
func (r *repo) merge(branch, message string, edits map[string]string) string {
	r.t.Helper()

	// Exit 1 is a conflict the edits resolve.
	_ = r.command(context.Background(), append(identity(), "merge", "--no-ff", "--no-commit", branch)...).Run() //nolint:errcheck // see above

	for name, data := range edits {
		r.write(name, data)
	}

	r.git("add", "-A")
	r.git("commit", "-q", "-m", message)

	return r.git("rev-parse", "HEAD")
}

func (r *repo) short(rev string) string { return r.git("log", "--format=%h", "-1", rev) }

// sideAndMain leaves main and a side branch each with one commit of their own, main checked out.
func (r *repo) sideAndMain() {
	r.branch("side")
	r.commit("side work", map[string]string{"side.txt": "s\n"})
	r.checkout("main")
	r.commit("main work", map[string]string{"main.txt": "m\n"})
}

// pullMerge merges a branch that adds the files into main.
func (r *repo) pullMerge(name string, files map[string]string, subject string) string {
	r.t.Helper()
	r.checkout("main")
	r.branch(name)
	r.commit(name+" work", files)
	r.checkout("main")
	r.commit("main "+name, map[string]string{"on_" + name + ".txt": "m\n"})

	if subject == "" {
		subject = "Merge pull request #12 from o/" + name
	}

	return r.merge(name, subject, nil)
}

func texts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = line.String()
	}

	return out
}
