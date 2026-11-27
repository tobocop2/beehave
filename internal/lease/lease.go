// Package lease builds the claims over the rows of a claim store.
// It imports no backend. The binary that owns the backend opens the rows and passes them to Open.
package lease

import (
	"io"
	"time"

	"github.com/tobocop2/beehest/internal/claim"
)

// Open builds the claims over the rows. The claims say nothing on their own: the caller shows what it wants to show.
func Open(rows claim.Rows) (*claim.Claims, error) {
	return claim.New(claim.Options{Rows: rows, Clock: time.Now, Out: io.Discard})
}
