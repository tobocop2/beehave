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
)

// version is set by -ldflags at release build; gochecknoglobals allows this name.
var version = "dev"

// privateUmask makes every file and folder obee creates private to the user.
const privateUmask = 0o077

func main() {
	syscall.Umask(privateUmask)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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
		Version:      version,
		Adapters:     table.check,
		Capabilities: table.capabilities,
		Items:        table.itemOpener(),
		Doctor:       newDoctor(bd, newSecurityRunner(), env),
		Flow:         flow,
	}
}
