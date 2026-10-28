package flow

import (
	"cmp"
	"maps"
	"regexp"
	"slices"

	"github.com/tobocop2/obee/internal/model"
)

const (
	rankReview = iota
	rankContainer
	rankRest
)

var closingKeyword = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):? +#([1-9][0-9]*)`)

// Order returns the items in queue order: items with a pull request in review first, then work under a container, then the rest.
// Inside a rank the order is severity, then item id.
func Order(items []model.Item, inReview map[string]bool) []model.Item {
	ordered := slices.Clone(items)
	slices.SortStableFunc(ordered, func(a, b model.Item) int {
		return cmp.Or(
			cmp.Compare(rankOf(a, inReview), rankOf(b, inReview)),
			cmp.Compare(severityRank(a.Severity), severityRank(b.Severity)),
			cmp.Compare(a.ID, b.ID),
		)
	})

	return ordered
}

func rankOf(item model.Item, inReview map[string]bool) int {
	switch {
	case inReview[item.ID]:
		return rankReview
	case item.Parent != "":
		return rankContainer
	default:
		return rankRest
	}
}

// severityRank puts P0 first and an unknown severity last.
func severityRank(severity model.Severity) int {
	if index := slices.Index(model.Severities(), severity); index >= 0 {
		return index
	}

	return len(model.Severities())
}

// ClosedItems returns the issue numbers a pull request body closes, each once, in numeric order.
func ClosedItems(body string) []string {
	found := map[string]bool{}

	for _, match := range closingKeyword.FindAllStringSubmatch(body, -1) {
		found[match[1]] = true
	}

	return slices.SortedFunc(maps.Keys(found), func(a, b string) int {
		return cmp.Or(cmp.Compare(len(a), len(b)), cmp.Compare(a, b))
	})
}
