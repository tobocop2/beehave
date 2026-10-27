package shell_test

import (
	"strings"
	"testing"

	"github.com/tobocop2/obee/internal/check/shell"
)

const (
	jobDir   = "/job"
	fixedMsg = "the job prints a fixed exit status"
)

var fullJob = strings.Join([]string{
	"#!/bin/bash",
	`cargo fmt --all -- --check; echo "STEP_RESULT name=fmt rc=$?"`,
	`QUALITY_BASE=origin/main task check:quality; echo "STEP_RESULT name=quality rc=$?"`,
	`~/tools/gen-1.2.3/bin/gen verify; echo "STEP_RESULT name=gen-verify rc=$?"`,
	"cargo hack clippy \\",
	`  --each-feature; echo "STEP_RESULT name=each-feature rc=$?"`,
	`mkdocs build --strict; echo "STEP_RESULT name=docs rc=$?"`,
	`echo "DONE-x rc=$reds gate=full"`,
	"",
}, "\n")

// jobWith returns the full job without each line that holds one of the drops, then the added lines.
func jobWith(drops []string, add ...string) string {
	var kept []string

	for _, line := range strings.Split(strings.TrimSuffix(fullJob, "\n"), "\n") {
		hit := false

		for _, drop := range drops {
			hit = hit || strings.Contains(line, drop)
		}

		if !hit {
			kept = append(kept, line)
		}
	}

	return strings.Join(append(kept, add...), "\n") + "\n"
}

// markerResult is what the exit marker rules say about a job.
type markerResult struct{ Lines []string }

// OK reports whether no line is a defect.
func (r markerResult) OK() bool {
	for _, line := range r.Lines {
		if strings.HasPrefix(line, shell.FailPrefix) {
			return false
		}
	}

	return true
}

// gateOf reads the job with the exit marker rules and the files it sources.
func gateOf(t *testing.T, script string, files map[string]string) markerResult {
	t.Helper()

	read := func(path string) (string, bool) {
		text, ok := files[strings.TrimPrefix(path, jobDir+"/")]

		return text, ok
	}
	units, _ := shell.ParseUnits(script)

	return markerResult{Lines: shell.FixedStatusLines(units, jobDir, read)}
}
