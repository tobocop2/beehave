package remedy

import (
	"context"
	"errors"
	"strings"
)

// errNoOrigin says that the working folder has no origin remote that names a repository.
var errNoOrigin = errors.New("cannot read the origin remote of the working folder")

// originRepo is the owner and name of the repository that the origin remote of the working folder names.
func originRepo(ctx context.Context, deps Deps) (string, error) {
	remote := deps.Git.Run(ctx, []string{"remote", "get-url", "origin"})
	if remote.ExitCode != 0 {
		return "", errNoOrigin
	}

	repo, ok := repoOf(remote.Stdout)
	if !ok {
		return "", errNoOrigin
	}

	return repo, nil
}

// repoOf is the owner and name at the end of a remote address: https://host/owner/name.git, git@host:owner/name and ssh://git@host/owner/name all give owner/name.
func repoOf(address string) (string, bool) {
	trimmed := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(address), "/"), ".git")
	parts := strings.FieldsFunc(trimmed, func(r rune) bool { return r == '/' || r == ':' })

	// A remote address has a host, an owner and a name.
	const hostOwnerName = 3
	if len(parts) < hostOwnerName {
		return "", false
	}

	return strings.Join(parts[len(parts)-2:], "/"), true
}
