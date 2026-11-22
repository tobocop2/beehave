// Package credential reads the forge tokens of the engine and builds the environment of a child that holds one.
package credential

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

const (
	homeFolder    = "~/"
	tokenFolder   = ".config/beehave/credentials"
	fileMode      = 0o600
	folderMode    = 0o700
	commandLimit  = 30 * time.Second
	homeVariable  = "HOME"
	pathVariable  = "PATH"
	loginVariable = "USER"
)

var (
	errEmpty   = errors.New("the credential gave an empty token")
	errMultiline = errors.New("the credential gave a token of more than one line")
)

// Load reads the token that the credential names. env is the environment of the engine: it gives the home folder for a file, and the path and home for a command.
// A file must lie in ~/.config/beehave/credentials, have mode 0600 and belong to the user, in a folder of mode 0700. A command runs once with no shell and sees only PATH, HOME and USER.
func Load(ctx context.Context, cred model.Credential, env map[string]string) (string, error) {
	var (
		token string
		err   error
	)

	if len(cred.Command) > 0 {
		token, err = fromCommand(ctx, cred.Command, env)
	} else {
		token, err = fromFile(cred.File, env[homeVariable])
	}

	if err != nil {
		return "", err
	}

	token = strings.TrimSpace(token)

	switch {
	case token == "":
		return "", errEmpty
	case strings.ContainsAny(token, "\r\n"):
		return "", errMultiline
	}

	return token, nil
}

// fromCommand runs the program and returns what it printed.
func fromCommand(ctx context.Context, words []string, env map[string]string) (string, error) {
	runner := proc.NewExecRunner(words[0], "")
	runner.Timeout = commandLimit
	runner.Only = []string{pathVariable + "=" + env[pathVariable], homeVariable + "=" + env[homeVariable], loginVariable + "=" + env[loginVariable]}

	done := runner.Run(ctx, words[1:])
	if done.ExitCode != 0 {
		return "", fmt.Errorf("the credential command %s exited with code %d. Run it by hand and fix its error", words[0], done.ExitCode)
	}

	return done.Stdout, nil
}

// fromFile returns the text of the token file at path, after the checks of Load.
func fromFile(path, home string) (string, error) {
	if home == "" {
		return "", errors.New("the credential file needs the HOME variable, which is empty")
	}

	if rest, found := strings.CutPrefix(path, homeFolder); found {
		path = filepath.Join(home, rest)
	}

	root := filepath.Join(home, tokenFolder)

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("the credential file: %w", err)
	}

	if err = within(root, resolved); err != nil {
		return "", err
	}

	if err = owned(filepath.Dir(resolved), folderMode, true, os.Getuid()); err != nil {
		return "", err
	}

	if err = owned(resolved, fileMode, false, os.Getuid()); err != nil {
		return "", err
	}

	text, err := os.ReadFile(resolved) //nolint:gosec // the path is inside the credential folder, checked above

	return string(text), err //nolint:wrapcheck // the error of the read names the path
}

// within refuses a path that is not inside the folder root, after links are resolved.
func within(root, path string) error {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("the credential folder: %w", err)
	}

	if rel, relErr := filepath.Rel(real, path); relErr != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("the credential file %s is outside %s. Move it there", path, root)
	}

	return nil
}

// owned refuses a folder or a file that the user uid does not own, or that has another mode than want.
func owned(path string, want os.FileMode, folder bool, uid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("the credential path: %w", err)
	}

	stat, isStat := info.Sys().(*syscall.Stat_t) // the owner of a file is only in the system record

	switch {
	case info.IsDir() != folder:
		return fmt.Errorf("%s is not the kind of entry that a credential needs", path)
	case !isStat || int(stat.Uid) != uid:
		return fmt.Errorf("%s belongs to another user", path)
	case info.Mode().Perm() != want:
		return fmt.Errorf("%s has mode %o, want %o. Run chmod %o on it", path, info.Mode().Perm(), want, want)
	}

	return nil
}
