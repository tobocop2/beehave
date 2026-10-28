package github_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestItemFields_DeclareBranchHeadShaPrAndAuthor(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, "")

	assert.Equal(t, []string{"branch", "head_sha", "pr", "author"}, f.ItemFields())
}

func TestItem_ReadsTheDeclaredFields(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `{"number":6,"headRefName":"fix-x","headRefOid":"abc","author":{"login":"gz"}}`)

	got, err := f.Item(context.Background(), 6)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"branch": "fix-x", "head_sha": "abc", "pr": "6", "author": "gz"}, got)
	assert.Equal(t, [][]string{{"pr", "view", "6", "--repo", repo, "--json", "number,headRefName,headRefOid,author"}}, run.Calls)
}

func TestItem_AMissingBranchAndHeadAreEmpty(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, `{"number":6}`)

	got, err := f.Item(context.Background(), 6)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"branch": "", "head_sha": "", "pr": "6", "author": ""}, got)
}

func TestItem_AFailedReadIsAnError(t *testing.T) {
	t.Parallel()

	f, _ := failing(t, "")

	_, err := f.Item(context.Background(), 6)
	require.Error(t, err)
}
