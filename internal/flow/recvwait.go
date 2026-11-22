package flow

import (
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// receivingOf reads the wait of the recv call that an open run is in: the deadline step of the call is the last record that is not a record of the library.
// A recv that took a message from the buffer records no deadline, and a recv that ended records its own step after the deadline.
func receivingOf(records []dbos.StepInfo) (recvWindow, bool) {
	for i := len(records) - 1; i >= 0; i-- {
		name := records[i].StepName
		if strings.HasPrefix(name, "DBOS.") {
			continue
		}

		if kind, _ := kindOfStep(name); kind != recvKind || !strings.HasSuffix(name, deadlineSuffix) {
			return recvWindow{}, false
		}

		var window recvWindow

		return window, decodeThen(records[i].Output, func(read recvWindow) { window = read })
	}

	return recvWindow{}, false
}

// callOf is the name of the call that a record belongs to: the name of a deadline step, which a call records before it waits, is the name of the call.
func callOf(name string) string {
	return strings.TrimSuffix(name, deadlineSuffix)
}
