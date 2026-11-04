// Package landlock is the sandbox enforcer for Linux. Landlock restricts the process that asks for it, and the restriction passes to its children, so obee
// starts a copy of itself as a small helper. The helper applies the rules of the plan to itself and then replaces itself with the command.
//
// The kernel must report Landlock ABI 4 or later (Linux 6.7). Below ABI 4 the kernel cannot limit TCP connect, and a plan could not keep the network closed,
// so the enforcer says it cannot run and the gate is refused as before. The enforcer limits TCP connect to the port of the proxy and refuses TCP bind.
// It does not limit UDP, so a command can still send datagrams, for example to a DNS server. TCP is limited by port only, not by address.
//
// Unix sockets are closed in one of two ways. From ABI 9 the kernel governs connect to a pathname socket and, with the abstract socket scope, to an
// abstract socket: the helper handles both, grants no socket, and a socket that the command makes itself stays usable. Below ABI 9 the kernel does not,
// so the helper first installs a seccomp filter that refuses socket(2) for AF_UNIX (and io_uring_setup, and every call of another interface than the
// native one). The command then makes no unix socket at all. A machine where that filter cannot load makes Check fail, so the gate is refused
// and never runs unconfined.
//
// Check runs a command that does nothing under the whole confinement in a throw-away child: the child loads the filter (below ABI 9) and applies the
// Landlock configuration of the kernel, exactly as the helper does for a gate. A kernel or a container that refuses either makes Check fail, so the
// gate is refused and never fails at run time with exit code 125.
//
// ABI 9 is not verified on a real kernel: no machine that the project uses runs Linux 6.18 or later. The ABI 9 configuration is the one that go-landlock
// ships, and Check proves on each machine that the kernel accepts it.
package landlock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	ll "github.com/landlock-lsm/go-landlock/landlock"
	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"

	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/sandbox"
)

// Name is the name of this enforcer in sandbox_enforcer.
const Name = "landlock"

// helperArg is the first argument that makes obee act as the helper.
const helperArg = "--obee-landlock-helper"

// minABI is the first Landlock ABI that limits TCP connect.
const minABI = 4

// unixABI is the first Landlock ABI that limits connect to a pathname unix socket. Below it the helper installs a seccomp filter.
const unixABI = 9

// helperFailed is the exit code of the helper when it cannot apply the plan. The command then ran no code.
const helperFailed = 125

// signalBase is added to the signal number for the exit code of a command that a signal ended.
const signalBase = 128

// notFound is the exit code of a shell for a program that is not found.
const notFound = 127

// Enforcer runs a command under Landlock. The three functions are the kernel report, the path of this program and the writer of the plan text, which a test
// replaces.
type Enforcer struct {
	abi        func() (int, error)
	executable func() (string, error)
	marshal    func(any) ([]byte, error)
}

// New returns the Landlock enforcer.
func New() Enforcer {
	return Enforcer{abi: ABI, executable: os.Executable, marshal: json.Marshal}
}

// Name returns Name.
func (Enforcer) Name() string { return Name }

// ABI returns the Landlock ABI version that the kernel reports.
func ABI() (int, error) { return llsys.LandlockGetABIVersion() }

// probeLimit is the time that the probe child may run.
const probeLimit = 30 * time.Second

// Check says whether the kernel runs Landlock at ABI 4 or later, and whether a child can apply the confinement of this kernel: the seccomp filter below
// ABI 9 and the Landlock rules.
func (e Enforcer) Check() error {
	abi, err := e.abi()
	if err != nil {
		return fmt.Errorf("%w: this kernel does not run Landlock (%w). Landlock needs Linux 6.7 or later", sandbox.ErrUnavailable, err)
	}

	if abi < minABI {
		return fmt.Errorf("%w: this kernel reports Landlock ABI %d, and obee needs ABI %d (Linux 6.7) to limit TCP connect", sandbox.ErrUnavailable, abi, minABI)
	}

	if err := e.probe(); err != nil {
		return fmt.Errorf("%w: this kernel reports Landlock ABI %d, and %w", sandbox.ErrUnavailable, abi, err)
	}

	return nil
}

// probe starts the helper on a plan that runs a command that does nothing, and reports why the helper could not confine it.
func (e Enforcer) probe() error {
	ctx, cancel := context.WithTimeout(context.Background(), probeLimit)
	defer cancel()

	child, err := e.helperCommand(ctx, spec{Plan: sandbox.Plan{Read: sandbox.SystemRead()}, Argv: []string{"true"}, Env: []string{"PATH=/usr/bin:/bin"}}, sandbox.Command{})
	if err != nil {
		return err
	}

	var out bytes.Buffer

	child.Stdout, child.Stderr = &out, &out

	if err := child.Run(); err != nil {
		return fmt.Errorf("a child cannot apply the confinement here (%w): %s", err, strings.TrimSpace(out.String()))
	}

	return nil
}

// spec is what the parent gives the helper in its second argument.
type spec struct {
	Plan sandbox.Plan `json:"plan"`
	Port int          `json:"port"`
	Argv []string     `json:"argv"`
	Env  []string     `json:"env"`
}

// Run starts the helper in its own process group and waits for it. The helper becomes the command, so the exit code is the one of the command.
func (e Enforcer) Run(ctx context.Context, plan sandbox.Plan, proxyPort int, cmd sandbox.Command) (int, error) {
	child, err := e.helperCommand(ctx, spec{Plan: plan, Port: proxyPort, Argv: cmd.Argv, Env: cmd.Env}, cmd)
	if err != nil {
		return 0, err
	}

	err = child.Run()
	if err == nil {
		return 0, nil
	}

	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		return 0, fmt.Errorf("start the sandbox helper: %w", err)
	}

	return codeOf(exited), nil
}

// helperCommand makes the process that runs this program as the helper, in its own process group. The helper gets the environment of the command in its
// plan and starts with none of its own, so no variable of obee reaches the command.
func (e Enforcer) helperCommand(ctx context.Context, s spec, cmd sandbox.Command) (*exec.Cmd, error) {
	self, err := e.executable()
	if err != nil {
		return nil, fmt.Errorf("find the obee program to start the sandbox helper: %w", err)
	}

	raw, err := e.marshal(s)
	if err != nil {
		return nil, fmt.Errorf("write the sandbox plan: %w", err)
	}

	child := proc.GroupCommand(ctx, self, []string{helperArg, string(raw)}, cmd.Dir)
	child.Env = []string{}
	child.Stdout, child.Stderr = sandbox.OrDiscard(cmd.Stdout), sandbox.OrDiscard(cmd.Stderr)

	return child, nil
}

// codeOf returns the exit code, or 128 plus the signal number for a command that a signal ended.
func codeOf(exited *exec.ExitError) int {
	if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return signalBase + int(status.Signal())
	}

	return exited.ExitCode()
}

// RunHelper is called first in main of obee, and in TestMain of a package that runs the contract suite. When the program was started as the helper,
// it applies the plan and replaces itself with the command: on success it never returns. When it cannot start the command, it writes the reason to stderr
// and returns the exit code to use (127 for a program that is not found, helperFailed for a plan that does not apply) and true. When the program was not
// started as the helper, it returns 0 and false at once.
func RunHelper(args []string, stderr io.Writer) (int, bool) {
	if len(args) != 3 || args[1] != helperArg {
		return 0, false
	}

	code, err := becomeCommand(args[2], system{abi: ABI, filter: newKernel().install, restrict: ll.Config.Restrict, execve: syscall.Exec})
	fmt.Fprintf(stderr, "obee sandbox: %v. The command did not start.\n", err)

	return code, true
}

// system is what the helper asks of the kernel: its Landlock ABI, a filter that refuses unix sockets, a lock on this process with rules, and the replacement
// of this process by the command. A test replaces them, because a filter and a lock cannot be undone in a test process and the replacement does not return.
type system struct {
	abi      func() (int, error)
	filter   func() error
	restrict func(cfg ll.Config, rules ...ll.Rule) error
	execve   func(program string, argv, env []string) error
}

// configFor returns the Landlock configuration for the ABI of the kernel: from ABI 9 it handles pathname unix sockets and the abstract socket scope, and
// no rule grants a socket; below that, TCP and the file system.
func configFor(abi int) ll.Config {
	if abi >= unixABI {
		return ll.V9
	}

	return ll.V4
}

// becomeCommand returns only when the command could not start. The code is the exit code of the helper then.
func becomeCommand(raw string, sys system) (int, error) {
	var s spec
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return helperFailed, fmt.Errorf("read the sandbox plan: %w", err)
	}

	if len(s.Argv) == 0 {
		return helperFailed, errors.New("the sandbox plan has no command")
	}

	program, err := findProgram(s.Argv[0], s.Env)
	if err != nil {
		// A shell gives 127 for a missing program as well, and the command ran no code.
		return notFound, err
	}

	if err := confine(s, sys); err != nil {
		return helperFailed, err
	}

	return helperFailed, sys.execve(program, s.Argv, s.Env)
}

// confine locks this process. Below ABI 9 the kernel does not govern unix sockets, so a filter that refuses to make one loads first, and a failure stops
// the command.
func confine(s spec, sys system) error {
	abi, err := sys.abi()
	if err != nil {
		return fmt.Errorf("read the Landlock ABI: %w", err)
	}

	if abi < unixABI {
		if err := sys.filter(); err != nil {
			return err
		}
	}

	return restrict(s, func(rules ...ll.Rule) error { return sys.restrict(configFor(abi), rules...) })
}

// findProgram looks for name in the PATH of env, the way a shell does for the command. A name with a slash is taken as it is.
func findProgram(name string, env []string) (string, error) {
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

// restrict applies the plan to this process: read and write by path, and TCP connect to the proxy port only.
// It fails, and does not fall back, when the kernel cannot apply a part of it.
func restrict(s spec, apply func(rules ...ll.Rule) error) error {
	rules := readRules(s.Plan.Read)

	for _, path := range s.Plan.Write {
		rules = append(rules, writeRule(path))
	}

	if s.Port > 0 {
		rules = append(rules, ll.ConnectTCP(uint16(s.Port))) //nolint:gosec // a port is below 65536
	}

	if err := apply(rules...); err != nil {
		return fmt.Errorf("apply the Landlock rules: %w", err)
	}

	return nil
}

// readRules returns the read rule of every read path.
func readRules(paths []string) []ll.Rule {
	rules := make([]ll.Rule, 0, len(paths)+1)

	for _, path := range paths {
		rules = append(rules, readRule(path))
	}

	return rules
}

func isFolder(path string) bool {
	info, err := os.Stat(path)

	return err != nil || info.IsDir()
}

func readRule(path string) ll.FSRule {
	if isFolder(path) {
		return ll.RODirs(path).IgnoreIfMissing()
	}

	return ll.ROFiles(path).IgnoreIfMissing()
}

func writeRule(path string) ll.FSRule {
	if isFolder(path) {
		return ll.RWDirs(path).IgnoreIfMissing()
	}

	return ll.RWFiles(path).IgnoreIfMissing()
}
