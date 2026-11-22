package main

import (
	"path/filepath"
	"strings"
)

const (
	// xcodePreferences is the file that xcrun reads for the license check of Xcode.
	xcodePreferences = "/Library/Preferences/com.apple.dt.Xcode.plist"
	// xcrunCache is the name of the cache file of xcrun in the temporary folder of a command.
	xcrunCache   = "xcrun_db"
	bundleSuffix = ".app"
)

// developerReads returns the paths that a plan opens for the developer folder: the folder, the .app bundle that holds it (Xcode) and the preferences
// file. It returns nothing when folder is empty.
func developerReads(folder string) []string {
	if folder == "" {
		return nil
	}

	reads := []string{folder}

	for dir := folder; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if strings.HasSuffix(dir, bundleSuffix) {
			reads = append(reads, dir)

			break
		}
	}

	return append(reads, xcodePreferences)
}

// developerEnv returns the variables that make the shims of Apple use the developer folder and keep the cache of xcrun in tmp, so that no write outside the
// plan is needed. It returns nothing when folder is empty.
func developerEnv(folder, tmp string) []string {
	if folder == "" {
		return nil
	}

	return []string{"DEVELOPER_DIR=" + folder, "xcrun_db=" + filepath.Join(tmp, xcrunCache)}
}
