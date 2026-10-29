package model

import "time"

// Window is one quota window: the percent used, and when it resets. A zero ResetsAt means the server gave no time.
type Window struct {
	Percent  float64
	ResetsAt time.Time
}

// Usage holds the two windows the runner pool decides on.
type Usage struct {
	FiveHour Window
	SevenDay Window
}

// Reading is the usage of one runner, or the reason it could not be read. Build one with Known or Unknown.
type Reading struct {
	usage  Usage
	reason string
	known  bool

	needsLogin bool
}

// Known returns a reading that holds usage.
func Known(usage Usage) Reading {
	return Reading{usage: usage, known: true}
}

// Unknown returns a reading that holds the reason the usage could not be read.
func Unknown(reason string) Reading {
	return Reading{reason: reason}
}

// UnknownLogin returns an unknown reading for a runner that has no usable login. The reason says how to log in. Waiting does not fix it.
func UnknownLogin(reason string) Reading {
	return Reading{reason: reason, needsLogin: true}
}

// NeedsLogin says whether the usage is unknown because the runner has no usable login.
func (r Reading) NeedsLogin() bool {
	return r.needsLogin
}

// Usage returns the usage and whether the reading holds one.
func (r Reading) Usage() (Usage, bool) {
	return r.usage, r.known
}

// Reason says why the usage is unknown. A reading that holds usage has no reason.
func (r Reading) Reason() string {
	if r.known {
		return ""
	}

	if r.reason == "" {
		return "no reading"
	}

	return r.reason
}
