package remedy

import (
	"context"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/tobocop2/obee/internal/ledger"
	"github.com/tobocop2/obee/internal/proc"
)

// workingFolder is the folder the tools read: the repository of the process.
const workingFolder = "."

// OpenForge turns the opener of a forge backend into the Forge field of Deps.
// A backend that fails returns a nil pointer of its own type. Put in the interface, that pointer is not nil, so the result gives the nil interface on an error.
func OpenForge[F Forge](open func(repo string) (F, error)) func(string) (Forge, error) {
	return func(repo string) (Forge, error) {
		forge, err := open(repo)
		if err != nil {
			return nil, err
		}

		return forge, nil
	}
}

// OpenClaims turns the opener of a claim store into the Claims field of Deps. It gives the nil interface on an error, as OpenForge does.
func OpenClaims[C Claims](open func(repo string) (C, error)) func(string) (Claims, error) {
	return func(repo string) (Claims, error) {
		claims, err := open(repo)
		if err != nil {
			return nil, err
		}

		return claims, nil
	}
}

// Main runs one command line (without the program name) in the working folder, with the forge that open gives and the claim store that claims gives, and returns the exit code. An interrupt stops the run.
func Main[F Forge](args []string, out, errOut io.Writer, open func(repo string) (F, error), claims func(repo string) (Claims, error)) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	deps := Deps{
		Git: proc.NewExecRunner("git", workingFolder), Dir: workingFolder, Forge: OpenForge(open), Now: time.Now, Claims: claims, Book: OpenBook(ledger.Open),
	}

	return Execute(ctx, args, deps, out, errOut)
}
