package model

// FindingKind names a rule a lane command broke.
type FindingKind string

// The rules a lane command can break.
const (
	FindingBulkStaging  FindingKind = "bulk-staging"
	FindingRemoteDelete FindingKind = "remote-delete"
	FindingDeleteText   FindingKind = "delete-text"
	FindingLocalDelete  FindingKind = "local-delete"
	FindingUnparsed     FindingKind = "unparsed"
	FindingOpaque       FindingKind = "opaque-command"
)

// Finding is one forbidden command found in a shell command, with the text that matched.
type Finding struct {
	Kind FindingKind
	Text string
}
