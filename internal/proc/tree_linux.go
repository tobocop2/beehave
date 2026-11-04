//go:build linux

package proc

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CapMeaning says in words what max_new_processes limits on this system. It is a format with the cap as its one number.
const CapMeaning = "at most %d tasks, threads included, in the tree of the command besides the command itself"

// scopeStopTimeout is the longest wait for the kill of a scope.
const scopeStopTimeout = 10 * time.Second

// envBinary starts a program with exactly the environment it is given.
const envBinary = "/usr/bin/env"

// platformHooks holds the programs that a capped command needs. A zero value means the system programs; tests supply others.
type platformHooks struct {
	systemdRun string
	systemctl  string
	unitPrefix string
}

func (h platformHooks) run() string { return orDefault(h.systemdRun, "systemd-run") }

func (h platformHooks) ctl() string { return orDefault(h.systemctl, "systemctl") }

func (h platformHooks) prefix() string { return orDefault(h.unitPrefix, "obee-") }

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

// Tree describes the limits of the tree of processes that one started command makes.
// MaxNewProcesses is the count of tasks (threads count) that the tree may hold besides the command itself.
// Env is the whole environment of the command. Nil means the environment of this process.
type Tree struct {
	MaxNewProcesses int
	Env             []string
}

// scopedCommand returns a command for binary that runs in its own process group, inside a new systemd user scope with TasksMax set to the cap of tree plus one.
// The kernel counts the tasks of that control group alone, so other programs of the user do not use the cap. A canceled context stops the scope, which kills every process in it,
// also one that left the process group with setsid. If the scope cannot start, the call fails and the command does not run: a cap that counts the wrong thing is refused.
// Only the runner uses it, and only for a step that sets a cap. A gate command in the sandbox, an agent run and a host job take no cap.
func (h platformHooks) scopedCommand(ctx context.Context, binary string, args []string, dir string, tree Tree) (*exec.Cmd, error) {
	if err := h.probe(ctx); err != nil {
		return nil, err
	}

	unit := newUnit(h.prefix())

	cmd := GroupCommand(ctx, h.run(), scopeArgs(unit, binary, args, tree), dir)
	if tree.Env != nil {
		cmd.Env = busEnv()
	}

	cmd.Cancel = h.cancelScope(ctx, cmd, unit)

	return cmd, nil
}

// cancelScope returns the cancel of a command in a scope: it kills the scope, then the process group.
func (h platformHooks) cancelScope(ctx context.Context, cmd *exec.Cmd, unit string) func() error {
	return func() error {
		h.stopScope(ctx, unit)

		return KillGroup(cmd)
	}
}

// scopeArgs returns the arguments of systemd-run: the scope with its cap, then the command. A command with its own environment starts through env, so that no variable
// of this process reaches it, also none that systemd-run itself needs.
func scopeArgs(unit, binary string, args []string, tree Tree) []string {
	inner := append([]string{binary}, args...)
	if tree.Env != nil {
		inner = append(append([]string{envBinary, "-i"}, tree.Env...), inner...)
	}

	head := []string{"--user", "--scope", "--collect", "--quiet", "--unit=" + unit, "-p", "TasksMax=" + strconv.Itoa(tree.MaxNewProcesses+1), "--"}

	return append(head, inner...)
}

// newUnit returns a unit name that no other call has: the scope of a name that exists fails to start.
func newUnit(prefix string) string {
	return prefix + rand.Text()
}

// busEnv is the environment that systemd-run needs to reach the user manager.
func busEnv() []string {
	var env []string

	for _, pair := range (&exec.Cmd{}).Environ() {
		if name, _, _ := strings.Cut(pair, "="); name == "XDG_RUNTIME_DIR" || name == "DBUS_SESSION_BUS_ADDRESS" {
			env = append(env, pair)
		}
	}

	return env
}

// probe starts a scope with a cap of one task around a program that ends at once. It fails with the fix when the scope cannot start.
func (h platformHooks) probe(ctx context.Context) error {
	env := busEnv()

	cmd := exec.CommandContext(ctx, h.run(), "--user", "--scope", "--collect", "--quiet", "-p", "TasksMax=1", "--", "true") //nolint:gosec // fixed arguments; the program name comes from the test hooks only
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("the check of the systemd user scope for max_new_processes ended before it finished: %w", ctxErr)
	}

	return fmt.Errorf("max_new_processes needs a systemd user manager with the pids controller, and a test scope did not start (%w: %s). %s",
		err, strings.TrimSpace(string(out)), probeAdvice(env))
}

// probeAdvice names the cause that the environment shows and the fix for it. Without XDG_RUNTIME_DIR, systemd-run cannot find the user bus: this is the case under cron,
// under a system service with User=, and after sudo -u. With it, the user manager itself is not running for this user.
func probeAdvice(env []string) string {
	for _, pair := range env {
		if value, found := strings.CutPrefix(pair, "XDG_RUNTIME_DIR="); found && value != "" {
			return "XDG_RUNTIME_DIR is set to " + value + ", so the user manager is not running or has no pids controller. " +
				"Run `loginctl enable-linger` for this user and log in again, or remove max_new_processes from the step"
		}
	}

	return fmt.Sprintf("XDG_RUNTIME_DIR is not set, so systemd-run cannot find the user bus. Set XDG_RUNTIME_DIR=/run/user/%d for obee, or remove max_new_processes from the step", os.Getuid())
}

// stopScope kills every process of the scope. The call is best effort: a scope that does not exist yet has nothing to kill, and the kill of the process group follows.
func (h platformHooks) stopScope(ctx context.Context, unit string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), scopeStopTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, h.ctl(), "--user", "kill", "--kill-whom=all", "--signal=SIGKILL", unit+".scope") //nolint:gosec // fixed arguments; the unit name is made by this package
	cmd.Env = busEnv()
	_ = cmd.Run() //nolint:errcheck // best effort, see above
}
