package main

import "github.com/rogpeppe/go-internal/testscript"

// reviewLoopFakes are the fakes of the review loop scripts: the source in testdata and the name in the folder fake.
var reviewLoopFakes = map[string]string{"review-loop-claude.sh": "claude"}

// cmdReviewLoopFakes is "reviewfakes": it writes the fake claude of the review loop scripts to the folder fake in the work folder.
func cmdReviewLoopFakes(ts *testscript.TestScript, _ bool, args []string) {
	if len(args) != 0 {
		ts.Fatalf("usage: reviewfakes")
	}

	writeFakes(ts, "reviewfakes", reviewLoopFakes)
}
