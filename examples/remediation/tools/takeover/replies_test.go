package takeover_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/examples/remediation/tools/takeover"
)

func TestReplies_AnOrdinaryReplyRefusesAndPrintsEveryReplyBlock(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9, notice: 5, reply: "still looking at this", replyAge: 2})
	s.pr["reviews"] = []obj{
		{"author": login("me"), "state": "CHANGES_REQUESTED", "submittedAt": "2020-01-01T00:00:00Z", "body": "x"},
		{"author": login("them"), "state": "COMMENTED", "submittedAt": ago(2), "body": "hm"},
	}
	s.rows = comments("them", ago(2))
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeRule, got.Code)
	text := all(got)
	assert.Contains(t, text, "REFUSE: 3 replies landed on #7 and none used KEEP or RELEASE. Read the intent and decide.")
	assert.Contains(t, text, "--- replies after "+ago(5)+" ---")
	assert.Contains(t, text, "(comment):\nstill looking at this\n")
	assert.Contains(t, text, "(review COMMENTED):\nhm\n")
	assert.Contains(t, text, "(review comment on a.go):\nline\n")
	assert.Contains(t, text, "--- end of replies ---")
	assert.Equal(t, "PROCEED only if the intent plainly allows it, by re-running: --take --reply-read", last(got))
	assert.Zero(t, s.count("free"))
}

func TestReplies_AReadReplyLetsTheTakeGoOnWithANote(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5, reply: "still looking at this", replyAge: 2})
	got := s.run(t, takeover.ModeTake, readReplies)

	assert.Equal(t, 0, got.Code)
	assert.Contains(t, got.Lines, "note: 1 replies landed after the notice, were read, and did not forbid this take.")
	assert.Equal(t, 1, s.count("free"))
}

func TestReplies_AReplyWithoutABodyIsRead(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5})
	s.pr["comments"] = append(s.comments(), obj{"author": login("them"), "createdAt": ago(0.5), "body": nil})

	assert.Equal(t, takeover.CodeRule, s.run(t, takeover.ModeTake).Code)
}
