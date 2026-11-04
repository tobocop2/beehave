package github_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestCreatePR_OpensADraftAndReadsTheNumberFromTheAddress(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "https://github.com/"+repo+"/pull/42\n")
	req := model.NewPull{Head: "bb-1", Base: "round-1", Title: "--admin title", Body: "line one\nline two"}

	number, err := f.CreatePR(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, 42, number)
	assert.Equal(t, [][]string{{"pr", "create", "--draft", "--repo", repo, "--head", "bb-1", "--base", "round-1", "--title", "--admin title", "--body", "line one\nline two"}}, run.Calls)

	down, _ := failing(t, "HTTP 502")
	_, err = down.CreatePR(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot open a draft pull request for branch bb-1 into round-1")

	odd, _ := fixed(t, "created\n")
	_, err = odd.CreatePR(context.Background(), req)
	require.Error(t, err)

	huge, _ := fixed(t, "https://github.com/"+repo+"/pull/99999999999999999999\n")
	_, err = huge.CreatePR(context.Background(), req)
	require.Error(t, err)
}

func TestLinkStack_LinksTheChildOnTopOfTheParentByAddress(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.LinkStack(context.Background(), 3, 4))
	assert.Equal(t, [][]string{{"stack", "link", "https://github.com/" + repo + "/pull/3", "https://github.com/" + repo + "/pull/4"}}, run.Calls)

	down, _ := failing(t, "HTTP 502")
	err := down.LinkStack(context.Background(), 3, 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot link pull request 4 into the stack of pull request 3")
}
