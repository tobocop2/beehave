package github

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
)

const (
	noBody       = "(no body)"
	unknownLine  = "?"
	nothingSaid  = "# nothing has been said yet: every finding you return is NEW by definition."
	noneStanding = "none"
)

// ExistingReview returns the report of everything said on a pull request, one entry for each output line. On a failed read of the change requests it returns the lines it has with the error.
func (f *Forge) ExistingReview(ctx context.Context, pr int) ([]string, error) {
	number := strconv.Itoa(pr)

	reviews, inline, conversation, err := f.sources(ctx, number)
	if err != nil {
		return nil, err
	}

	lines := report(pr, reviews, inline, conversation)

	standing, err := f.OpenChangeRequests(ctx, pr)
	if err != nil {
		return lines, err
	}

	joined := model.JoinChangeRequests(standing)
	if joined == "" {
		joined = noneStanding
	}

	return append(lines, "\n# standing change requests: "+joined), nil
}

// sources reads the three places a pull request keeps what was said. An unreadable source is not an empty one.
func (f *Forge) sources(ctx context.Context, number string) ([]model.Review, []model.Comment, []model.Comment, error) {
	reviews, err := readSource[model.Review](ctx, f, "pulls/"+number+"/reviews")
	if err != nil {
		return nil, nil, nil, err
	}

	inline, err := readSource[model.Comment](ctx, f, "pulls/"+number+"/comments")
	if err != nil {
		return nil, nil, nil, err
	}

	conversation, err := readSource[model.Comment](ctx, f, "issues/"+number+"/comments")
	if err != nil {
		return nil, nil, nil, err
	}

	return reviews, inline, conversation, nil
}

func readSource[T any](ctx context.Context, f *Forge, path string) ([]T, error) {
	items, err := readPages[T](ctx, f, path)
	if err != nil {
		return nil, forge.Refusal("cannot read "+path+". An unreadable source is not an empty one.", err)
	}

	return items, nil
}

func reviewLine(review model.Review) string {
	when, body := "", noBody
	if review.SubmittedAt != nil {
		when = *review.SubmittedAt
	}

	if review.Body != nil {
		body = *review.Body
	}

	return fmt.Sprintf("\n## REVIEW [%s] %s %s\n%s", model.OrNull(string(review.State)), model.OrNull(review.User.Login), when, body)
}

func inlineLine(comment model.Comment) string {
	where := unknownLine

	switch {
	case comment.Line != nil:
		where = strconv.Itoa(*comment.Line)
	case comment.OriginalLine != nil:
		where = strconv.Itoa(*comment.OriginalLine)
	}

	return fmt.Sprintf("\n## INLINE %s:%s %s %s\n%s", model.OrNull(comment.Path), where, model.OrNull(comment.User.Login), model.OrNull(comment.CreatedAt), comment.Body)
}

func conversationLine(comment model.Comment) string {
	return fmt.Sprintf("\n## CONVERSATION %s %s\n%s", model.OrNull(comment.User.Login), model.OrNull(comment.CreatedAt), comment.Body)
}

func report(pr int, reviews []model.Review, inline, conversation []model.Comment) []string {
	lines := []string{fmt.Sprintf("# existing review on #%d: %d reviews, %d inline, %d conversation", pr, len(reviews), len(inline), len(conversation))}
	if len(reviews)+len(inline)+len(conversation) == 0 {
		lines = append(lines, nothingSaid)
	}

	for _, review := range reviews {
		lines = append(lines, reviewLine(review))
	}

	for _, comment := range inline {
		lines = append(lines, inlineLine(comment))
	}

	for _, comment := range conversation {
		lines = append(lines, conversationLine(comment))
	}

	return lines
}
