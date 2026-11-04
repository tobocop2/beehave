package forge

import (
	"context"
	"fmt"
	"strings"

	"github.com/tobocop2/beehave/internal/store"
)

// PostOnce comments on a pull request once for each key. A step can run twice after a crash, so it reads the comments first and posts nothing when a comment
// already holds the marker line of the key. The marker is the line that the trackers write, so the same key means the same note on every surface.
// A failed read is an error and posts nothing.
func PostOnce(ctx context.Context, target Activity, pr int, key, text string) error {
	rows, err := target.Comments(ctx, pr)
	if err != nil {
		return fmt.Errorf("read the comments of pull request %d: %w", pr, err)
	}

	for _, row := range rows {
		if store.HasMarker(strings.ReplaceAll(row.Body, "\r\n", "\n"), key) {
			return nil
		}
	}

	return target.PostComment(ctx, pr, store.WithMarker(text, key))
}
