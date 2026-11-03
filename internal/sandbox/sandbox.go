// Package sandbox is the contract between obee and the mechanisms that confine one command: a plan of what the command may read, write and reach,
// an enforcer that runs the command under the plan, and the one allow-list proxy that every enforcer shares.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
)

// ErrUnavailable is wrapped by the error of an enforcer that cannot run here.
var ErrUnavailable = errors.New("the sandbox enforcer cannot run here")

// Plan is what one command may do. It is recorded with the step, so a replay reads the recorded plan.
// A path outside Read and Write is closed. Network false closes every network connection; Network true opens only the allow-list proxy,
// and the proxy lets through only Domains. A domain is a host name, or *.name for every subdomain of name.
type Plan struct {
	Read    []string `json:"read"`
	Write   []string `json:"write"`
	Network bool     `json:"network"`
	Domains []string `json:"domains"`
}

// Command is one program to run: its words, its folder, its whole environment and where its output goes.
type Command struct {
	Argv   []string
	Dir    string
	Env    []string
	Stdout io.Writer
	Stderr io.Writer
}

// Enforcer runs one command under a plan, with the mechanism of one operating system.
//
// Call order: Check first. Run only after Check returned nil. Run may be called from several goroutines at once, each with its own plan.
// proxyPort is the port of the allow-list proxy on 127.0.0.1, or 0 when the plan has no network. The enforcer lets the command connect to that port and to no other.
// Run returns the exit code of the command, passed through. A signal that ended the command gives 128 plus the signal number.
// The error is not nil only when the command could not run under the plan; the command then ran no code.
type Enforcer interface {
	Name() string
	// Check returns nil when this enforcer can run here, and an error that wraps ErrUnavailable and says why when it cannot.
	Check() error
	Run(ctx context.Context, plan Plan, proxyPort int, cmd Command) (int, error)
}

// Execute starts the allow-list proxy for a plan with network, gives the command the proxy in HTTPS_PROXY and HTTP_PROXY, and runs it under the enforcer.
// The proxy stops when the command ends.
func Execute(ctx context.Context, e Enforcer, plan Plan, cmd Command) (int, error) {
	return execute(ctx, e, plan, cmd, StartProxy)
}

// execute is Execute with the function that starts the proxy, so a test can make the start fail.
func execute(ctx context.Context, e Enforcer, plan Plan, cmd Command, start func(context.Context, []string) (*Proxy, error)) (int, error) {
	if !plan.Network {
		return e.Run(ctx, plan, 0, cmd)
	}

	proxy, err := start(ctx, plan.Domains)
	if err != nil {
		return 0, err
	}
	defer proxy.Close()

	cmd.Env = withProxy(cmd.Env, proxy.Port())

	return e.Run(ctx, plan, proxy.Port(), cmd)
}

// proxyLines is the number of proxy variables that withProxy adds: two names in two cases.
const proxyLines = 4

// isProxyVariable reports whether name is a proxy variable that a program reads, in any case.
func isProxyVariable(name string) bool {
	switch strings.ToUpper(name) {
	case "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY":
		return true
	}

	return false
}

// withProxy removes every proxy variable from env and adds the proxy of obee, in both cases that programs read.
func withProxy(env []string, port int) []string {
	out := make([]string, 0, len(env)+proxyLines)

	for _, pair := range env {
		name, _, _ := strings.Cut(pair, "=")
		if !isProxyVariable(name) {
			out = append(out, pair)
		}
	}

	url := "http://127.0.0.1:" + strconv.Itoa(port)
	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY"} {
		out = append(out, name+"="+url, strings.ToLower(name)+"="+url)
	}

	return out
}

// Describe is the line that the report of a step carries, so the plan is recorded with the step.
func (p Plan) Describe(enforcer string) string {
	return fmt.Sprintf("sandbox %s plan: {read: %q, write: %q, network: %t, domains: %q}", enforcer, p.Read, p.Write, p.Network, p.Domains)
}

// SystemRead lists the folders that a program needs to start: the system programs, libraries and settings. They hold no credential of a person.
func SystemRead() []string {
	return []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/etc", "/opt", "/proc", "/sys", "/dev"}
}

// SystemWrite lists the device files that every program writes.
func SystemWrite() []string {
	return []string{"/dev/null", "/dev/tty", "/dev/zero"}
}

// Choose returns the key in the table of the enforcer that name asks for: auto picks the first of order that can run. It returns an error that wraps
// ErrUnavailable when none can run, or when the named one cannot.
func Choose(table map[string]Enforcer, order []string, name string) (string, error) {
	if name == "" || name == "auto" {
		return firstThatRuns(table, order)
	}

	e, ok := table[name]
	if !ok {
		return "", fmt.Errorf("%w: sandbox_enforcer names %q, and the enforcers are %s", ErrUnavailable, name, strings.Join(order, ", "))
	}

	return name, e.Check()
}

// firstThatRuns returns the name of the first enforcer of order that can run here, or an error that wraps ErrUnavailable with every reason.
func firstThatRuns(table map[string]Enforcer, order []string) (string, error) {
	var reasons []string

	for _, key := range order {
		err := table[key].Check()
		if err == nil {
			return key, nil
		}

		reasons = append(reasons, err.Error())
	}

	if len(reasons) == 0 {
		return "", fmt.Errorf("%w: this build has no enforcer for %s", ErrUnavailable, runtime.GOOS)
	}

	return "", fmt.Errorf("%w: %s", ErrUnavailable, strings.Join(reasons, "; "))
}
