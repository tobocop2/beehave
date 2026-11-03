package flow

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDryRun_ATimedWaitWithNoAnswerEndsOnItsDeadline(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"plain":     `wait(name = "ask", question = "Go?", timeout = "1s")`,
		"reminders": `wait(name = "ask", question = "Go?", to = ["ana"], timeout = "1s", remind = [0.5])`,
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, stop := context.WithTimeout(t.Context(), eventTimeout)
			defer stop()

			file := dryFile(t, "def main(item):\n    r = "+call+"\n    if not r.timed_out:\n        fail(\"NOT TIMED OUT\")\n")

			var out bytes.Buffer

			started := time.Now()

			require.NoError(t, DryRun(ctx, file, "12", map[string]string{}, &out), out.String())
			require.Less(t, time.Since(started), eventTimeout, "the wait ends on its deadline and not at the end of the test")
		})
	}
}
