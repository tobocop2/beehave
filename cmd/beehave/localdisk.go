package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/proc"
)

// localDisk is the disk floor of a lane that starts on this machine. It measures the worktree root and the state folder, and the floor and the wait are the
// `host.disk_floor_gb` and `host.disk_wait_minutes` of the project. A project with no host block, and a localDisk with no free function, is not checked.
type localDisk struct {
	projects map[string]config.Project
	// root is the folder of the worktrees of the items. state is the state folder, which holds the store and the logs.
	root, state string
	free        host.FreeSpace
	poll        time.Duration
}

// newLocalDisk returns the disk floor of the lanes of the projects, which reads the free space with `df`.
func newLocalDisk(projects map[string]config.Project, stateDir string) localDisk {
	return localDisk{
		projects: projects,
		root:     filepath.Join(stateDir, "trees"), state: stateDir,
		free: host.DfFree(proc.NewExecRunner("df", "")),
		poll: defaultDiskPoll,
	}
}

// check refuses a start of a lane of the project when the worktree root or the state folder is under the floor of the project, after the wait of the project.
func (d localDisk) check(ctx context.Context, project string) error {
	found, ok := d.projects[project]
	if !ok || found.Host == nil || d.free == nil {
		return nil
	}

	wait := time.Duration(found.Host.DiskWaitMinutes) * time.Minute

	return host.LocalFloor{Free: d.free, FloorGB: found.Host.DiskFloorGB, Wait: wait, Poll: d.poll}.Check(ctx, d.root, d.state)
}
