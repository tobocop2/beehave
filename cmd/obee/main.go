// Command obee is the composition root: it wires the packages together and runs the CLI.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/tobocop2/obee/internal/cli"
)

// version is set by -ldflags at release build; gochecknoglobals allows this name.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx, os.Args[1:], cli.BuildInfo{Version: version}, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
