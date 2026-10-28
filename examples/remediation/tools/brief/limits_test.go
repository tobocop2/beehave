package brief_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/brief"
)

func TestReadText_ReturnsTheBytesAsWritten(t *testing.T) {
	t.Parallel()

	text, err := brief.ReadText(writeFile(t, "a.md", "a\r\nb\xff\n"))

	require.NoError(t, err)
	assert.Equal(t, "a\r\nb\xff\n", text)
}

func TestReadText_RefusesAFileThatCannotBeRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := brief.ReadText(dir)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot read "+dir+": ")
	assert.Contains(t, err.Error(), ". Check that the file exists and that you can read it.")
}

func TestReadText_RefusesAFileWithANulByte(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "a.md", "a\x00b")

	_, err := brief.ReadText(path)

	require.Error(t, err)
	assert.Equal(t, path+" holds a NUL byte. Rewrite it as plain text.", err.Error())
}

func TestReadBytes_SumsTheSizesAndSkipsABlankPath(t *testing.T) {
	t.Parallel()

	plan := writeFile(t, "plan.md", strings.Repeat("x", 500))
	report := writeFile(t, "report.md", strings.Repeat("y", 3000))

	got, err := brief.ReadBytes([]string{plan, "", report})

	require.NoError(t, err)
	assert.Equal(t, 3500, got)
}

func TestReadBytes_CountsBytesNotCharacters(t *testing.T) {
	t.Parallel()

	got, err := brief.ReadBytes([]string{writeFile(t, "u.md", "é\xff")})

	require.NoError(t, err)
	assert.Equal(t, 3, got)
}

func TestReadBytes_AnEmptyListIsZero(t *testing.T) {
	t.Parallel()

	got, err := brief.ReadBytes(nil)

	require.NoError(t, err)
	assert.Zero(t, got)
}

func TestReadBytes_RefusesAPathThatIsNotAFile(t *testing.T) {
	t.Parallel()

	for _, path := range []string{filepath.Join(t.TempDir(), "gone.md"), t.TempDir()} {
		_, err := brief.ReadBytes([]string{path})

		require.Error(t, err, path)
		assert.Contains(t, err.Error(), "the required reading names "+path+", which is not a file here, so its size is unknown.")
	}
}

func TestReadBytes_RefusesAFileWithANulByte(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "n.md", "\x00")

	_, err := brief.ReadBytes([]string{path})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds a NUL byte")
}

func TestReadBytes_FollowsASymlinkToAFile(t *testing.T) {
	t.Parallel()

	target := writeFile(t, "target.md", "abcd")
	link := filepath.Join(t.TempDir(), "link.md")
	require.NoError(t, os.Symlink(target, link))

	got, err := brief.ReadBytes([]string{link})

	require.NoError(t, err)
	assert.Equal(t, 4, got)
}

func TestSizeGate_PassesAtTheLimitAndRefusesAboveIt(t *testing.T) {
	t.Parallel()

	require.NoError(t, brief.SizeGate(1000, 500, 1500))

	err := brief.SizeGate(1000, 501, 1500)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the required reading is 1501 bytes (the brief 1000, the files under '## Read first' 501)")
	assert.Contains(t, err.Error(), "above 1500 bytes, about 375 tokens.")
	assert.Contains(t, err.Error(), "Cut the list: a fix lane reads the plan, the handoff and the open findings, not the whole report.")
}

func TestSizeGate_TheDefaultLimitIsAboutFortyThousandTokens(t *testing.T) {
	t.Parallel()

	require.NoError(t, brief.SizeGate(brief.ReadLimit, 0, brief.ReadLimit))
	assert.Equal(t, 40000, brief.ReadLimit/brief.BytesPerToken)
}

func TestCheckCap_RefusesACapThatIsNotAboveZero(t *testing.T) {
	t.Parallel()

	require.NoError(t, brief.CheckCap(1))
	require.NoError(t, brief.CheckCap(brief.ContextCap))

	for _, bad := range []int{0, -5} {
		err := brief.CheckCap(bad)

		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "must be a whole number of tokens above zero.")
	}
}
