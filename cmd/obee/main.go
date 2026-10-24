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
	code := cli.Execute(ctx, os.Args[1:], cli.BuildInfo{Version: version, Adapters: newAdapters(proc.NewExecRunner("bd", "")).check}, config.ProcessEnv(), os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
