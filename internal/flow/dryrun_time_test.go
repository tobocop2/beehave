package flow

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// thirtySeconds is the time of each call in the tests that end at once. A run that waited for one call would take this long.
const thirtySeconds = "30s"

func TestDryRun_ASleepAWaitAndARecvEndAtOnce(t *testing.T) {
	t.Parallel()

	const text = "def main(item):\n    n = sleep(\"" + thirtySeconds + "\", name = \"nap\")\n    w = wait(name = \"ask\", question = \"Go?\", timeout = \"" + thirtySeconds + "\")\n    r = recv(topic = \"done\", timeout = \"" + thirtySeconds + "\", name = \"got\")\n" +
		"    if n.slept != 0 or not w.timed_out or not r.timed_out:\n        fail(\"WRONG RESULTS: %s %s %s\" % (n.slept, w.timed_out, r.timed_out))\n"

	var out bytes.Buffer

	started := time.Now()

	require.NoError(t, DryRun(t.Context(), dryFile(t, text), "12", map[string]string{}, &out), out.String())

	assert.Less(t, time.Since(started), 20*time.Second, "a call that waited its 30 seconds would take longer")
	assert.Contains(t, out.String(), "sleep:nap#1/start", "the marker step of the sleep stays")
	assert.Contains(t, out.String(), "sleep:nap#1:", "the sleep prints its step")
	assert.Contains(t, out.String(), "wait:deadline", "the record of the deadline of the wait stays")
	assert.NotContains(t, out.String(), "DBOS.sleep", "no real sleep ran")
	assert.NotContains(t, out.String(), "DBOS.recv", "no real receive ran")
}

func TestDryRun_AWaitWithNoDeadlineAndNoEntryIsRefused(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), dryFile(t, "def main(item):\n    wait(name = \"ask\", question = \"Go?\")\n"), "12", map[string]string{}, &out)

	require.Error(t, err)
	assert.Contains(t, out.String(), "wait:ask has no deadline")
	assert.Contains(t, out.String(), "no answer for it")
}

func TestDryRun_AFixtureAnswersAWaitWithNoDeadlineAndAWaitWithOne(t *testing.T) {
	t.Parallel()

	const text = "def main(item):\n    a = wait(name = \"ask\", question = \"Go?\")\n    b = wait(name = \"again\", question = \"Go?\", timeout = \"" + thirtySeconds + "\")\n" +
		"    if a.timed_out or a.answer != \"go\" or b.timed_out or b.answer != \"yes\":\n        fail(\"WRONG ANSWERS: %r %r\" % (a, b))\n"

	out, err := dryWithFixture(t, text, `{"wait:ask": {"answer": "go"}, "wait:again": [{"answer": "yes"}]}`)

	require.NoError(t, err, out)
}

func TestDryRun_AFixtureWaitThatTimesOutGivesNoAnswer(t *testing.T) {
	t.Parallel()

	const text = "def main(item):\n    a = wait(name = \"ask\", question = \"Go?\")\n    if not a.timed_out or a.answer != \"\":\n        fail(\"WRONG ANSWER: %r\" % a)\n"

	out, err := dryWithFixture(t, text, `{"wait:ask": {"timed_out": true}}`)

	require.NoError(t, err, out)
}

func TestDryRun_AFixtureGivesTheValueOfARecvAndEndsAnotherOneTimedOut(t *testing.T) {
	t.Parallel()

	const text = "def main(item):\n    a = recv(topic = \"done\", timeout = \"" + thirtySeconds + "\", name = \"got\")\n    b = recv(topic = \"done\", timeout = \"" + thirtySeconds + "\", name = \"late\")\n" +
		"    if a.timed_out or a.value != \"x\" or not b.timed_out:\n        fail(\"WRONG VALUES: %r %r\" % (a, b))\n"

	out, err := dryWithFixture(t, text, `{"recv:got": {"value": "x"}}`)

	require.NoError(t, err, out)
}

func TestDryRun_AWaitEntryNoCallUsedIsRefusedAtTheEndOfACleanRun(t *testing.T) {
	t.Parallel()

	out, err := dryWithFixture(t, "def main(item):\n    wait(name = \"ask\", question = \"Go?\", timeout = \""+thirtySeconds+"\")\n", `{"wait:ask": [{"answer": "go"}, {"answer": "go"}]}`)

	require.Error(t, err)
	assert.Contains(t, out, "wait:ask")
	assert.Contains(t, out, "result 2 was never made")
}

func TestDryRun_AWaitPastTheEndOfItsListIsRefused(t *testing.T) {
	t.Parallel()

	const text = "def main(item):\n    wait(name = \"ask\", question = \"Go?\", timeout = \"" + thirtySeconds + "\")\n    wait(name = \"ask\", question = \"Go?\", timeout = \"" + thirtySeconds + "\")\n"

	out, err := dryWithFixture(t, text, `{"wait:ask": [{"answer": "go"}]}`)

	require.Error(t, err)
	assert.Contains(t, out, "wait:ask call 2")
}

func TestDryRun_AWaitAndARecvWithNoNameTakeTheKeyOfTheBuiltinAndARecvPastTheEndOfItsListIsRefused(t *testing.T) {
	t.Parallel()

	const text = "def main(item):\n    a = wait(question = \"Go?\")\n    b = recv(topic = \"done\", timeout = \"" + thirtySeconds + "\")\n" +
		"    if a.answer != \"go\" or b.value != \"x\":\n        fail(\"WRONG: %r %r\" % (a, b))\n    recv(topic = \"done\", timeout = \"" + thirtySeconds + "\")\n"

	out, err := dryWithFixture(t, text, `{"wait": {"answer": "go"}, "recv": [{"value": "x"}]}`)

	require.Error(t, err)
	assert.Contains(t, out, "recv call 2")
	assert.NotContains(t, out, "WRONG")
}

func TestDryRun_AGateWithNoNameTakesTheKeyOfTheBuiltinAndIsRedWhenTheFixtureSaysSo(t *testing.T) {
	t.Parallel()

	out, err := dryWithFixture(t, "def main(item):\n    g = gate()\n    if g.passed:\n        fail(\"PASSED\")\n", `{"gate": [{"passed": false}]}`)

	require.NoError(t, err, out)
}
