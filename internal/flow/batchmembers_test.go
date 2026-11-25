package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

// TestMerge_TheCheckAndTheRunRefuseAMemberWithTheSameText runs each member through the entry point of the check and through the run.
// Each side must give the text in want, so a change of a rule of a member moves this table and no side can drift from the other.
func TestMerge_TheCheckAndTheRunRefuseAMemberWithTheSameText(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, members, want string }{
		{"item is a number", `[struct(item = 1, head = "h", closes = [])]`, "members[0]: `item` of a member of `prs` must be text that is not empty"},
		{"head is empty", `[struct(item = "i", head = "", closes = [])]`, "members[0]: `head` of a member of `prs` must be text that is not empty"},
		{"approved is text", `[struct(item = "i", head = "h", approved = "x", closes = [])]`, "members[0]: `approved` of a member of `prs` must be a bool, not string"},
		{"item is missing", `[struct(head = "h", closes = [])]`, "members[0]: a member of `prs` has no `item`"},
		{"the second member is wrong", `[struct(item = "i", head = "h", closes = []), struct(item = "j", head = "k", approved = 1, closes = [])]`, "members[1]: `approved` of a member of `prs` must be a bool, not int"},
		{"a member that is not a struct", `[struct(item = "i", head = "h", closes = []), 1]`, "members[1]: the member has the type int, not struct"},
		{"a list as a member", `[[struct(item = "i", head = "h", closes = [])]]`, "members[0]: the member has the type list, not struct"},
		{"a struct as the item", `[struct(item = struct(a = 1), head = "h", closes = [])]`, "members[0]: `item` of a member of `prs` must be text that is not empty"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			path := writeFlow(t, bodyFlow(`merge(members = `+tc.members+`, base = "main", repo = "o/r", name = "b")`))

			found, _ := workflow.Check(path)
			require.Len(t, found, 1)
			assert.Contains(t, found[0].Text, "merge: "+tc.want)

			_, err := rig.engine(t).RunFile(testItem, testProject, path, modeOff())
			require.ErrorContains(t, err, tc.want)
		})
	}
}
