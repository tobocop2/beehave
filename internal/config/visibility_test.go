package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestLoadFolder_TheVisibilityKeyIsReadAnUnknownOrEmptyOneIsRefusedAndNoneMeansUnknown(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(goodProject+`, visibility = "private"`))
	require.Empty(t, problems)
	require.Equal(t, model.VisibilityPrivate, project.Visibility)

	project, problems = loadProjectText(t, call(goodProject+`, visibility = "public"`))
	require.Empty(t, problems)
	require.Equal(t, model.VisibilityPublic, project.Visibility)

	project, problems = loadProjectText(t, call(goodProject))
	require.Empty(t, problems)
	require.Empty(t, project.Visibility)

	_, problems = loadProjectText(t, call(goodProject+`, visibility = ""`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), "`visibility` must be a non-empty string")

	_, problems = loadProjectText(t, call(goodProject+`, visibility = "internal"`))
	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), `unknown visibility "internal"`)
}
