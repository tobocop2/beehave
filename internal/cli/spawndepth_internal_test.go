package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

func TestEngineOptions_TheItemsQueuesReachTheHighestMaxSpawnDepthOfTheProjects(t *testing.T) {
	t.Parallel()

	projects := map[string]config.Project{
		"a": {Project: model.Project{MaxSpawnDepth: 2}},
		"b": {Project: model.Project{MaxSpawnDepth: 5}},
		"c": {Project: model.Project{MaxSpawnDepth: 1}},
	}

	assert.Equal(t, 5, flowCommands{}.engineOptions(config.Config{Projects: projects}, "/state").MaxSpawnDepth)
	assert.Equal(t, 0, flowCommands{}.engineOptions(config.Config{}, "/state").MaxSpawnDepth, "no project gives the default of the engine")
}
