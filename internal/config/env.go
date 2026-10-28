package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// ConfigDirVariable names the config folder when no flag does.
	ConfigDirVariable = "OBEE_CONFIG"

	dbosPrefix      = "DBOS__"
	stateDirVar     = "OBEE_STATE_DIR"
	xdgStateVar     = "XDG_STATE_HOME"
	stateAppDir     = "obee"
	defaultStateRel = ".local/state"
	defaultConfRel  = ".config/obee"
)

// DBOS reads these variables on its own and would send data to a cloud service, so obee refuses
// to start when any is set.
var dbosVariables = [...]string{"DBOS_DOMAIN", "DBOS_SYSTEM_DATABASE_URL"} //nolint:gochecknoglobals // read-only list of names

// ProcessEnv returns the process environment as a map. It is the only read of the environment.
func ProcessEnv() map[string]string {
	env := map[string]string{}

	for _, entry := range os.Environ() {
		if name, value, found := strings.Cut(entry, "="); found {
			env[name] = value
		}
	}

	return env
}

// RefuseDBOSVariables returns an error when any DBOS__* variable, DBOS_DOMAIN or
// DBOS_SYSTEM_DATABASE_URL is set. Each name is in the message with the fix.
func RefuseDBOSVariables(env map[string]string) error {
	var found []string

	for name := range env {
		if strings.HasPrefix(name, dbosPrefix) || isDBOSVariable(name) {
			found = append(found, name)
		}
	}

	if len(found) == 0 {
		return nil
	}

	slices.Sort(found)

	return refuse("%s is set. DBOS reads it and would connect to a service outside this machine. Unset it and start obee again.",
		strings.Join(found, ", "))
}

func isDBOSVariable(name string) bool {
	return slices.Contains(dbosVariables[:], name)
}

// FolderPath returns the config folder: the flag value, else OBEE_CONFIG, else ~/.config/obee.
func FolderPath(env map[string]string, flag string) (string, error) {
	if flag != "" {
		return expandHome(flag)
	}

	if dir := env[ConfigDirVariable]; dir != "" {
		return expandHome(dir)
	}

	return homeRelative(defaultConfRel)
}

func homeRelative(rel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", refuse("cannot find the home folder: %w. Set HOME.", err)
	}

	return filepath.Join(home, rel), nil
}
