// Command remedy runs the tools of the remediation example. The subcommands are in the command table of the remedy package.
package main

import (
	"os"

	"github.com/tobocop2/obee/examples/remediation/tools/remedy"
	forgegithub "github.com/tobocop2/obee/internal/forge/github"
)

func main() {
	os.Exit(remedy.Main(os.Args[1:], os.Stdout, os.Stderr, forgegithub.NewExec))
}
