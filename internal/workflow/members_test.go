package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memberCases are lists of members for the prs keyword. A want of "" is a list that the check accepts.
var memberCases = []struct {
	name string
	prs  string
	want string
}{
	{"item is a number", `[struct(item = 1, head = "h")]`, "prs[0]: `item` of a member of `prs` must be text that is not empty"},
	{"head is a bool", `[struct(item = "i", head = True)]`, "prs[0]: `head` of a member of `prs` must be text that is not empty"},
	{"approved is text", `[struct(item = "i", head = "h", approved = "x")]`, "prs[0]: `approved` of a member of `prs` must be a bool, not string"},
	{"own_draft is text", `[struct(item = "i", head = "h", own_draft = "7")]`, "prs[0]: own_draft must be the number of a pull request"},
	{"own_draft is zero", `[struct(item = "i", head = "h", own_draft = 0)]`, "prs[0]: own_draft must be the number of a pull request"},
	{"own_draft is negative", `[struct(item = "i", head = "h", own_draft = -5)]`, "prs[0]: own_draft must be the number of a pull request"},
	{"a computed own_draft does not hide a wrong head", `[struct(item = "i", head = "", own_draft = name)]`, "prs[0]: `head` of a member of `prs` must be text that is not empty"},
	{"all three fields are wrong", `[struct(item = 1, head = True, approved = "x")]`, "prs[0]: `item` of a member of `prs` must be text that is not empty"},
	{"the second member is wrong", `[struct(item = "i", head = "h"), struct(item = "j", head = "k", approved = 1)]`, "prs[1]: `approved` of a member of `prs` must be a bool, not int"},
	{"a computed member keeps the index of the next", `[name, struct(item = "j", head = "k", approved = 1)]`, "prs[1]: `approved` of a member of `prs` must be a bool, not int"},
	{"a tuple of members", `(struct(item = 1, head = "h"),)`, "prs[0]: `item` of a member of `prs` must be text that is not empty"},
	{"a parenthesized member", `[(struct(item = 1, head = "h"))]`, "prs[0]: `item` of a member of `prs` must be text that is not empty"},
	{"a parenthesized list", `([struct(item = 1, head = "h")])`, "prs[0]: `item` of a member of `prs` must be text that is not empty"},
	{"item is missing", `[struct(head = "h")]`, "prs[0]: a member of `prs` has no `item`"},
	{"a computed item does not hide a wrong approved", `[struct(item = name, head = "h", approved = "x")]`, "prs[0]: `approved` of a member of `prs` must be a bool, not string"},
	{"a computed approved does not hide a wrong head", `[struct(item = "i", head = "", approved = name)]`, "prs[0]: `head` of a member of `prs` must be text that is not empty"},
	{"a literal member that is not a struct", `[struct(item = "i", head = "h"), 1]`, "prs[1]: the member has the type int, not struct"},
	{"a dict member", `[{"item": "i", "head": "h"}]`, "prs[0]: the member has the type dict, not struct"},
	{"a dict member with a computed value", `[{"item": name}]`, "prs[0]: the member has the type dict, not struct"},
	{"a list member that holds a struct", `[[struct(item = 1, head = "h")]]`, "prs[0]: the member has the type list, not struct"},
	{"a tuple member that holds a computed value", `[(name,)]`, "prs[0]: the member has the type tuple, not struct"},
	{"a struct as the item", `[struct(item = struct(a = 1), head = "h")]`, "prs[0]: `item` of a member of `prs` must be text that is not empty"},
	{"a list with a computed member as the item", `[struct(item = [name], head = "h")]`, "prs[0]: `item` of a member of `prs` must be text that is not empty"},
	{"a valid member", `[struct(item = "i", head = "h", approved = True)]`, ""},
	{"a member with the number of its own draft", `[struct(item = "i", head = "h", approved = True, own_draft = 7)]`, ""},
	{"an own_draft that only the run knows is left to the run", `[struct(item = "i", head = "h", own_draft = name)]`, ""},
	{"every field computed", `[struct(item = name, head = name, approved = name)]`, ""},
	{"a field that is a call", `[struct(item = str(name), head = "h")]`, ""},
	{"a member that is computed", `[name]`, ""},
	{"a member that is a call of another function", `[str(name)]`, ""},
	{"a member that is a struct with a positional argument", `[struct(name, item = 1)]`, ""},
	{"a member that is a struct with a keyword dict", `[struct(**name)]`, ""},
	{"a member that is a call of another name", `[dict(item = 1, head = "h")]`, ""},
	{"none is no member", `None`, "`prs` is empty"},
}

func TestCheck_ReadsTheFieldsOfAStructMemberOfPrs(t *testing.T) {
	t.Parallel()

	for _, tc := range memberCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    name = item.id\n    merge(prs = "+tc.prs+", base = \"main\", repo = \"o/r\", name = \"n\")\n")

			if tc.want == "" {
				assert.Empty(t, got)

				return
			}

			require.Len(t, got, 1)
			assert.Contains(t, got[0], "merge: "+tc.want)
		})
	}
}

func TestCheck_ReadsAStructThatTheFileDefinesAsItsOwnName(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def struct(**fields):\n    return fields\n\ndef main(item):\n    merge(prs = [struct(item = 1, head = True)], base = \"main\", repo = \"o/r\", name = \"n\")\n")

	assert.Empty(t, got)
}
