package model

// Role names the forge identity that a credential belongs to.
type Role string

// RoleWriter is the identity that every push, comment and merge of the engine runs as.
const RoleWriter Role = "writer"

// Credential says where the engine reads the token of a role. It sets File or Command, never both.
type Credential struct {
	// File is a path inside ~/.config/beehave/credentials. A leading ~/ means the home folder.
	File string
	// Command is a program and its arguments, run once at start with no shell. Its trimmed output is the token.
	Command []string
}
