package model

// FindingKind names a rule a shell command broke.
type FindingKind string

// The rules a shell command can break.
const (
	FindingBulkStaging  FindingKind = "bulk-staging"
	FindingRemoteDelete FindingKind = "remote-delete"
	FindingDeleteText   FindingKind = "delete-text"
	FindingLocalDelete  FindingKind = "local-delete"
	FindingUnparsed     FindingKind = "unparsed"
	FindingOpaque       FindingKind = "opaque-command"
	FindingGitAuthor    FindingKind = "git-author"
	FindingGitBypass    FindingKind = "git-bypass"
	FindingEnvOverride  FindingKind = "env-override"
	FindingConfigRead   FindingKind = "config-read"
	FindingGitWrite     FindingKind = "git-write"
	FindingProtected    FindingKind = "protected-path"
	FindingZshExpansion FindingKind = "zsh-expansion"
)

// FindingKinds returns the thirteen kinds, sorted by name. A workflow lists them in `forbid_commands`.
func FindingKinds() []FindingKind {
	return []FindingKind{
		FindingBulkStaging, FindingConfigRead, FindingDeleteText, FindingEnvOverride, FindingGitAuthor,
		FindingGitBypass, FindingGitWrite, FindingLocalDelete, FindingOpaque, FindingProtected, FindingRemoteDelete, FindingUnparsed, FindingZshExpansion,
	}
}

// NeedsPaths reports whether a kind judges commands against a list of paths that the step must give in `protect_paths`.
func (k FindingKind) NeedsPaths() bool { return k == FindingProtected }

// FindingKindNames returns the names of the kinds, in the order of FindingKinds.
func FindingKindNames() []string {
	kinds := FindingKinds()
	names := make([]string, 0, len(kinds))

	for _, kind := range kinds {
		names = append(names, string(kind))
	}

	return names
}

// Finding is one forbidden command found in a shell command, with the text that matched.
type Finding struct {
	Kind FindingKind
	Text string
}
