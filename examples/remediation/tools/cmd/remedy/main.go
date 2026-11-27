// Command remedy runs the tools of the remediation example. The subcommands are in the command table of the remedy package.
package main

import (
	"os"

	"github.com/tobocop2/beehest/examples/remediation/tools/remedy"
	forgegithub "github.com/tobocop2/beehest/internal/forge/github"
)

// The GitHub forge gives the closing references; without this line a drift would fall back to the body parse.
var _ remedy.Listing = (*forgegithub.Forge)(nil)

func main() {
	os.Exit(remedy.Main(os.Args[1:], os.Stdout, os.Stderr, forgegithub.NewExec))
}
