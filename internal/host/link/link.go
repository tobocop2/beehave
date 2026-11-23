// Package link is the command line to a host, shared by every host adapter. It owns the text of a failed command and the ssh command line
// of a remote one: the ssh options, the quoting of each word, and the exit code that says the ssh link dropped.
package link

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

var (
	// ErrCommand is returned for a command that exits with a failure.
	ErrCommand = errors.New("command failed")
	// ErrDropped is returned for a read that the ssh link dropped. The command did not run, or its answer did not arrive.
	ErrDropped = errors.New("the ssh link to the host dropped")
)

// ErrNoFlock is the refusal of a host that has no flock(1), which the slot limit and the clone lock of a job need.
var ErrNoFlock = errors.New("the host has no flock command. Install util-linux (flock) on the host")

const droppedExit = 255 // the exit code of ssh when the link fails

// CheckFlock returns an error that wraps ErrNoFlock when the host has no flock command. A dropped link is its own error.
func (l *Link) CheckFlock(ctx context.Context) error {
	result := l.Exec(ctx, "flock", "--version")
	if err := l.Drop(result); err != nil {
		return err
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("%s: %w", l.target, ErrNoFlock)
	}

	return nil
}

// Failure is the error of a command line that did not exit with zero. It wraps ErrCommand. name is the word that the error shows.
func Failure(name string, result model.CommandResult) error {
	return fmt.Errorf("%w: %s: exit %d: %s", ErrCommand, name, result.ExitCode, strings.TrimSpace(result.Stderr))
}

func Run(ctx context.Context, runner proc.Runner, name string, args []string) (string, error) {
	result := runner.Run(ctx, args)
	if result.ExitCode != 0 {
		return "", Failure(name, result)
	}

	return result.Stdout, nil
}

type Link struct {
	target  string
	ssh     proc.Runner
	options []string // the ssh options of a machine link; empty for a link whose ssh config entry holds them
	guarded bool     // the target follows a "--", so it cannot become an option
}

// ErrConfig is returned by ForMachine for a setting that could change the ssh command line.
var ErrConfig = errors.New("invalid ssh host setting")

// Options is the block of a machine: the keys of the ssh or the slurm block. An empty field leaves the choice to the ssh config of the user.
// Block is the name of the block, "ssh" or "slurm". It names the key in a refusal. At is the position of the block in the project file; a refusal shows it.
type Options struct {
	Block, At                           string
	Address, User, Identity, KnownHosts string
	Port                                int
}

// Refusal returns an error that wraps ErrConfig. at is the position of the block in the project file, and a refusal shows it when it is not empty.
func Refusal(at, format string, args ...any) error {
	text := fmt.Sprintf(format, args...)
	if at == "" {
		return fmt.Errorf("%w: %s", ErrConfig, text)
	}

	return fmt.Errorf("%w: %s: %s", ErrConfig, at, text)
}

// key returns the name of a key of the block as the user writes it, for a refusal.
func (o Options) key(name string) string { return "host." + o.Block + "." + name }

const (
	maxPort = 65535
	// The ssh options of every machine call: a dead link ends in exit 255 after about a minute and never hangs.
	connectTimeout = "ConnectTimeout=15"
	aliveInterval  = "ServerAliveInterval=15"
	aliveCount     = "ServerAliveCountMax=3"
)

var wordPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]*$`)

// ForMachine returns the link to a machine that the owner provides. target is the ssh alias, or a label when Options.Address names the machine.
// The target follows a "--", a host key that changed is always refused, and no word can become an ssh option. It runs nothing.
func ForMachine(target string, o Options, ssh proc.Runner) (*Link, error) {
	options, err := machineOptions(target, o)
	if err != nil {
		return nil, err
	}

	return &Link{target: target, ssh: ssh, options: options, guarded: true}, nil
}

// checkWords refuses a target, address or user that could become an ssh option, and a port or identity that ssh would read wrongly.
func checkWords(target string, o Options) error {
	words := []struct{ name, word string }{{"the ssh target", target}, {o.key("address"), o.Address}, {o.key("user"), o.User}}
	for i, w := range words {
		if (w.word != "" || i == 0) && !wordPattern.MatchString(w.word) {
			return Refusal(o.At, "%s %q must start with a letter or digit and hold letters, digits, dots, dashes, at signs and underscores", w.name, w.word)
		}
	}

	if o.Port < 0 || o.Port > maxPort || strings.ContainsFunc(o.Identity, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return Refusal(o.At, "`%s` is 1 to 65535 and `%s` holds no space", o.key("port"), o.key("identity"))
	}

	return nil
}

func machineOptions(target string, o Options) ([]string, error) {
	if err := checkWords(target, o); err != nil {
		return nil, err
	}

	policy, err := hostKeyPolicy(o)
	if err != nil {
		return nil, err
	}

	options := []string{"BatchMode=yes", connectTimeout, aliveInterval, aliveCount}
	options = appendIf(options, "HostName=", o.Address)
	options = appendIf(options, "User=", o.User)

	if o.Port != 0 {
		options = append(options, "Port="+strconv.Itoa(o.Port))
	}

	return append(identityOptions(options, o.Identity), "StrictHostKeyChecking="+policy), nil
}

// identityOptions names the key file. IdentitiesOnly keeps ssh from offering other keys of the agent to the host.
func identityOptions(options []string, identity string) []string {
	if identity == "" {
		return options
	}

	return append(options, "IdentityFile="+identity, "IdentitiesOnly=yes")
}

func appendIf(options []string, key, value string) []string {
	if value == "" {
		return options
	}

	return append(options, key+value)
}

// hostKeyPolicy maps the known_hosts setting to the ssh value. The setting has no value that turns the check off.
func hostKeyPolicy(o Options) (string, error) {
	switch o.KnownHosts {
	case "", model.KnownHostsStrict:
		return "yes", nil
	case model.KnownHostsAcceptNew:
		return model.KnownHostsAcceptNew, nil
	}

	return "", Refusal(o.At, "`%s` must be %s or %s. A host key that changed is always refused", o.key("known_hosts"), model.KnownHostsStrict, model.KnownHostsAcceptNew)
}

// New returns the link to target. The first argument of an ssh call is target: the name of the host in the ssh config.
func New(target string, ssh proc.Runner) *Link { return &Link{target: target, ssh: ssh} }

// Target is the name that the link connects to: the ssh alias or label.
func (l *Link) Target() string { return l.target }

// Exec returns the raw result, for a caller that reads the exit code.
func (l *Link) Exec(ctx context.Context, words ...string) model.CommandResult {
	return l.ssh.Run(ctx, l.args(words))
}

// Remote runs a command on the host and returns what it printed. A failure wraps ErrCommand. name is the word that the error shows.
func (l *Link) Remote(ctx context.Context, name string, words ...string) (string, error) {
	return Run(ctx, l.ssh, name, l.args(words))
}

func (l *Link) Runner(binary string) proc.Runner { //nolint:ireturn // host.Teardown takes the runner contract
	return remoteRunner{l: l, binary: binary}
}

// Drop returns an error that wraps ErrDropped when the result is a dropped ssh link, and nil otherwise.
func (l *Link) Drop(result model.CommandResult) error {
	if result.ExitCode != droppedExit {
		return nil
	}

	return fmt.Errorf("%w: ssh to %s: %s", ErrDropped, l.target, strings.TrimSpace(result.Stderr))
}

type remoteRunner struct {
	l      *Link
	binary string
}

func (r remoteRunner) Run(ctx context.Context, args []string) model.CommandResult {
	return r.l.Exec(ctx, append([]string{r.binary}, args...)...)
}

// args returns the ssh arguments for a command. ssh joins the words with spaces and sends them to the shell of the host, so each word is quoted.
func (l *Link) args(words []string) []string {
	args := []string{"-o", "BatchMode=yes", l.target}

	if l.guarded {
		args = nil

		for _, option := range l.options {
			args = append(args, "-o", option)
		}

		args = append(args, "--", l.target)
	}

	for _, word := range words {
		args = append(args, model.ShellQuote(word))
	}

	return args
}
