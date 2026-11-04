// Package link is the command line to a host, shared by every host adapter. It owns the text of a failed command and the ssh command line
// of a remote one: the ssh options, the quoting of each word, and the exit code that says the ssh link dropped.
package link

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

var (
	// ErrCommand is returned for a command that exits with a failure.
	ErrCommand = errors.New("command failed")
	// ErrDropped is returned for a read that the ssh link dropped. The command did not run, or its answer did not arrive.
	ErrDropped = errors.New("the ssh link to the host dropped")
)

const droppedExit = 255 // the exit code of ssh when the link fails

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
	target string
	ssh    proc.Runner
}

// New returns the link to target. The first argument of an ssh call is target: the name of the host in the ssh config.
func New(target string, ssh proc.Runner) *Link { return &Link{target: target, ssh: ssh} }

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

	for _, word := range words {
		args = append(args, model.ShellQuote(word))
	}

	return args
}
