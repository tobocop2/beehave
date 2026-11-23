package agent

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlacePrompt_AFolderThatIsGoneIsAReasonAndNoRun(t *testing.T) {
	t.Parallel()

	in, args, problem := placePrompt("p", PromptFile, filepath.Join(t.TempDir(), "gone"), []string{"a"})

	assert.Nil(t, in)
	assert.Nil(t, args)
	assert.Contains(t, problem, "cannot write the prompt file")
}
