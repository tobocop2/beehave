//go:build darwin

package seatbelt_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/sandbox"
	"github.com/tobocop2/beehest/internal/sandbox/seatbelt"
)

// A plan that names the system folders, as the plan of a gate and the check of the enforcer do, reads every folder that the profile added by itself
// before it stopped adding them: the system folders, with /etc as its real folder /private/etc.
func TestProfile_APlanWithTheSystemFoldersReadsWhatTheProfileAddedBefore(t *testing.T) {
	t.Parallel()

	_, defines := seatbelt.Profile(sandbox.Plan{Read: sandbox.SystemRead()}, 0)

	before := append(sandbox.SystemRead(), "/System", "/Library/Apple", "/private/etc", "/private/var/db/dyld", "/private/var/db/timezone", "/private/var/select")
	for _, path := range before {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = path
		}

		assert.Contains(t, readsOf(defines), resolved, path)
	}
}
