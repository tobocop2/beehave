// Command obee is the composition root: it wires the packages together and runs the CLI.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/sandbox/enforcers"
	"github.com/tobocop2/obee/internal/sandbox/landlock"
)

// version is set by -ldflags at release build; gochecknoglobals allows this name.
var version = "dev"

// privateUmask makes every file and folder obee creates private to the user.
const privateUmask = 0o077

func main() {
	// A copy of obee started by the Landlock enforcer applies its rules and replaces itself with the command. It comes back here only when the command did not start.
	if code, helper := landlock.RunHelper(os.Args, os.Stderr); helper {
		os.Exit(code)
	}

	syscall.Umask(privateUmask)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	env := config.ProcessEnv()
	code := cli.Execute(ctx, os.Args[1:], buildInfo(env, version, flowFactory()), env, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// buildInfo wires the adapters, the doctor and the item list to the command line, with the flow given.
func buildInfo(env map[string]string, version string, flow cli.Flow) cli.BuildInfo {
	bd := proc.NewExecRunner("bd", "")
	table := newAdapters(bd, func() (string, error) { return config.StateDir(env) })

	return cli.BuildInfo{
		Version:  version,
		Adapters: table.check,
		Items:    table.itemOpener(),
		Doctor:   newDoctor(bd, newSecurityRunner(), env),
		Flow:     flow,

		GateRefusal: enforcers.Refusal,
	}
}
