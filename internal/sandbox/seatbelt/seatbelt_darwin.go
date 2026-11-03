//go:build darwin

package seatbelt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/sandbox"
)

// sandboxExec is the program that applies a profile to a command. It is at this path on every macOS.
const sandboxExec = "/usr/bin/sandbox-exec"

// signalBase is added to the signal number for the exit code of a command that a signal ended.
const signalBase = 128

// notFound is the exit code of a shell for a program that is not found.
const notFound = 127

// probeLimit is the time that a probe child may run.
const probeLimit = 30 * time.Second

// Enforcer runs a command under sandbox-exec. The field is the path of sandbox-exec, which a test replaces.
type Enforcer struct {
	program string
}

// New returns the Seatbelt enforcer.
func New() Enforcer { return Enforcer{program: sandboxExec} }

// Name returns Name.
func (Enforcer) Name() string { return Name }

// Check says whether sandbox-exec is here and applies a profile, and proves the confinement in two throw-away children: one runs a program that the plan
// lets it run, and one tries to write a file outside the plan and must fail. A system that refuses the profile, or a profile that does not confine,
// makes Check fail, so the gate is refused and never runs unconfined.
func (e Enforcer) Check() error {
	if _, err := os.Stat(e.program); err != nil {
		return fmt.Errorf("%w: %s is missing (Apple deprecated it, and a macOS without it cannot run this enforcer): %w", sandbox.ErrUnavailable, e.program, err)
	}

	if err := e.probe(); err != nil {
		return fmt.Errorf("%w: %w", sandbox.ErrUnavailable, err)
	}

	return nil
}

// probe runs a program that does nothing, and then a write outside the plan, under a plan that reads the system folders only.
func (e Enforcer) probe() error {
	ctx, cancel := context.WithTimeout(context.Background(), probeLimit)
	defer cancel()

	folder, err := os.MkdirTemp("", "obee-seatbelt-probe-")
	if err != nil {
		return fmt.Errorf("make the folder of the probe: %w", err)
	}
	defer os.RemoveAll(folder)

	return e.confines(ctx, folder)
}

// confines runs a program that the plan lets it run, and then a write to a file in folder, which the plan does not list.
func (e Enforcer) confines(ctx context.Context, folder string) error {
	code, out, err := e.try(ctx, "true")
	if err != nil {
		return fmt.Errorf("sandbox-exec cannot start a program under the profile: %w", err)
	}

	if code != 0 {
		return fmt.Errorf("sandbox-exec cannot run a program under the profile (exit %d): %s", code, out)
	}

	target := filepath.Join(folder, "outside")

	code, _, err = e.try(ctx, "sh", "-c", "echo x > "+target)
	if _, statErr := os.Stat(target); err != nil || code == 0 || statErr == nil {
		return errors.Join(fmt.Errorf("sandbox-exec does not refuse a write outside the plan, or cannot start a program under the profile (exit %d)", code), err)
	}

	return nil
}

// try runs a program of the system under a plan that reads the system folders only, and returns its exit code and what it printed.
func (e Enforcer) try(ctx context.Context, argv ...string) (code int, out string, err error) {
	var printed strings.Builder

	code, err = e.Run(ctx, sandbox.Plan{Read: sandbox.SystemRead()}, 0, sandbox.Command{
		Argv: argv, Dir: "/", Env: []string{"PATH=/usr/bin:/bin"}, Stdout: &printed, Stderr: &printed,
	})

	return code, strings.TrimSpace(printed.String()), err
}

// Run starts the command under sandbox-exec in its own process group and waits for it. sandbox-exec becomes the command, so the exit code is the one of
// the command. A program that is not found gives 127 and runs no code.
func (e Enforcer) Run(ctx context.Context, plan sandbox.Plan, proxyPort int, cmd sandbox.Command) (int, error) {
	if len(cmd.Argv) == 0 {
		return 0, errors.New("the sandbox plan has no command")
	}

	program, err := find(cmd.Argv[0], cmd.Env)
	if err != nil {
		fmt.Fprintf(orDiscard(cmd.Stderr), "obee sandbox: %v. The command did not start.\n", err)

		return notFound, nil
	}

	if err := refuseShim(program); err != nil {
		return 0, err
	}

	child := proc.GroupCommand(ctx, e.program, arguments(plan, proxyPort, program, cmd.Argv[1:]), cmd.Dir)
	child.Env = append([]string{}, cmd.Env...)
	child.Stdout, child.Stderr = orDiscard(cmd.Stdout), orDiscard(cmd.Stderr)

	return codeOf(child.Run())
}

// shimFolder is the folder of the Apple shims, and shimExample is one of them. Every shim in the folder is the same file, so a program is a shim when it is
// the same file as the example.
const (
	shimFolder  = "/usr/bin/"
	shimExample = "/usr/bin/git"
)

// isShim reports whether program is one of the Apple developer-tools shims in /usr/bin (git, make, clang and others). A shim asks xcrun for the real tool,
// and xcrun reads the developer folder and writes its cache to the per-user system temporary folder, which no plan opens. The real tool runs when the
// shim is not in the way, so the refusal is an early and clear message instead of an error from xcrun.
func isShim(program string) bool {
	if !strings.HasPrefix(program, shimFolder) {
		return false
	}

	a, errA := os.Stat(program)
	b, errB := os.Stat(shimExample)

	return errA == nil && errB == nil && os.SameFile(a, b)
}

// refuseShim returns an error that tells the user what to do when program is a shim, and nil otherwise.
func refuseShim(program string) error {
	if !isShim(program) {
		return nil
	}

	return fmt.Errorf("%s is an Apple developer-tools shim. It needs the Xcode or Command Line Tools folder and a cache file in a system folder that no plan "+
		"can open, so it cannot run under the sandbox. Use the same tool from another folder, such as the one that Homebrew installs, "+
		"and add the folder of its libraries to sandbox_read if the tool needs one", program)
}

// arguments returns the arguments of sandbox-exec: the profile, one -D for each parameter, then the program and its arguments.
func arguments(plan sandbox.Plan, proxyPort int, program string, rest []string) []string {
	text, defines := Profile(plan, proxyPort)
	args := []string{"-p", text}

	for _, define := range defines {
		args = append(args, "-D", define)
	}

	return append(append(args, program), rest...)
}

// find looks for name in the PATH of env, the way a shell does for the command. A name with a slash is taken as it is.
func find(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		return exec.LookPath(name)
	}

	for _, pair := range env {
		// An empty PATH finds nothing: proc.LookPath would take the PATH of obee for it.
		if paths, ok := strings.CutPrefix(pair, "PATH="); ok && paths != "" {
			if found, err := proc.LookPath(name, paths); err == nil {
				return found, nil
			}
		}
	}

	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}

	return w
}

// codeOf turns the result of waiting for the child into an exit code: 0 for success, the code of the command, or 128 plus the signal number for a command
// that a signal ended. The error is not nil when sandbox-exec itself did not start.
func codeOf(err error) (int, error) {
	if err == nil {
		return 0, nil
	}

	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		return 0, fmt.Errorf("start sandbox-exec: %w", err)
	}

	if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return signalBase + int(status.Signal()), nil
	}

	return exited.ExitCode(), nil
}
