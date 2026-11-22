package agent

import (
	"math"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/fstree"
)

// maxPid is the largest process id of Linux (pid_max of a 64-bit kernel is 4194304).
const maxPid = 4194304

func TestTempPath_TheLongestTemporaryFolderKeepsTheCLIHomeUnderItsLimit(t *testing.T) {
	t.Parallel()

	temp := filepath.Join(shortTemp, userTempName(math.MaxUint32), fstree.Name(maxPid, fstree.Attempts-1))
	home := temp + "/claude-" + strconv.FormatUint(math.MaxUint32, 10)

	assert.LessOrEqual(t, len(home), cliTempLimit,
		"Claude Code 2.1.330 sets the TMPDIR of the shell to /tmp/claude-<uid> when <CLAUDE_CODE_TMPDIR>/claude-<uid> is over 44 bytes, which leaves the Go cache of the step in a folder that nothing removes: %s", home)
}

func TestUserTempName_WritesTheUidInBase36(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "b0", userTempName(0))
	assert.Equal(t, "bdx", userTempName(501))
}
