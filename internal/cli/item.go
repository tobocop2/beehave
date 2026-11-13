package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/tobocop2/beehave/internal/model"
)

// ItemStore is the item list of one project with the local tracker. Close ends its use of the file.
type ItemStore interface {
	Add(ctx context.Context, id, title, body string) error
	Item(ctx context.Context, id string) (map[string]string, error)
	OpenItems(ctx context.Context) ([]model.Item, error)
	Close() error
}

// ItemOpener opens the item list of a project. A nil value leaves the item commands out and the run check off.
type ItemOpener func(ctx context.Context, project model.Project) (ItemStore, error)

const (
	titleFlag    = "title"
	bodyFileFlag = "body-file"
)

// addItems adds the item command when the build has an item list.
func (f flowCommands) addItems(root *cobra.Command) {
	if len(f.items) == 0 {
		return
	}

	item := &cobra.Command{Use: "item", Short: "Add and list the items of a project that has the local tracker"}
	item.AddCommand(f.itemAddCommand(), f.itemListCommand())
	root.AddCommand(item)
}

func (f flowCommands) itemAddCommand() *cobra.Command {
	var flag, title, bodyFile string

	command := &cobra.Command{
		Use:   "add <project> <id> --title <title> --body-file <file>",
		Short: "Add an open item to a project that has the local tracker",
		Long:  "Add an open item to a project that has the local tracker. The item id is a name such as fix-login. The body is the text of the file: the agent reads it as the task.",
		Args:  cobra.ExactArgs(maxItemAddArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.itemAdd(cmd, flag, newItem{project: args[0], id: args[1], title: title, bodyFile: bodyFile})
		},
	}
	addConfigFlag(command, &flag)
	command.Flags().StringVar(&title, titleFlag, "", "the title of the item (required)")
	command.Flags().StringVar(&bodyFile, bodyFileFlag, "", "the file that holds the body of the item (required)")

	return command
}

// maxItemAddArgs is the project and the item id.
const maxItemAddArgs = 2

// newItem is what the user gives to add an item.
type newItem struct {
	project, id, title, bodyFile string
}

// body refuses an item with a bad id, no title or no body file, and returns the text of the body file.
func (n newItem) body() (string, error) {
	switch {
	case n.title == "":
		return "", fmt.Errorf("give the title of the item with --%s", titleFlag)
	case n.bodyFile == "":
		return "", fmt.Errorf("give the file that holds the body of the item with --%s", bodyFileFlag)
	}

	if err := itemID(n.id); err != nil {
		return "", err
	}

	text, err := os.ReadFile(filepath.Clean(n.bodyFile))
	if err != nil {
		return "", fmt.Errorf("read the body file: %w. Name a file that exists with --%s", err, bodyFileFlag)
	}

	return string(text), nil
}

func (f flowCommands) itemAdd(cmd *cobra.Command, flag string, item newItem) error {
	body, err := item.body()
	if err != nil {
		return err
	}

	project, err := f.localProject(cmd, flag, item.project)
	if err != nil {
		return err
	}

	err = f.withItems(project, func(items ItemStore) error { return items.Add(f.ctx, item.id, item.title, body) })
	if err != nil {
		return err
	}

	// A failed write to stdout has no recovery.
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "added item %s to project %s\n", item.id, item.project)

	return nil
}

func (f flowCommands) itemListCommand() *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "list <project>",
		Short: "List the open items of a project that has the local tracker",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return f.itemList(cmd, flag, args[0]) },
	}
	addConfigFlag(command, &flag)

	return command
}

func (f flowCommands) itemList(cmd *cobra.Command, flag, name string) error {
	project, err := f.localProject(cmd, flag, name)
	if err != nil {
		return err
	}

	items, err := f.openItems(project)
	if err != nil {
		return err
	}

	printItems(cmd.OutOrStdout(), name, items)

	return nil
}

// openItems reads the open items of the project.
func (f flowCommands) openItems(project model.Project) ([]model.Item, error) {
	var items []model.Item

	err := f.withItems(project, func(store ItemStore) error {
		var err error

		items, err = store.OpenItems(f.ctx)

		return err
	})

	return items, err
}

// printItems writes one line for each item: the id, a tab, the title.
func printItems(stdout io.Writer, project string, items []model.Item) {
	// A failed write to stdout has no recovery.
	if len(items) == 0 {
		_, _ = fmt.Fprintf(stdout, "no items in project %s\n", project)

		return
	}

	for _, item := range items {
		_, _ = fmt.Fprintf(stdout, "%s\t%s\n", item.ID, item.Title)
	}
}

// localProject loads the config and returns the named project. It refuses a project whose tracker is not local.
func (f flowCommands) localProject(cmd *cobra.Command, flag, name string) (model.Project, error) {
	cfg, _, err := f.options(cmd.ErrOrStderr(), flag)
	if err != nil {
		return model.Project{}, err
	}

	if _, err = chooseProject(cfg, []string{name}); err != nil {
		return model.Project{}, err
	}

	project := cfg.Projects[name].Project
	if f.items[project.Tracker] == nil {
		return model.Project{}, fmt.Errorf("project %s has the %s tracker. beehave item works only on a project with a tracker that holds a local list", name, project.Tracker)
	}

	return project, nil
}

// withItems opens the item list of the project, runs fn and closes the list. The caller has checked that the tracker of the project has an opener.
func (f flowCommands) withItems(project model.Project, fn func(ItemStore) error) error {
	items, err := f.items[project.Tracker](f.ctx, project)
	if err != nil {
		return err
	}

	return errors.Join(fn(items), items.Close())
}

// knownItem refuses an item that a project with a local item list does not have. Another tracker has no list here, so it passes.
// The run refuses before the engine opens, so a typo in an id takes no claim and starts nothing.
func (f flowCommands) knownItem(project model.Project, item string) error {
	if f.items[project.Tracker] == nil {
		return nil
	}

	return f.withItems(project, func(items ItemStore) error {
		_, err := items.Item(f.ctx, item)

		return err
	})
}
