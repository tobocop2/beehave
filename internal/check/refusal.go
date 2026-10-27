package check

// Refusal is a check that cannot run. Lines holds the lines the check found before it stopped.
type Refusal struct {
	Message string
	Lines   []string
}

func (r *Refusal) Error() string {
	return r.Message
}
