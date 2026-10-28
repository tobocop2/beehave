package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	// runnersFileName is the file with the agent logins, the agent limit and the pool caps.
	runnersFileName = "runners.toml"
	// projectsDirName is the folder with one TOML file for each project.
	projectsDirName = "projects"
	// localHost is the build host that means this machine.
	localHost = "local"

	runnerKeys  = "name, agent, config_dir, keychain"
	poolKeys    = "five_hour_max, seven_day_max"
	agentsKeys  = "max_concurrent"
	runnersTop  = "agents, pool, runner"
	projectKeys = "repo, tracker, forge, beads_prefix, base_branch, gate, build_host, steps"
)

// The value goes into an ssh command, so a cluster name is a letter, then letters, digits, hyphens.
var clusterName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

// decodeFile reads a TOML file as bytes and decodes it strictly into target. allowed lists the
// keys of the file for the message about an unknown key.
func decodeFile(path string, target any, allowed string) error {
	raw, err := os.ReadFile(path) //nolint:gosec // the config folder is the user's own
	if err != nil {
		return refuse("cannot read %s: %w. Check that the file exists and that you can read it.", path, err)
	}

	if err = checkSyntax(path, raw); err != nil {
		return err
	}

	err = toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(target)
	if err == nil {
		return nil
	}

	return describeDecodeError(path, err, allowed)
}

// describeDecodeError names an unknown key or a key of the wrong type.
func describeDecodeError(path string, err error, allowed string) error {
	var missing *toml.StrictMissingError
	if errors.As(err, &missing) {
		return refuse("%s: unknown key `%s`. Remove it, or use one of: %s.", path, strings.Join(missing.Errors[0].Key(), "."), allowed)
	}

	var invalid *toml.DecodeError

	if !errors.As(err, &invalid) || len(invalid.Key()) == 0 {
		return refuse("%s cannot be read: %w. Write it as the example config shows.", path, err)
	}

	return refuse("%s: `%s` has the wrong type. Write it as the example config shows.", path, strings.Join(invalid.Key(), "."))
}

// checkSyntax decodes into a map, which finds syntax errors and repeated keys that a typed decode
// would report as a wrong type.
func checkSyntax(path string, raw []byte) error {
	var syntax map[string]any
	if err := toml.Unmarshal(raw, &syntax); err != nil {
		return refuse("%s is not valid TOML: %w. Fix the syntax, or remove the repeated key.", path, err)
	}

	return nil
}

func requireText(path, key, value string) error {
	if strings.TrimSpace(value) == "" {
		return refuse("%s: `%s` must be a non-empty string. Set it.", path, key)
	}

	return nil
}

// expandHome replaces a leading "~" with the home folder.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", refuse("cannot expand `%s`: %w. Set HOME, or write the full path.", path, err)
	}

	return filepath.Join(home, path[1:]), nil
}
