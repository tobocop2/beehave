package realtools_test

import (
	"context"
	"fmt"
	"log"
	"testing"

	forgegh "github.com/tobocop2/beehave/internal/forge/github"
)

// TestMain closes the pull requests that a crashed run left with the label of the suite, before any test opens one. The tests run in parallel, so no test sweeps for itself:
// a sweep inside a test would close the pull request of a test that runs at the same time.
func TestMain(m *testing.M) {
	if env("BEEHAVE_REAL_TOOLS") == "1" && env("GH_TOKEN") != "" {
		if err := sweepLeftovers(context.Background()); err != nil {
			log.Printf("the sweep of the sandbox failed, and the suite runs anyway: %v", err)
		}
	}

	m.Run()
}

// sweepLeftovers closes the open pull requests of the sandbox that carry the label of the suite.
func sweepLeftovers(ctx context.Context) error {
	forge, err := forgegh.NewExec(sandbox())
	if err != nil {
		return fmt.Errorf("open the sandbox forge: %w", err)
	}

	left, err := forge.PRList(ctx, 100, "--state", "open", "--label", sweepLabel, "--json", "number")
	if err != nil {
		return fmt.Errorf("list the pull requests of the suite: %w", err)
	}

	for _, pull := range left {
		if err := forge.ClosePR(ctx, pull.Number, "closed by the next run of the real-tools suite"); err != nil {
			return fmt.Errorf("close pull request %d: %w", pull.Number, err)
		}
	}

	return nil
}
