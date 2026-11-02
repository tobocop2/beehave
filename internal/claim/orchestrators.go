package claim

import (
	"context"
	"slices"
	"strings"
)

// Holding is one owner and the items it holds with a live lease, sorted.
type Holding struct {
	Holder string
	Items  []string
}

// Others lists the other owners that run now: each owner but the caller that holds a row whose lease has not ended, with its items.
// A row with no lease counts as live. An unreadable store is a refusal and gives no list, so a caller never reads a failed read as "nobody else runs".
func (c *Claims) Others(ctx context.Context, owner string) ([]Holding, error) {
	me, err := OwnerOf(owner)
	if err != nil {
		return nil, err
	}

	rows, err := c.held(ctx)
	if err != nil {
		return nil, err
	}

	items := map[string][]string{}

	for _, row := range rows {
		if row.Holder != me && c.live(row) {
			items[row.Holder] = append(items[row.Holder], row.Item)
		}
	}

	return holdings(items), nil
}

// holdings turns the items of each holder into a list sorted by holder, with the items of each sorted.
func holdings(items map[string][]string) []Holding {
	others := make([]Holding, 0, len(items))

	for holder, held := range items {
		slices.Sort(held)
		others = append(others, Holding{Holder: holder, Items: held})
	}

	slices.SortFunc(others, func(a, b Holding) int { return strings.Compare(a.Holder, b.Holder) })

	return others
}
