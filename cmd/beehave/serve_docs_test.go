package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
)

const (
	reviewPollSentence = "set `agents.review_poll_minutes` to 60"
	stateDirName       = "BEEHAVE_STATE_DIR"
)

func readDoc(t *testing.T, name string) string {
	t.Helper()

	text, err := os.ReadFile(filepath.Join("..", "..", "docs", name)) //nolint:gosec // G304: the name is a constant of this test
	require.NoError(t, err)

	return string(text)
}

func TestServeDocs_TheSampleFilesAndTheReviewPollSentenceExist(t *testing.T) {
	t.Parallel()

	serve := readDoc(t, "serve.md")

	for _, want := range []string{
		"<key>KeepAlive</key>", "<key>RunAtLoad</key>", "<string>/usr/local/bin/beehave</string>", "<string>serve</string>",
		"[Service]", "ExecStart=/usr/local/bin/beehave serve", "Restart=on-failure",
		config.ConfigDirVariable, stateDirName, reviewPollSentence,
	} {
		assert.Contains(t, serve, want)
	}

	assert.Contains(t, readDoc(t, "design/durable-loop.md"), reviewPollSentence)
}

func TestServeDocs_TheReviewPollKeyAndTheValueOfTheSentenceLoad(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "runners.star"), []byte("agents(review_poll_minutes = 60)\nrunner(name = \"a\", agent = \"claude\")\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "projects", "p.star"), []byte("project(repo = \"o/n\", tracker = \"github\", forge = \"github\", claims = \"beads\", beads = beads(prefix = \"p\"), base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n"), 0o600))

	cfg, problems := config.LoadFolder(dir, map[string]string{}, newAdapters(nil, nil, nil).registry())

	require.Empty(t, problems)
	assert.Equal(t, 60, cfg.Agents.ReviewPollMinutes, "the key of the docs is the key of the config")
}
