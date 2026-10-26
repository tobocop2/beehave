// Package config loads the round settings, the config folder and the state folder.
// It is the only package that reads the process environment.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// roundEnvName is the file name of the round settings file.
	roundEnvName = "round.env"

	roundEnvVariable = "ROUND_ENV"
	scratchpadVar    = "SCRATCHPAD"
	blanks           = " \t"
	quotes           = "\"'"
	asciiLimit       = 0x80
	minQuotedLength  = 2
)

var shellName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ErrRefused marks a config problem that the user fixes by editing a file or a variable.
var ErrRefused = errors.New("refused")

// Pair is one KEY=value line of a round.env file.
type Pair struct {
	Key   string
	Value string
}

// refusal wraps the formatted message, so an %w cause stays reachable with errors.Is.
type refusal struct {
	err error
}

func (r refusal) Error() string { return r.err.Error() }

func (r refusal) Unwrap() error { return r.err }

func (r refusal) Is(target error) bool { return target == ErrRefused }

func refuse(format string, args ...any) error {
	return refusal{err: fmt.Errorf(format, args...)}
}

// ParseRoundEnv returns the KEY=value pairs of a round.env file, in file order.
// A blank line, a comment line and a line with no "=" are skipped. A key that is not a shell
// name is skipped after its blanks are removed.
func ParseRoundEnv(path string) ([]Pair, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the caller names the round.env path
	if err != nil {
		return nil, refuse("cannot read %s: %w. Check that the file exists and that you can read it.", path, err)
	}

	if !utf8.Valid(raw) {
		return nil, refuse("%s is not valid UTF-8. Rewrite it as plain UTF-8 text.", path)
	}

	if bytes.ContainsRune(raw, '\r') {
		return nil, refuse("%s holds a carriage return (a Windows line ending?). Rewrite it with plain newlines.", path)
	}

	if bytes.ContainsRune(raw, 0) {
		return nil, refuse("%s holds a NUL byte. Rewrite it as plain text.", path)
	}

	return parseLines(path, string(raw))
}

func parseLines(path, text string) ([]Pair, error) {
	var pairs []Pair

	for line := range strings.SplitSeq(text, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || strings.HasPrefix(strings.TrimLeft(line, blanks), "#") {
			continue
		}

		if !isASCII(key) {
			return nil, refuse("%s holds a key that is not ASCII. Rename the key to a shell name.", path)
		}

		key = strings.NewReplacer(" ", "", "\t", "").Replace(key)
		if shellName.MatchString(key) {
			pairs = append(pairs, Pair{Key: key, Value: unquote(value)})
		}
	}

	return pairs, nil
}

func unquote(value string) string {
	if len(value) >= minQuotedLength && value[0] == value[len(value)-1] && strings.ContainsRune(quotes, rune(value[0])) {
		return value[1 : len(value)-1]
	}

	return value
}

func isASCII(text string) bool {
	for i := range len(text) {
		if text[i] >= asciiLimit {
			return false
		}
	}

	return true
}

// FindRoundEnv returns the round.env path to use; the file may not exist. A non-empty ROUND_ENV
// wins alone. Else the first file in beside, its parent or SCRATCHPAD, else ./round.env.
func FindRoundEnv(env map[string]string, beside string) string {
	if explicit := env[roundEnvVariable]; explicit != "" {
		return explicit
	}

	for _, candidate := range searchPaths(env, beside) {
		if isFile(candidate) {
			return candidate
		}
	}

	return roundEnvName
}

func searchPaths(env map[string]string, beside string) []string {
	var candidates []string
	if beside != "" {
		candidates = append(candidates,
			filepath.Join(beside, roundEnvName), filepath.Join(filepath.Dir(beside), roundEnvName))
	}

	if scratch := env[scratchpadVar]; scratch != "" {
		candidates = append(candidates, filepath.Join(scratch, roundEnvName))
	}

	return candidates
}

func isFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

// Round is a round.env path and the environment after the file filled in the unset keys.
type Round struct {
	EnvFile string
	Env     map[string]string
}

// LoadRound finds round.env, then merges it under env. A key counts as set when it is present,
// empty or not, so a caller who exports an empty value keeps it.
func LoadRound(env map[string]string, beside string) (Round, error) {
	path := FindRoundEnv(env, beside)
	merged := maps.Clone(env)

	if isFile(path) {
		pairs, err := ParseRoundEnv(path)
		if err != nil {
			return Round{}, err
		}

		for _, pair := range pairs {
			if _, set := merged[pair.Key]; !set {
				merged[pair.Key] = pair.Value
			}
		}
	}

	return Round{EnvFile: path, Env: merged}, nil
}

// Settings returns the typed settings of this round.
func (r Round) Settings() Settings {
	return Settings{env: r.Env}
}

// Need returns the value of a required key, or a refusal that names the fix.
func (r Round) Need(name, why string) (string, error) {
	if value := r.Env[name]; value != "" {
		return value, nil
	}

	tail := ""
	if why != "" {
		tail = " " + why
	}

	return "", refuse("%s is not set. Export it, or put it in %s.%s", name, r.EnvFile, tail)
}

// Root returns the directory that holds round.env. Round state goes here, never beside the tools.
// The path is made absolute without resolving symlinks.
func (r Round) Root() (string, error) {
	// A working directory that cannot be read leaves abs empty, and the file test then refuses.
	abs, _ := filepath.Abs(r.EnvFile) //nolint:errcheck // an empty abs fails the file test below

	if !isFile(abs) {
		return "", refuse("no round.env at %s, so there is no round root to write round state into. Set ROUND_ENV.", r.EnvFile)
	}

	return filepath.Dir(abs), nil
}
