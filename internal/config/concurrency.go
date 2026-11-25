package config

// Concurrency is the mode of the cap of the agents queue.
type Concurrency string

const (
	// ConcurrencyStatic holds the queue at agents.max_concurrent.
	ConcurrencyStatic Concurrency = "static"
	// ConcurrencyAdaptive lets the queue cap move below agents.max_concurrent.
	ConcurrencyAdaptive Concurrency = "adaptive"
)

// Known reports whether the mode is one of the two that the engine runs.
func (c Concurrency) Known() bool {
	return c == ConcurrencyStatic || c == ConcurrencyAdaptive
}
