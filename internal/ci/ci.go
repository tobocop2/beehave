// Package ci holds the CI-neutral forms of the gate list: the files a reader gets, the steps it derives and the gate_map that holds them.
package ci

import "strings"

// failPrefix starts each line of the message of a refusal.
const failPrefix = "FAIL: "

// File is a CI workflow file: its path and its text.
type File struct {
	Path, Text string
}

// Step is one step of the gate list: a step name and the command that runs it.
type Step struct {
	Name, Command string
}

// Refused is a step or a job of a CI workflow file that the gate list leaves out, with the reason.
type Refused struct {
	Name, Reason string
}

// Derived is what a read of the CI workflow files gives: the steps of the gate list, and the steps and jobs that the gate list leaves out.
type Derived struct {
	Steps   []Step
	Refused []Refused
}

// Reader derives the gate steps of CI workflow files. Each CI system has one.
type Reader func(files []File) (Derived, error)

// Refusal is a read that cannot finish. Lines holds the lines the read found before it stopped.
type Refusal struct {
	Message string
	Lines   []string
}

func (r *Refusal) Error() string {
	return r.Message
}

// NewRefusal returns the refusal of the given problems, one FAIL line each, and the lines found before.
func NewRefusal(problems, lines []string) *Refusal {
	messages := make([]string, len(problems))
	for i, problem := range problems {
		messages[i] = failPrefix + problem
	}

	return &Refusal{Message: strings.Join(messages, "\n"), Lines: lines}
}
