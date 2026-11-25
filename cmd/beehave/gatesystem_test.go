package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/sandbox"
)

// A gate command reads the system folders and writes the system devices by its own plan, because no enforcer adds them: Landlock never did, and Seatbelt
// adds only the frameworks of macOS. A plan without them starts no shell, git, go or make.
func TestPlanOf_AGatePlanReadsTheSystemFoldersAndWritesTheSystemDevices(t *testing.T) {
	t.Parallel()

	for _, readOnly := range []bool{false, true} {
		plan := planOf(nil, "/w", "/t", nil, nil, readOnly, "")

		for _, path := range sandbox.SystemRead() {
			assert.Contains(t, plan.Read, path, "read-only %v", readOnly)
		}

		for _, path := range sandbox.SystemWrite() {
			assert.Contains(t, plan.Write, path, "read-only %v", readOnly)
		}
	}
}
