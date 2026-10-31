package proc

// WithCount returns r with a count of the processes of the user that tests supply.
func WithCount(r ExecRunner, count func() (int, error)) ExecRunner {
	r.count = count

	return r
}

// WithAbs returns r with a function that makes a path absolute, which tests supply.
func WithAbs(r ExecRunner, abs func(string) (string, error)) ExecRunner {
	r.abs = abs

	return r
}
