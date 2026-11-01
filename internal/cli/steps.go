package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/flow"
)

// printSteps writes the steps of one run: one line each by default, or one JSON object.
func printSteps(stdout io.Writer, client Steering, id string, asJSON bool) error {
	view, err := client.Steps(id)
	if err != nil {
		return err
	}

	if asJSON {
		return json.NewEncoder(stdout).Encode(view)
	}

	for _, step := range view.Steps {
		// A failed write to stdout has no recovery.
		_, _ = fmt.Fprintln(stdout, stepLine(step))
	}

	_, _ = fmt.Fprintln(stdout, runLine(view))

	return nil
}

// runLine is the last line of the view: the word run, the state of the run and, for a run that failed, the error that the store keeps for it.
// A run that fails outside a step has no failed step, so this line holds the cause.
func runLine(view flow.RunSteps) string {
	columns := []string{"run", string(view.State)}

	if view.Error != "" {
		columns = append(columns, "error="+strconv.Quote(oneLine(view.Error)))
	}

	return strings.Join(columns, "\t")
}

// stepLine describes one step: its name, start time, duration and outcome, then the fields that decide the flow. A field the step lacks is left out.
func stepLine(step flow.StepLine) string {
	columns := []string{step.Name, step.Started.Format(time.RFC3339), spanOf(step.Seconds), step.State}

	if fields := stepFields(step); len(fields) > 0 {
		columns = append(columns, strings.Join(fields, " "))
	}

	return strings.Join(columns, "\t")
}

// stepFields lists the fields of a step as `name=value`, in a fixed order.
func stepFields(step flow.StepLine) []string {
	var fields []string

	if step.ExitCode != nil {
		fields = append(fields, "exit_code="+strconv.Itoa(*step.ExitCode))
	}

	fields = appendText(fields, "stop", step.Stop)

	fields = appendText(fields, "verdict", step.Verdict)
	fields = appendText(fields, "head", step.Head)

	if step.Passed != nil {
		fields = append(fields, "gate="+gateWord(*step.Passed), gateSteps(step.Gate))
	}

	return appendProblems(fields, step)
}

// appendProblems adds the word for an output that could not be read, and the error of the step.
func appendProblems(fields []string, step flow.StepLine) []string {
	if step.OutputUnreadable {
		fields = append(fields, "output_unreadable")
	}

	if step.Error != "" {
		fields = append(fields, "error="+strconv.Quote(oneLine(step.Error)))
	}

	return fields
}

// appendText adds `name=value` when the value is not empty.
func appendText(fields []string, name, value string) []string {
	if value == "" {
		return fields
	}

	return append(fields, name+"="+value)
}

// gateWord is pass or fail, the word that the gate steps use.
func gateWord(passed bool) string {
	if passed {
		return "pass"
	}

	return "fail"
}

// gateSteps lists each gate step with its status: `command:pass,tests:fail`.
func gateSteps(steps []flow.GateStep) string {
	parts := make([]string, 0, len(steps))
	for _, step := range steps {
		parts = append(parts, step.Name+":"+step.Status)
	}

	return "steps=" + strings.Join(parts, ",")
}

// spanOf writes a duration in whole seconds, or in milliseconds when it is under one second.
func spanOf(seconds float64) string {
	span := time.Duration(seconds * float64(time.Second))
	if span >= time.Second {
		return span.Round(time.Second).String()
	}

	return span.Round(time.Millisecond).String()
}
