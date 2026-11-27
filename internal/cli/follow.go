package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/tobocop2/beehest/internal/flow"
)

// follow prints the agent output of the run that the command names, as it arrives.
func (f flowCommands) follow(cmd *cobra.Command, flag, item string, args []string, asJSON bool) error {
	if len(args) != 1 || item != "" || asJSON {
		return errFollowAlone
	}

	out := cmd.OutOrStdout()

	return f.withClient(cmd, flag, func(client Steering) error {
		return client.Follow(f.ctx, args[0], newFollowPrinter(out).print)
	})
}

// followPrinter writes the entries of the agent streams: a heading line for each phase and for each new try of a phase, then the entries.
type followPrinter struct {
	out   io.Writer
	phase string
	try   int
}

func newFollowPrinter(out io.Writer) *followPrinter {
	return &followPrinter{out: out}
}

// print writes one entry. A tool call starts with "> ".
func (p *followPrinter) print(entry flow.FollowedEntry) {
	if entry.Kind == flow.EntryWait {
		p.say("-- waiting for an answer to: " + entry.Text)

		return
	}

	if entry.Phase != p.phase || entry.Try != p.try {
		p.phase, p.try = entry.Phase, entry.Try
		p.say(fmt.Sprintf("-- %s, try %d", entry.Phase, entry.Try))
	}

	if entry.Kind == flow.EntryCall {
		p.say("> " + entry.Text)

		return
	}

	p.say(entry.Text)
}

func (p *followPrinter) say(line string) {
	// A failed write to stdout has no recovery.
	_, _ = fmt.Fprintln(p.out, line)
}
