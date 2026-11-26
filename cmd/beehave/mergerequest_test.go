package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
)

func TestNewLife_TheMergeRequestCarriesTheAlertKeyAndTheCIFilesOfTheForge(t *testing.T) {
	t.Parallel()

	project := config.Project{Project: model.Project{Name: "p", Forge: forgegithub.Name, SecurityAlerts: true}}
	life := newLife(config.Config{Projects: map[string]config.Project{"p": project}}, newAdapters(nil, nil, nil), worktrees{})

	request := life.request(project, 7, gated, flow.MergeSettings{}, nil)

	assert.True(t, request.Alerts, "the key security_alerts of the project turns the check on")
	assert.Equal(t, []string{forgegithub.WorkflowDir}, request.CIPaths)
	assert.Equal(t, forgegithub.WorkflowFix, request.CIFix)
}

func TestNewLife_TheMergeRequestOfAProjectWithoutTheAlertKeyChecksNoAlerts(t *testing.T) {
	t.Parallel()

	project := config.Project{Project: model.Project{Name: "p", Forge: forgegithub.Name}}
	life := newLife(config.Config{Projects: map[string]config.Project{"p": project}}, newAdapters(nil, nil, nil), worktrees{})

	request := life.request(project, 7, gated, flow.MergeSettings{}, nil)

	assert.False(t, request.Alerts)
}
