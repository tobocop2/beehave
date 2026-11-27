package model

// Role names the forge identity that a credential belongs to.
type Role string

// RoleWriter is the identity that every push, comment and merge of the engine runs as.
const RoleWriter Role = "writer"

// HasWriter reports whether the project declares a writer credential.
func (p Project) HasWriter() bool {
	_, declared := p.Credentials[RoleWriter]

	return declared
}

// Credential says where the engine reads the token of a role. It sets File or Command, never both.
type Credential struct {
	// File is a path inside ~/.config/beehest/credentials. A leading ~/ means the home folder.
	File string
	// Command is a program and its arguments, run once at start with no shell. Its trimmed output is the token.
	Command []string
	// CILocked is the statement of the owner that the token cannot change CI files, for a token whose permissions the forge does not show.
	CILocked bool
}

// CIWrite is the answer of a forge to whether a token can change the CI files of the repository.
type CIWrite uint8

// The closed set of answers. The zero value is CIWriteUnknown, so an answer that was never read is never CIWriteNo.
const (
	CIWriteUnknown CIWrite = iota
	CIWriteNo
	CIWriteYes
)
