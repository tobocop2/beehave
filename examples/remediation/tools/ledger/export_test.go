package ledger

// WaitAtMost sets how many milliseconds the ledger waits for a lock, so a test of a locked file does not wait 30 seconds.
func WaitAtMost(l *Ledger, ms int) { l.busyMs = ms }
