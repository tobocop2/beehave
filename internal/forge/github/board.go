package github

import (
	"context"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
)

const (
	argProject   = "project"
	argOwner     = "--owner"
	argFormat    = "--format"
	argLimit     = "--limit"
	jsonFormat   = "json"
	statusField  = "Status"
	boardListMax = 1000
)

type boardView struct {
	ID string `json:"id"`
}

type boardFields struct {
	Fields []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Options []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"options"`
	} `json:"fields"`
}

type boardAdded struct {
	ID string `json:"id"`
}

type boardItems struct {
	Total int `json:"totalCount"`
	Items []struct {
		Status  string `json:"status"`
		Content struct {
			Type       string `json:"type"`
			Number     int    `json:"number"`
			Repository string `json:"repository"`
		} `json:"content"`
	} `json:"items"`
}

// PlaceCard adds the pull request or issue to the board and sets the option of its Status field. gh item-add returns the existing card for an item
// that is on the board already, and item-edit with the same option changes nothing, so the same call again is safe. The column is looked up by name before
// any write, so a name that the board lacks is refused with the names that it has.
func (f *Forge) PlaceCard(ctx context.Context, card model.Card) error {
	project, field, option, err := f.columnIDs(ctx, card)
	if err != nil {
		return err
	}

	added, err := boardRead[boardAdded](ctx, f, "item-add", f.boardArgs(card, "item-add", "--url", f.cardAddress(card), argFormat, jsonFormat))
	if err != nil {
		return err
	}

	if added.ID == "" {
		return forge.Refuse("gh project answered with no id for the card on board %d", card.Board)
	}

	done := f.run.Run(ctx, []string{argProject, "item-edit", "--id", added.ID, "--project-id", project, "--field-id", field, "--single-select-option-id", option})
	if done.ExitCode != 0 {
		return forge.Refuse("gh project item-edit could not set the column %q of the card on board %d", card.Status, card.Board)
	}

	return nil
}

// CardStatus reads the Status column of the card from the item list of the board. A list that is longer than the read limit is refused, because the
// card could be past the end. The card is matched by repository, kind and number.
func (f *Forge) CardStatus(ctx context.Context, card model.Card) (string, error) {
	listed, err := boardRead[boardItems](ctx, f, "item-list", f.boardArgs(card, "item-list", argFormat, jsonFormat, argLimit, strconv.Itoa(boardListMax)))
	if err != nil {
		return "", err
	}

	if listed.Total > len(listed.Items) {
		return "", forge.Refuse("the item list of board %d is cut short: %d items, %d read", card.Board, listed.Total, len(listed.Items))
	}

	return f.statusIn(listed, card)
}

// statusIn finds the card in the item list and returns its status.
func (f *Forge) statusIn(listed boardItems, card model.Card) (string, error) {
	kind := "Issue"
	if card.Pull {
		kind = "PullRequest"
	}

	for _, item := range listed.Items {
		if item.Content.Number == card.Number && item.Content.Type == kind && strings.EqualFold(item.Content.Repository, f.repo) {
			return item.Status, nil
		}
	}

	return "", forge.Refuse("%s #%d is not on board %d", strings.ToLower(kind), card.Number, card.Board)
}

// columnIDs reads the id of the board, the id of its Status field and the id of the option that the card status names. It writes nothing.
func (f *Forge) columnIDs(ctx context.Context, card model.Card) (project, field, option string, err error) {
	view, err := boardRead[boardView](ctx, f, argView, f.boardArgs(card, argView, argFormat, jsonFormat))
	if err != nil {
		return "", "", "", err
	}

	if view.ID == "" {
		return "", "", "", forge.Refuse("gh project answered with no id for board %d", card.Board)
	}

	fields, err := boardRead[boardFields](ctx, f, "field-list", f.boardArgs(card, "field-list", argFormat, jsonFormat))
	if err != nil {
		return "", "", "", err
	}

	field, option, err = columnOf(fields, card)

	return view.ID, field, option, err
}

// boardArgs builds a gh project command for the board of the card: the subcommand, the board number, the owner and then the rest.
func (f *Forge) boardArgs(card model.Card, sub string, rest ...string) []string {
	return append([]string{argProject, sub, strconv.Itoa(card.Board), argOwner, f.boardOwner(card)}, rest...)
}

// boardOwner is the owner of the board: the one that the card names, or the owner of the repository.
func (f *Forge) boardOwner(card model.Card) string {
	if card.Owner != "" {
		return card.Owner
	}

	return f.owner
}

// cardAddress is the web address of the pull request or issue of the card, which gh project item-add takes.
func (f *Forge) cardAddress(card model.Card) string {
	kind := "issues"
	if card.Pull {
		kind = "pull"
	}

	return "https://github.com/" + strings.TrimSpace(f.repo) + "/" + kind + "/" + strconv.Itoa(card.Number)
}

// boardRead runs one gh project command and decodes its JSON answer. A failed command is a refusal that names the subcommand and the token scope that gh project needs.
func boardRead[T any](ctx context.Context, f *Forge, name string, args []string) (T, error) { //nolint:ireturn // the decoder returns the caller's own type
	var zero T

	done := f.run.Run(ctx, args)
	if done.ExitCode != 0 {
		return zero, forge.Refuse("gh project %s failed. The token needs the project scope, and the board must exist", name)
	}

	out, err := model.Decode[T]("gh project "+name, done.Stdout)
	if err != nil {
		return zero, forge.Refusal("cannot read the answer of gh project "+name, err)
	}

	return out, nil
}

// columnOf returns the id of the Status field and the id of its option with the name of the card status.
func columnOf(fields boardFields, card model.Card) (field, option string, err error) {
	for _, candidate := range fields.Fields {
		if candidate.Name != statusField {
			continue
		}

		names := make([]string, 0, len(candidate.Options))

		for _, opt := range candidate.Options {
			if opt.Name == card.Status {
				return candidate.ID, opt.ID, nil
			}

			names = append(names, opt.Name)
		}

		return "", "", forge.Refuse("board %d has no column %q. It has: %s", card.Board, card.Status, strings.Join(names, ", "))
	}

	return "", "", forge.Refuse("board %d has no %s field", card.Board, statusField)
}
