package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/model"
)

const (
	fakeSkyLaunch = "#!/bin/sh\nfor last in \"$@\"; do :; done\ncat \"$last\" > \"$SETUP_SEEN\"\nexit 0\n"
	fakeSSHFlock  = "#!/bin/sh\necho 'flock from util-linux 2.39.3'\nexit 0\n"
)

// setupProject is a project on a SkyPilot host that has a setup command and a source to clone.
func setupProject() model.Project {
	return model.Project{
		Name: "p", BuildHost: "lane-host", BaseBranch: "main", Source: "https://example.test/o/n.git",
		Host: &model.HostConfig{Provider: "skypilot", IdleMinutes: 45, MaxRun: time.Hour, SetupCommand: "make setup"},
	}
}

// fakeCluster puts a sky and an ssh first on the PATH. The fake sky copies the task file of a launch to the file that the function returns.
func fakeCluster(t *testing.T) string {
	t.Helper()

	bin, seen := t.TempDir(), filepath.Join(t.TempDir(), "task")
	require.NoError(t, exectest.Write(filepath.Join(bin, "sky"), fakeSkyLaunch))
	require.NoError(t, exectest.Write(filepath.Join(bin, "ssh"), fakeSSHFlock))
	t.Setenv("PATH", bin+":"+testPath)
	t.Setenv("SETUP_SEEN", seen)

	return seen
}

func TestNewSkyPilotHost_TheSetupCommandRunsInACloneOfTheSourceAtTheBaseBranchOnAFreshCluster(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	seen := fakeCluster(t)
	noForge := func(model.Project) (forge.Forge, error) {
		return nil, errors.New("the forge is asked although the project has a source")
	}

	h, err := newSkyPilotHost(setupProject(), noForge)
	require.NoError(t, err)
	require.NoError(t, h.Up(t.Context()))

	task, err := os.ReadFile(filepath.Clean(seen))
	require.NoError(t, err)
	assert.Contains(t, string(task), "git clone --branch main https://example.test/o/n.git p")
	assert.Contains(t, string(task), "make setup")
}

func TestNewSkyPilotHost_ASetupCommandOfAProjectWithNoSourceTakesTheCloneURLOfItsForge(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	seen := fakeCluster(t)
	project := setupProject()
	project.Source = ""

	h, err := newSkyPilotHost(project, forgeAt("https://example.test/forge/n.git"))
	require.NoError(t, err)
	require.NoError(t, h.Up(t.Context()))

	task, err := os.ReadFile(filepath.Clean(seen))
	require.NoError(t, err)
	assert.Contains(t, string(task), " https://example.test/forge/n.git p")
}

func TestNewSkyPilotHost_ASetupCommandOfAProjectWithNoSourceAndNoForgeIsRefusedWithTheForgeError(t *testing.T) {
	t.Parallel()

	project := setupProject()
	project.Source = ""

	_, err := newSkyPilotHost(project, func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge })

	require.ErrorIs(t, err, forge.ErrNoForge)
}

func TestHostBlocks_TheSpotDiskSizeAndSetupCommandKeysLoadAndTheTableBuildsTheHostOrRefusesTheValue(t *testing.T) {
	t.Parallel()

	const head = `, host = host(provider = "skypilot", disk_floor_gb = 50, idle_minutes = 30`

	project, problems := loadHostProject(t, hostProjectTail+head+`, setup_command = "make setup", skypilot = skypilot(spot = True, disk_size_gb = 100))`)

	require.Empty(t, problems)
	assert.Equal(t, "make setup", project.Host.SetupCommand)

	spot, given := project.Host.Block.Bool("spot")
	assert.True(t, given)
	assert.True(t, spot)

	disk, given := project.Host.Block.Whole("disk_size_gb")
	assert.True(t, given)
	assert.Equal(t, 100, disk)

	_, err := newAdapters(nil, nil, nil).host(project.Project)
	require.NoError(t, err)

	_, problems = loadHostProject(t, hostProjectTail+head+`, skypilot = skypilot(spot = "yes"))`)

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "want True or False")

	bad, problems := loadHostProject(t, hostProjectTail+head+`, skypilot = skypilot(disk_size_gb = 5))`)

	require.Empty(t, problems, "the loader reads the number; the host refuses the range")

	_, err = newAdapters(nil, nil, nil).host(bad.Project)
	require.ErrorContains(t, err, "`host.skypilot.disk_size_gb` must be a whole number from 10 to 65536")
}

func TestNewSkyPilotHost_AProjectWithNoSetupCommandAsksNoForgeForAURL(t *testing.T) {
	t.Parallel()

	project := setupProject()
	project.Source = ""
	project.Host.SetupCommand = ""

	_, err := newSkyPilotHost(project, func(model.Project) (forge.Forge, error) {
		return nil, errors.New("the forge is asked for a project with no setup command")
	})

	require.NoError(t, err)
}
