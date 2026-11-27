package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobocop2/beehest/internal/flow"
)

const sendLong = "Send a message to the open run of an item. A recv call of its workflow with the same topic receives it.\n" +
	"Give the value as text after the item, or as a file of JSON with --json. The command prints the id of the message.\n" +
	"A second send with the same --key to the same run inserts nothing, so a send that you repeat after a failure delivers once. " +
	"With no --key, the id is a fresh key: give it as --key to repeat the send."

// errSendValue is returned when beehest send gets both a text and --json, or neither.
var errSendValue = errors.New("give the value as text after the item, or as a file with --json, not both")

// sendFlags holds the flags of beehest send.
type sendFlags struct {
	config, topic, key, file string
}

func (f flowCommands) sendCommand() *cobra.Command {
	var flags sendFlags

	command := &cobra.Command{
		Use:   "send <item> --topic <topic> [--key <key>] <text> | send <item> --topic <topic> [--key <key>] --json <file>",
		Short: "Send a message to the open run of an item",
		Long:  sendLong,
		Args:  cobra.MinimumNArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return f.send(cmd, flags, args) },
	}
	addConfigFlag(command, &flags.config)
	command.Flags().StringVar(&flags.topic, "topic", "", "the topic of the message, which the recv call of the workflow names")
	command.Flags().StringVar(&flags.key, "key", "", "the idempotency key: a second send with this key to the same run inserts nothing")
	command.Flags().StringVar(&flags.file, "json", "", "a file that holds the value as JSON, in place of the text")

	return command
}

// send runs beehest send: it reads the value, sends the message and prints its id.
func (f flowCommands) send(cmd *cobra.Command, flags sendFlags, args []string) error {
	value, err := sendValue(flags.file, args[1:])
	if err != nil {
		return err
	}

	msg := flow.Outgoing{Topic: flags.topic, Key: flags.key, Sender: "cli:" + f.env["USER"], Value: value}

	return f.withClient(cmd, flags.config, func(client Steering) error {
		id, err := client.Send(args[0], msg)
		if err != nil {
			return err
		}

		_, _ = fmt.Fprintln(cmd.OutOrStdout(), id)

		return nil
	})
}

// sendValue is the JSON text of the value of a message: the words after the item as one string, or the bytes of the file of --json.
func sendValue(file string, words []string) (json.RawMessage, error) {
	if (file == "") == (len(words) == 0) {
		return nil, errSendValue
	}

	if file != "" {
		text, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			return nil, fmt.Errorf("read the value of the message: %w", err)
		}

		return text, nil
	}

	text, _ := json.Marshal(strings.Join(words, " ")) //nolint:errcheck,errchkjson // a string always encodes

	return text, nil
}
