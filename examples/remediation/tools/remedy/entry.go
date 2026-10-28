package remedy

import (
	"context"
	"io"
	"os"
	"os/signal"

	"github.com/tobocop2/obee/examples/remediation/tools/roundcut"
	"github.com/tobocop2/obee/internal/proc"
)

// workingFolder is the folder the tools read: the repository of the process.
const workingFolder = "."

// OpenForge turns the opener of a forge backend into the Forge field of Deps.
// A backend that fails returns a nil pointer of its own type. Put in the interface, that pointer is not nil, so the result gives the nil interface on an error.
func OpenForge[F roundcut.Forge](open func(repo string) (F, error)) func(string) (roundcut.Forge, error) {
	return func(repo string) (roundcut.Forge, error) {
		forge, err := open(repo)
		if err != nil {
			return nil, err
		}

		return forge, nil
	}
}

// Main runs one command line (without the program name) in the working folder, with the forge that open gives, and returns the exit code. An interrupt stops the run.
func Main[F roundcut.Forge](args []string, out, errOut io.Writer, open func(repo string) (F, error)) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	deps := Deps{Git: proc.NewExecRunner("git", workingFolder), Dir: workingFolder, Forge: OpenForge(open)}

	return Execute(ctx, args, deps, out, errOut)
}
