package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	"github.com/tobocop2/beehest/internal/lockfile"
)

// keyLockRetry is the wait between two tries to take the lock of a key.
const keyLockRetry = 25 * time.Millisecond

// keyLockBytes is the number of bytes of the SHA-256 hash of a key that name its lock file.
const keyLockBytes = 16

// LockKey takes the file lock of one key in the folder dir and returns the function that frees it. It waits until the lock is free or ctx ends.
// A caller holds it around the lookup and the create of an item whose key every run shares, so two writers on this host file one item.
// The lock file is item-<hash of the key>.lock, one file for each key.
func LockKey(ctx context.Context, dir, key string) (func() error, error) {
	sum := sha256.Sum256([]byte(key))
	lock := lockfile.Open(filepath.Join(dir, "item-"+hex.EncodeToString(sum[:keyLockBytes])+".lock"))

	if _, err := lock.TryLockContext(ctx, keyLockRetry); err != nil {
		return nil, fmt.Errorf("take the lock of a shared item key in %s: %w", dir, err)
	}

	return lock.Unlock, nil
}
