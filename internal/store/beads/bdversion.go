package beads

import (
	"context"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/store"
)

// minBd is the oldest bd that has what the claim rows need: the leases with bd heartbeat and bd reclaim, and the guards bd update --if-assignee and --if-status with exit code 13 for a mismatch.
const minBd = "1.3.2"

var bdVersion = regexp.MustCompile(`\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?`)

// requireGuards refuses an old bd before a lease write. It asks bd once; a bd that passed is not asked again.
// Every method that writes a claim row calls it first, so no path reaches a guard flag that the bd does not have.
func (s *Store) requireGuards(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.guarded {
		return nil
	}

	if err := RequireBd(ctx, s.run); err != nil {
		return err
	}

	s.guarded = true

	return nil
}

// RequireBd refuses a bd older than minBd. It reads `bd --version`, which prints `bd version <x.y.z> (<build>)`.
func RequireBd(ctx context.Context, run proc.Runner) error {
	done := run.Run(ctx, []string{"--version"})
	if done.ExitCode != 0 {
		return store.Refuse("store: cannot run `bd --version`: %s. obee needs bd %s or newer for claims", firstRunes(strings.TrimSpace(done.Stderr), maxStderrRunes), minBd)
	}

	found := bdVersion.FindString(done.Stdout)
	if !semver.IsValid("v" + found) {
		return store.Refuse("obee needs bd %s or newer for claims; cannot read a version from %q", minBd, firstRunes(strings.TrimSpace(done.Stdout), maxStderrRunes))
	}

	if semver.Compare("v"+found, "v"+minBd) < 0 {
		return store.Refuse("obee needs bd %s or newer for claims; found %s", minBd, found)
	}

	return nil
}
