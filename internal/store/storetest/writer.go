package storetest

import (
	"context"

	"github.com/tobocop2/obee/internal/store"
)

// WriterProbe reads back what a writer under test did. A test builds it over the same fake or file as the writer.
// Items counts the items that Create filed. Status reads the status of an item. Comments counts the comments on an item.
type WriterProbe interface {
	Items() int
	Status(id string) store.Status
	Comments(id string) int
}

const (
	writeKey   = "run-1:plan"
	fieldBody  = "body"
	twoItems   = 2
	twoComment = 2
)

// CheckWriter checks the rules of store.Writer: one key gives one item, a status is idempotent, one comment key gives one comment.
func CheckWriter(r Reporter, writer store.Writer, probe WriterProbe) {
	r.Helper()

	c := check{r: r}
	id := checkCreate(c, writer, probe)

	child, _, err := writer.Create(context.Background(), "run-1:child", store.NewItem{Title: "child", Parent: id})
	c.noError(err, "Create with a parent")
	c.equal(probe.Items(), twoItems, "Create with another key: items")

	checkStatus(c, writer, probe, child)
	checkComment(c, writer, probe, id)
}

// checkCreate files an item twice with one key and returns its id.
func checkCreate(c check, writer store.Writer, probe WriterProbe) string {
	c.r.Helper()

	ctx := context.Background()
	item := store.NewItem{Title: "plan", Body: fieldBody}

	id, created, err := writer.Create(ctx, writeKey, item)
	c.noError(err, "Create")
	c.equal(created, true, "Create: created on the first call")

	again, created, err := writer.Create(ctx, writeKey, item)
	c.noError(err, "Create again")
	c.equal(again, id, "Create again: id of the first item")
	c.equal(created, false, "Create again: created")
	c.equal(probe.Items(), 1, "Create twice with one key: items")

	return id
}

// checkComment adds a comment twice with one key, then a comment with another key.
func checkComment(c check, writer store.Writer, probe WriterProbe, id string) {
	c.r.Helper()

	ctx := context.Background()

	c.noError(writer.Comment(ctx, id, "note", "first"), "Comment")
	c.noError(writer.Comment(ctx, id, "note", "first"), "Comment again")
	c.equal(probe.Comments(id), 1, "Comment twice with one key: comments")
	c.noError(writer.Comment(ctx, id, "other", "second"), "Comment with another key")
	c.equal(probe.Comments(id), twoComment, "Comment with another key: comments")
}

// checkStatus walks an item through the three statuses and sets each twice.
func checkStatus(c check, writer store.Writer, probe WriterProbe, id string) {
	c.r.Helper()

	for _, status := range []store.Status{store.StatusHold, store.StatusHold, store.StatusClosed, store.StatusClosed, store.StatusOpen, store.StatusOpen, store.StatusClosed, store.StatusOpen} {
		c.noError(writer.SetStatus(context.Background(), id, status), "SetStatus "+string(status))
		c.equal(probe.Status(id), status, "SetStatus: status read back")
	}
}

// CheckWriterFails runs every method of a writer whose commands all fail. Each must return an error and never a guess.
func CheckWriterFails(r Reporter, writer store.Writer) {
	r.Helper()

	check{r: r}.allFail([]call{
		{"Create", func(ctx context.Context) error {
			_, _, err := writer.Create(ctx, writeKey, store.NewItem{Title: "plan"})

			return err
		}},
		{"SetStatus", func(ctx context.Context) error { return writer.SetStatus(ctx, item, store.StatusClosed) }},
		{"Comment", func(ctx context.Context) error { return writer.Comment(ctx, item, "note", "text") }},
	})
}
