package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// developerLookup is the time that xcode-select has to name the developer folder.
const developerLookup = 10 * time.Second

// developerFolder returns the developer folder that xcode-select names for environ, the environment of the engine, or "" when it names none that exists.
// DEVELOPER_DIR in environ wins, as xcode-select documents. The engine calls it once at start, before any head code runs.
func developerFolder(environ []string) string {
	return developerFolderWithin(environ, developerLookup)
}

// developerFolderWithin is developerFolder with a limit on the time that xcode-select runs.
func developerFolderWithin(environ []string, limit time.Duration) string {
	program, err := exec.LookPath("xcode-select")
	if err != nil {
		return ""
	}

	cmd := exec.Command(program, "-p") //nolint:gosec,noctx // xcode-select is found on the PATH of the engine, which has no context at start; the timer ends it
	cmd.Env = environ
	cmd.WaitDelay = limit

	timer := time.AfterFunc(limit, func() { _ = cmd.Process.Kill() }) //nolint:errcheck // a process that ended already needs no kill
	defer timer.Stop()

	out, err := cmd.Output()
	folder := strings.TrimSpace(string(out))

	if info, statErr := os.Stat(folder); err != nil || statErr != nil || !info.IsDir() || !filepath.IsAbs(folder) {
		return ""
	}

	return folder
}
