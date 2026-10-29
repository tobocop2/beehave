package merge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/tobocop2/obee/internal/lockfile"
)

// EditBody reads the body of a pull request, applies edit and writes the result, all under an exclusive lock on integration-<pr>.lock in dir.
// Two writers of one body never lose a row, because each one reads the body after the other wrote it. The lock is released on every exit.
func EditBody(ctx context.Context, dir string, forge Body, pr int, edit func(body string) string) (err error) {
	lock := lockfile.Open(filepath.Join(dir, fmt.Sprintf("integration-%d.lock", pr)))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock the body of #%d: %w", pr, err)
	}

	defer func() { err = errors.Join(err, lock.Unlock()) }()

	body, err := forge.PRBody(ctx, pr)
	if err != nil {
		return fmt.Errorf("read the body of #%d: %w", pr, err)
	}

	if err := forge.SetBody(ctx, pr, edit(body)); err != nil {
		return fmt.Errorf("write the body of #%d: %w", pr, err)
	}

	return nil
}
