// Package realtools holds the live suite of beehave: tests that run the pinned gh, bd and claude themselves, and the sandbox repository. Every test starts only when
// BEEHAVE_REAL_TOOLS is 1, which make test-real-tools sets after it installs the pinned tools. A row that needs a token skips with its reason when the token is missing.
package realtools
