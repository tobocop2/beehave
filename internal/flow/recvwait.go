package flow

import (
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// receivingOf reads the wait of the recv call that an open run is in.
func receivingOf(records []dbos.StepInfo) (recvWindow, bool) {
	var window recvWindow

	ok := decodeThen(waitOutput(records, recvKind), func(read recvWindow) { window = read })

	return window, ok
}

// streamingOf reads the wait of the stream call that an open run is in.
func streamingOf(records []dbos.StepInfo) (streamWindow, bool) {
	var window streamWindow

	ok := decodeThen(waitOutput(records, streamKind), func(read streamWindow) { window = read })

	return window, ok
}

// waitOutput gives the output of the deadline step of the call of one kind that an open run is in: the deadline step of the call is the last record that is not a record of the library.
// A call that took its answer from the buffer records no deadline, and a call that ended records its own step after the deadline. Then the output is nil.
func waitOutput(records []dbos.StepInfo, want string) any {
	for i := len(records) - 1; i >= 0; i-- {
		name := records[i].StepName
		if strings.HasPrefix(name, "DBOS.") {
			continue
		}

		if kind, _ := kindOfStep(name); kind != want || !strings.HasSuffix(name, deadlineSuffix) {
			return nil
		}

		return records[i].Output
	}

	return nil
}

// callOf is the name of the call that a record belongs to: the name of a deadline step, which a call records before it waits, is the name of the call.
func callOf(name string) string {
	return strings.TrimSuffix(name, deadlineSuffix)
}
