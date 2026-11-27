package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A library of the shared cache is not on disk and is left out; a relative name or a missing library outside the cache gives no read set.
func TestLibraryPath_LeavesOutTheSharedCacheAndRefusesWhatItCannotFind(t *testing.T) {
	t.Parallel()

	path, err := libraryPath("/usr/lib/libbeehest-absent.dylib")
	require.NoError(t, err)
	assert.Empty(t, path)

	for name, want := range map[string]string{
		"@rpath/libz.dylib":            "relative to a program",
		"/opt/absent/lib/libz.1.dylib": "find the library",
	} {
		_, err := libraryPath(name)
		assert.ErrorContains(t, err, want, name)
	}
}

// A universal program gives the libraries of the slice of this machine.
func TestMachoImports_ReadsTheSliceOfThisMachine(t *testing.T) {
	t.Parallel()

	names, err := machoImports("/usr/bin/true")
	require.NoError(t, err)
	assert.Contains(t, names, "/usr/lib/libSystem.B.dylib")

	_, err = machoImports("/etc/hosts")
	assert.ErrorContains(t, err, "read the libraries")
}
