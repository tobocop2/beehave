package model

import "time"

// Claim timing the config supplies. A hold expires when its holder was last seen longer ago than the lease.
const (
	DefaultClaimLease  = 2 * time.Hour
	DefaultClaimSettle = 2 * time.Second
	MinClaimLease      = time.Second
)
