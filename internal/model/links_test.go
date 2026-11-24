package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestBodyLinks_ReadsEachLinkOnceInTheOrderOfTheBody(t *testing.T) {
	t.Parallel()

	body := "Closes #7. Part of acme/tools#9, refs #7 and fixes https://github.com/acme/other/issues/3. Related to #4"

	links, err := model.BodyLinks(body)

	require.NoError(t, err)
	assert.Equal(t, []model.IssueLink{{Number: 7}, {Repo: "acme/tools", Number: 9}, {Repo: "acme/other", Number: 3}, {Number: 4}}, links)
	assert.Equal(t, "acme/tools#9", links[1].String())
	assert.Equal(t, "#7", links[0].String())

	none, err := model.BodyLinks("a plain body, see #7")

	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestBodyLinks_RefusesANumberThatIsNotAnInt(t *testing.T) {
	t.Parallel()

	_, err := model.BodyLinks("Closes acme/tools#99999999999999999999")

	require.ErrorContains(t, err, "cannot read the link acme/tools#99999999999999999999 in the body")
}

func TestWithoutListRefusals_KeepsTheFactsOfAReadThatOnlyLacksAListOrALinkAndNoOtherRefusal(t *testing.T) {
	t.Parallel()

	read := func(raw string) (model.PullFacts, error) {
		view, err := model.Decode[model.PullRequest]("test", raw)
		require.NoError(t, err)

		return model.WithoutListRefusals(view.Read(4))
	}

	for name, raw := range map[string]string{
		"a clean read":            `{"state":"OPEN","headRefOid":"abc","baseRefName":"main","isDraft":false,"labels":[],"assignees":[]}`,
		"no lists":                `{"state":"OPEN","headRefOid":"abc","baseRefName":"main"}`,
		"a bad link":              `{"state":"OPEN","headRefOid":"abc","baseRefName":"main","isDraft":false,"labels":[],"assignees":[],"body":"Closes #99999999999999999999"}`,
		"a bad link and no lists": `{"state":"OPEN","headRefOid":"abc","baseRefName":"main","body":"Closes #99999999999999999999"}`,
	} {
		facts, err := read(raw)

		require.NoError(t, err, name)
		assert.Equal(t, "abc", facts.Head, name)
		assert.Equal(t, 4, facts.Number, name)
	}

	_, err := read(`{"state":"OPEN","baseRefName":"main","isDraft":false,"labels":[],"assignees":[]}`)

	require.ErrorContains(t, err, "the read has no head", "a read with no head stays a refusal")

	_, err = read(`{"state":"OPEN","baseRefName":"main","body":"Closes #99999999999999999999"}`)

	require.ErrorContains(t, err, "the read has no head", "a bad link does not hide a missing head")
}

func TestPullRequest_ReadNamesEveryNullFieldAndReadsTheRest(t *testing.T) {
	t.Parallel()

	view, err := model.Decode[model.PullRequest]("test", `{"state":"OPEN","headRefOid":"abc","baseRefName":"main","body":"Closes #7","author":{"login":"dev"}}`)
	require.NoError(t, err)

	facts, err := view.Read(4)

	var hole *model.IncompletePull

	require.ErrorAs(t, err, &hole)
	assert.Equal(t, []string{"draft", "labels", "assignees"}, hole.Missing)
	assert.Equal(t, "cannot read pull request 4: the read has no draft, labels, assignees", err.Error())
	assert.Equal(t, 4, facts.Number)
	assert.Equal(t, []model.IssueLink{{Number: 7}}, facts.Links)

	view, err = model.Decode[model.PullRequest]("test", `{"state":"OPEN","headRefOid":"abc","baseRefName":"main","body":"Closes #99999999999999999999","isDraft":true,"labels":[],"assignees":[]}`)
	require.NoError(t, err)

	_, err = view.Read(4)

	require.ErrorContains(t, err, "cannot read the link")
}
