// Package gotelemetry turns the telemetry of the go command off in an empty home folder. With telemetry on or local, the go command starts a child
// process that it does not wait for, and the child outlives the command and writes into the home folder.
package gotelemetry

import (
	"errors"
	"os"
	"path/filepath"
)

const (
	// ConfigFolder is the config folder of the go command under a home folder, which XDG_CONFIG_HOME names.
	ConfigFolder = ".config"
	// Mode is the content of the mode file that turns telemetry off.
	Mode = "off\n"
	// folderMode is the mode of the folders of the mode file: only the owner reads them.
	folderMode = 0o700
	// fileMode is the mode of the mode file.
	fileMode = 0o600
)

// ModeFiles returns the paths of the telemetry mode file under a home folder, relative to it: under the config folder that XDG_CONFIG_HOME names,
// and under Library/Application Support, where the go command looks on macOS.
func ModeFiles() []string {
	return []string{
		filepath.Join(ConfigFolder, "go", "telemetry", "mode"),
		filepath.Join("Library", "Application Support", "go", "telemetry", "mode"),
	}
}

// Off writes the mode off at each path of ModeFiles under home, and returns every error.
func Off(home string) error {
	var err error

	for _, file := range ModeFiles() {
		path := filepath.Join(home, file)
		err = errors.Join(err, os.MkdirAll(filepath.Dir(path), folderMode), os.WriteFile(path, []byte(Mode), fileMode))
	}

	return err
}
