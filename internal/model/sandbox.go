package model

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Sandbox is the sandbox setting of one agent step as the run records it. A step with no sandbox records no value (nil), and a record
// without the value replays as a step with no sandbox. Network lists the domains that the shell may reach. AllowWrite lists the extra paths that it may write.
type Sandbox struct {
	Network    []string `json:"network,omitempty"`
	AllowWrite []string `json:"allow_write,omitempty"`
}

// SandboxMode is the sandbox default of a project: whether a step that sets no sandbox runs in the sandbox.
type SandboxMode string

// The values of the sandbox key of a project file.
const (
	SandboxOn  SandboxMode = "on"
	SandboxOff SandboxMode = "off"
)

// ErrUnknownSandboxMode is returned when text names no sandbox mode.
var ErrUnknownSandboxMode = errors.New("unknown sandbox mode")

// ParseSandboxMode returns the sandbox mode that text names.
func ParseSandboxMode(text string) (SandboxMode, error) {
	for _, mode := range []SandboxMode{SandboxOn, SandboxOff} {
		if string(mode) == text {
			return mode, nil
		}
	}

	return "", fmt.Errorf("%w %q: use one of on, off", ErrUnknownSandboxMode, text)
}

// SandboxByDefault is true when the project file says sandbox = "on". A project that says nothing runs its steps with no sandbox.
func (p Project) SandboxByDefault() bool { return p.Sandbox == SandboxOn }

// domainName is a host name for the network list: labels of letters, digits and hyphens, with an optional leading "*." for all subdomains.
var domainName = regexp.MustCompile(`^(\*\.)?[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)

// CheckNetwork refuses a network entry that is not a domain name. A URL, a path, a port or a bare "*" would name more than one host or none.
func CheckNetwork(domains []string) error {
	for _, domain := range domains {
		if !domainName.MatchString(domain) {
			return fmt.Errorf("sandbox network lists %q, which is not a domain name. Write a domain such as api.example.org, with no scheme, port or path", domain)
		}
	}

	return nil
}

// EnvPrefix starts the name of every file that the sandbox treats as a credential file, at any depth of the work folder.
const EnvPrefix = ".env"

// CredentialPatterns lists the credential paths of the engine, relative to the home folder. A step cannot remove an entry, and the project file can add to the list.
// An entry with * is a glob that the plan expands to the paths that exist. This list is the one source for the plan and for the check of the path lists.
func CredentialPatterns() []string {
	return []string{
		".ssh", ".aws", ".config/gh", ".config/gcloud", ".azure", ".gnupg", ".netrc", ".npmrc", ".pypirc", ".git-credentials", ".docker", ".kube",
		".cargo/credentials*", ".terraform.d", ".config/op", ".config/beehave", ".claude*", "Library/Keychains", ".local/share/keyrings",
	}
}

// ClosedNames lists the names that the sandbox of an agent keeps closed to writes at the top of the work folder: the git folder, the settings folder
// of the agent CLI and the two usual hook folders. A write there runs later, outside the sandbox, as the owner. An allow_write path with one of these
// names at any depth is refused.
func ClosedNames() []string {
	return []string{".git", ".claude", ".husky", ".githooks"}
}

// parts splits a path into its names, without empty names, "." and the home mark.
func parts(path string) []string {
	var out []string

	for _, name := range strings.Split(path, "/") {
		if name != "" && name != "." && name != "~" {
			out = append(out, name)
		}
	}

	return out
}

// ClosedPart returns the name in path that is a closed name or starts with the credential file prefix, or "" when there is none.
func ClosedPart(path string) string {
	for _, name := range parts(path) {
		if slices.ContainsFunc(ClosedNames(), func(closed string) bool { return strings.EqualFold(closed, name) }) || isEnvName(name) {
			return name
		}
	}

	return ""
}

// isEnvName reports whether a name starts with the credential file prefix, in any case: a case-insensitive disk reads .ENV as .env.
func isEnvName(name string) bool {
	return len(name) >= len(EnvPrefix) && strings.EqualFold(name[:len(EnvPrefix)], EnvPrefix)
}

// EnvPart returns the name in path that starts with the credential file prefix, or "" when there is none.
func EnvPart(path string) string {
	for _, name := range parts(path) {
		if isEnvName(name) {
			return name
		}
	}

	return ""
}

// HomeCredential returns the credential path (written ~/...) that a path under the home folder is, is inside, or holds, or "" when there is none.
// A path that is not written ~ or ~/... returns "". The home folder itself holds every credential path.
func HomeCredential(path string) string {
	if !IsHomePath(path) {
		return ""
	}

	rest := parts(path)
	if len(rest) == 0 {
		return "~/"
	}

	for _, pattern := range CredentialPatterns() {
		if sameStart(strings.Split(pattern, "/"), rest) {
			return "~/" + pattern
		}
	}

	return ""
}

// sameStart reports whether the names of two paths agree, without case, as far as the shorter one goes, where a name of the pattern may hold a glob.
func sameStart(pattern, names []string) bool {
	for i := range min(len(pattern), len(names)) {
		if ok, err := filepath.Match(strings.ToLower(pattern[i]), strings.ToLower(names[i])); err != nil || !ok {
			return false
		}
	}

	return true
}

// CheckAllowWrite refuses an allow_write entry that is empty, that holds a parent reference, which could leave the folder it starts from, or that
// reopens what the sandbox keeps closed: a credential path under the home folder, a .env file, .git, .claude, .husky or .githooks.
func CheckAllowWrite(paths []string) error {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return errors.New("sandbox allow_write holds an empty path. Give a path")
		}

		if strings.Contains(path, "..") {
			return fmt.Errorf("sandbox allow_write lists %q, which holds \"..\". Write the path without it", path)
		}

		if name := ClosedPart(path); name != "" {
			return fmt.Errorf("sandbox allow_write lists %q, which reaches %s. The sandbox keeps that name closed. Remove the path", path, name)
		}

		if secret := HomeCredential(path); secret != "" {
			return fmt.Errorf("sandbox allow_write lists %q, which overlaps the credential path %s. Remove the path", path, secret)
		}
	}

	return nil
}

// CheckSandboxRead refuses a sandbox_read entry that opens a credential: the whole file system, the home folder, a credential path under the home
// folder or a path that holds one, and a .env file. Plan time checks the absolute paths against the real home folder.
func CheckSandboxRead(paths []string) error {
	for _, path := range paths {
		switch {
		case len(parts(path)) == 0:
			return fmt.Errorf("sandbox_read lists %q, which is the file system or the home folder. List the folders that the shell needs", path)
		case HomeCredential(path) != "":
			return fmt.Errorf("sandbox_read lists %q, which overlaps the credential path %s. Remove the path", path, HomeCredential(path))
		case EnvPart(path) != "":
			return fmt.Errorf("sandbox_read lists %q, which reaches %s. The sandbox keeps that name closed. Remove the path", path, EnvPart(path))
		}
	}

	return nil
}
