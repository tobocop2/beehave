package model_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestPullRequest_ReadGivesTheLabelsTheReviewersAndTheRequestedLogins(t *testing.T) {
	t.Parallel()

	view, err := model.Decode[model.PullRequest]("test", `{"state":"OPEN","headRefOid":"abc","baseRefName":"main","isDraft":true,"assignees":[{"login":"a"}],`+
		`"labels":[{"name":"hold"},{"name":"bug"}],"reviews":[{"author":{"login":"rev"}}],"reviewRequests":[{"login":"asked"}]}`)
	require.NoError(t, err)

	facts, err := view.Read(4)

	require.NoError(t, err)
	assert.Equal(t, []string{"hold", "bug"}, facts.Labels)
	assert.Equal(t, []string{"rev", "asked"}, facts.Reviewers, "the reviewers come first, then the logins that were asked")
	assert.Equal(t, []string{"a"}, facts.Assignees)
	assert.True(t, facts.Draft)
}

func TestLinkError_NamesTheLinkAndUnwrapsToItsCause(t *testing.T) {
	t.Parallel()

	view, err := model.Decode[model.PullRequest]("test", `{"state":"OPEN","headRefOid":"abc","baseRefName":"main","isDraft":false,"labels":[],"assignees":[],"body":"Closes #99999999999999999999"}`)
	require.NoError(t, err)

	_, err = view.Read(4)

	var link *model.LinkError

	require.ErrorAs(t, err, &link)
	assert.Equal(t, "abc", link.Facts.Head, "the facts that the read did give stay")
	require.Error(t, errors.Unwrap(err), "the cause of the refusal stays reachable")
	assert.ErrorContains(t, errors.Unwrap(err), "cannot read the link")
}
