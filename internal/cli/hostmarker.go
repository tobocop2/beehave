package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
)

const markerMode os.FileMode = 0o600

// markerPath is the file in the state folder that records the cluster that host up started for a project.
// A cluster name holds no @, so the project and the cluster name cannot run together.
func markerPath(env map[string]string, project model.Project) (string, error) {
	dir, err := config.StateDir(env)
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "host-"+project.Name+"@"+project.BuildHost), nil
}

// writeMarker records the id of the cluster. A reader sees the old id or the new one, never part of it.
func writeMarker(path, id string) error {
	dir := filepath.Dir(path)

	if err := config.EnsureStateDir(dir); err != nil {
		return err
	}

	// One obee command writes a marker at a time per project, so the file beside the marker has one writer.
	next := path + ".tmp"

	err := os.WriteFile(next, []byte(id), markerMode)
	if err == nil {
		err = os.Rename(next, path)
	}

	return err
}

// startAndMark starts a cluster that is not up and writes the marker of it. Only the command that starts a cluster marks it.
// A cluster that is up already is not obee's to stop: no marker is written, and the call succeeds only when the marker names it.
func startAndMark(in input, machine Machine, project model.Project) error {
	status, err := machine.Status(in.ctx)
	if err != nil {
		return err
	}

	path, err := markerPath(in.env, project)
	if err != nil {
		return err
	}

	if status.State == model.HostUp {
		return reportRunning(in, machine, path, project)
	}

	return upAndWrite(in, machine, path)
}

// upAndWrite starts the cluster and writes the marker with the id that the host reports for it. A failed start writes no marker.
func upAndWrite(in input, machine Machine, path string) error {
	if err := machine.Up(in.ctx); err != nil {
		return err
	}

	id, err := clusterID(in, machine)
	if err != nil {
		return err
	}

	return writeMarker(path, id)
}

// reportRunning says that the cluster was running before this command. It succeeds when the marker names the cluster.
func reportRunning(in input, machine Machine, path string, project model.Project) error {
	id, err := clusterID(in, machine)
	if err != nil {
		return err
	}

	text, err := os.ReadFile(path) //nolint:gosec // the path is the state folder plus the names of a project that the config holds
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	notice := fmt.Sprintf("the cluster of project %s was already running", project.Name)

	if err == nil && strings.TrimSpace(string(text)) == id {
		say(in.stdout, notice+" and obee started it")

		return nil
	}

	return fmt.Errorf("%s, and obee will not stop it", notice)
}

// teardownOwned stops the cluster when the marker names it, and removes the marker after the teardown succeeds.
func teardownOwned(in input, machine Machine, project model.Project) error {
	path, err := ownedMarker(in, machine, project)
	if err != nil {
		return err
	}

	if err = machine.Teardown(in.ctx); err != nil {
		return err
	}

	return os.Remove(path)
}

// ownedMarker returns the path of the marker and refuses unless it names the id of the cluster that the host reports now.
func ownedMarker(in input, machine Machine, project model.Project) (string, error) {
	path, err := markerPath(in.env, project)
	if err != nil {
		return "", err
	}

	id, err := clusterID(in, machine)
	if err != nil {
		return "", err
	}

	text, err := os.ReadFile(path) //nolint:gosec // the path is the state folder plus the names of a project that the config holds
	if errors.Is(err, fs.ErrNotExist) || (err == nil && strings.TrimSpace(string(text)) != id) {
		return "", fmt.Errorf("obee did not start the cluster of project %s, so host clean leaves it up. Start it with `obee host up`, or stop it yourself", project.Name)
	}

	return path, err
}
