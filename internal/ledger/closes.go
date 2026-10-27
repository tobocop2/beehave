package ledger

import (
	"maps"
	"regexp"
	"slices"
	"strings"
)

// closesPattern finds a closing keyword and an issue number. It spells each letter in both cases, because a case-folding match also takes the long s.
var closesPattern = regexp.MustCompile(`(?:[Cc][Ll][Oo][Ss][Ee][SsDd]?|[Ff][Ii][Xx](?:[Ee][SsDd])?|[Rr][Ee][Ss][Oo][Ll][Vv][Ee][SsDd]?):? +#([0-9]+)`)

// closingNumbers returns the issue numbers a pull request body closes, each once, in numeric order.
// Of two spellings of one number, such as 12 and 012, the first one stays.
func closingNumbers(body string) []string {
	spelling := map[string]string{}

	for _, match := range closesPattern.FindAllStringSubmatch(body, -1) {
		value := strings.TrimLeft(match[1], "0")
		if _, seen := spelling[value]; !seen {
			spelling[value] = match[1]
		}
	}

	var numbers []string

	for _, value := range slices.SortedFunc(maps.Keys(spelling), compareNumbers) {
		numbers = append(numbers, spelling[value])
	}

	return numbers
}

// compareNumbers orders two numbers without leading zeros by value, however many digits they have.
func compareNumbers(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}

	return strings.Compare(a, b)
}
