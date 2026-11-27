package credential

import (
	"errors"

	"github.com/tobocop2/beehest/internal/model"
)

var (
	// ErrCIWritable is the refusal of a writer token that the forge shows can change CI. ci_locked does not lift it.
	ErrCIWritable = errors.New("the writer token can change workflow files. Make a token with no Workflows permission " +
		"(docs/design/trust.md, section \"The CI lock\"), then start again. ci_locked does not lift this")
	// ErrCIUnstated is the refusal of a writer token whose CI permission the forge does not show, when the owner has not stated it.
	ErrCIUnstated = errors.New("beehest cannot read whether the writer token can change workflow files. If the token has no Workflows permission, " +
		"set ci_locked = True in its credential(...) block (docs/design/trust.md, section \"The CI lock\")")
)

// CIStart decides whether the engine may start with a writer token, from the answer of the forge and the statement ci_locked of the owner.
// Yes refuses whatever the statement says, no starts, and unknown starts only when the owner stated that the token cannot change CI.
func CIStart(answer model.CIWrite, locked bool) error {
	if answer == model.CIWriteYes {
		return ErrCIWritable
	}

	if answer == model.CIWriteUnknown && !locked {
		return ErrCIUnstated
	}

	return nil
}
