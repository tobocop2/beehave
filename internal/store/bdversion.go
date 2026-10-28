package store

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// minBd is the oldest bd that has the guards the claim rows need: bd update --if-assignee and --if-status, with exit code 13 for a mismatch.
const minBd = "1.3.2"

var bdVersion = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?`)

// requireGuards refuses an old bd before a lease write. It asks bd once; a bd that passed is not asked again.
// Every method that writes a claim row calls it first, so no path reaches a guard flag that the bd does not have.
func (s *BeadsStore) requireGuards(ctx context.Context) error {
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
func RequireBd(ctx context.Context, run Runner) error {
	done := run.Run(ctx, []string{"--version"})
	if done.ExitCode != 0 {
		return refuse("store: cannot run `bd --version`: %s. obee needs bd %s or newer for claims", firstRunes(strings.TrimSpace(done.Stderr), maxStderrRunes), minBd)
	}

	found := bdVersion.FindStringSubmatch(done.Stdout)
	have, readable := versionOf(found)

	if !readable {
		return refuse("obee needs bd %s or newer for claims; cannot read a version from %q", minBd, firstRunes(strings.TrimSpace(done.Stdout), maxStderrRunes))
	}

	if olderThanMin(have) {
		return refuse("obee needs bd %s or newer for claims; found %s", minBd, found[0])
	}

	return nil
}

// version is the numbers of a bd version and whether it is a pre-release.
type version struct {
	numbers    [3]int
	prerelease bool
}

// versionOf reads a match of bdVersion. It reports false for no match or for a number that is too large.
func versionOf(found []string) (version, bool) {
	var have version

	if found == nil {
		return have, false
	}

	for i := range have.numbers {
		number, err := strconv.Atoi(found[i+1])
		if err != nil {
			return have, false
		}

		have.numbers[i] = number
	}

	have.prerelease = found[4] != ""

	return have, true
}

// olderThanMin compares a version with minBd. A pre-release is older than its release.
func olderThanMin(have version) bool {
	want, _ := versionOf(bdVersion.FindStringSubmatch(minBd))

	if have.numbers != want.numbers {
		return slices.Compare(have.numbers[:], want.numbers[:]) < 0
	}

	return have.prerelease
}
