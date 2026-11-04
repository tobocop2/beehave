package proc

// WithAbs returns r with a function that makes a path absolute, which tests supply.
func WithAbs(r ExecRunner, abs func(string) (string, error)) ExecRunner {
	r.abs = abs

	return r
}
