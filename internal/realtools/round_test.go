package realtools_test

import (
	"os"
	"testing"
)

// TestRealRound_OneRoundOnTheSandbox is the release round: one round of the remediation example on the sandbox repository, with one small issue and one real claude step.
// It costs an agent run, so it starts only when BEEHAVE_REAL_ROUND is 1, which only the release checklist in CONTRIBUTING.md asks for.
func TestRealRound_OneRoundOnTheSandbox(t *testing.T) {
	requireToken(t)

	if os.Getenv("BEEHAVE_REAL_ROUND") != "1" {
		t.Skip("BEEHAVE_REAL_ROUND is not 1: the release round makes a paid claude call, so only the release checklist starts it")
	}

	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
		t.Skip("CLAUDE_CODE_OAUTH_TOKEN is not set: the release round makes a paid claude call")
	}

	t.Fatal("the round project of the sandbox is not written yet: it needs the sandbox repository, its token and its workflow file from the owner")
}
