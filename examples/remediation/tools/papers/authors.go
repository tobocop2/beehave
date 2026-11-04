package papers

import (
	"fmt"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	exitAuthorsRefused = 2
	exitWrongAddress   = 3
	noLogin            = "?"
	blanks             = " \t"
	newline            = "\n"
)

// AuthorsResult is what the commit author check says and the exit code it asks for.
type AuthorsResult struct {
	Out, Err []string
	Code     int
}

// Authors checks `<sha> <email> <login>` lines: a commit of ours must carry our address.
func Authors(text, meEmail, meLogin string) AuthorsResult {
	if meEmail == "" || meLogin == "" {
		return AuthorsResult{Code: exitAuthorsRefused, Err: []string{
			"REFUSE: --email and --login must both be given. The address says what a commit of ours",
			"        must carry; the login says which commits are ours. Without both, a wrong address",
			"        on our own commit cannot be told apart from a third party's legitimate one.",
		}}
	}

	if text == "" {
		return AuthorsResult{Code: exitAuthorsRefused, Err: []string{"REFUSE: no commit authors to check; refusing to call that clean"}}
	}

	tally := tallyAuthors(model.Records(text), meEmail, meLogin)
	if messages := authorMessages(tally.bad, tally.unlinked, meEmail); len(messages) > 0 {
		return AuthorsResult{Code: exitWrongAddress, Err: messages}
	}

	return AuthorsResult{Out: []string{fmt.Sprintf("author check: %d commits, %d ours and correctly addressed", tally.total, tally.mine)}}
}

// authorTally is what the author lines hold: the wrong addresses, the unlinked ones and the counts.
type authorTally struct {
	bad, unlinked []string
	mine, total   int
}

func tallyAuthors(lines []string, meEmail, meLogin string) authorTally {
	tally := authorTally{bad: []string{}, unlinked: []string{}}

	for _, line := range lines {
		email, login := authorFields2(line)
		if email == meEmail {
			tally.mine++
		}

		if line != "" {
			tally.total++
		}

		tally.sort(line, email, login, meEmail, meLogin)
	}

	return tally
}

func (tally *authorTally) sort(line, email, login, meEmail, meLogin string) {
	switch {
	case login == meLogin && email != meEmail:
		tally.bad = append(tally.bad, line)
	case login == noLogin && email != meEmail:
		tally.unlinked = append(tally.unlinked, line)
	}
}

func authorMessages(bad, unlinked []string, meEmail string) []string {
	var messages []string

	if len(bad) > 0 {
		messages = append(messages, "REFUSE: these commits are OURS but carry the wrong address (expected "+meEmail+"):")
		messages = append(messages, indented(bad)...)
		messages = append(messages,
			"  A commit under another address does not attribute to the account. Repair only OUR",
			"  commits, over the range they cover, and leave a third party's under their own name.")
	}

	if len(unlinked) > 0 {
		messages = append(messages, "REFUSE: these commits have no linked account and carry an address other than "+meEmail+":")
		messages = append(messages, indented(unlinked)...)
		messages = append(messages,
			"  They may be OUR commits under a wrong address. If they are, repair them.",
			"  If they are a third party's, link the address to the account, or merge by hand.")
	}

	return messages
}

func indented(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = "  " + line
	}

	return out
}

// authorFields2 returns the second and third field of a line, empty when missing.
func authorFields2(line string) (string, string) {
	fields := append(model.BlankFields(strings.Trim(line, blanks)), "", "", "")
	return fields[1], fields[2]
}
