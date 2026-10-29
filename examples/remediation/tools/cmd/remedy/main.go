// Command remedy runs the tools of the remediation example. The subcommands are in the command table of the remedy package.
package main

import (
	"os"

	"github.com/tobocop2/obee/examples/remediation/tools/remedy"
	"github.com/tobocop2/obee/examples/remediation/tools/scope"
	"github.com/tobocop2/obee/internal/config"
	forgegithub "github.com/tobocop2/obee/internal/forge/github"
	"github.com/tobocop2/obee/internal/proc"
)

// The GitHub forge gives the closing references; without this line a drift would fall back to the body parse.
var _ scope.Listing = (*forgegithub.Forge)(nil)

func main() {
	claims := remedy.OpenClaims(openClaims(config.ProcessEnv(), proc.NewExecRunner("bd", "")))

	os.Exit(remedy.Main(os.Args[1:], os.Stdout, os.Stderr, forgegithub.NewExec, claims))
}
