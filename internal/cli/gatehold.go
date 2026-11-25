package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// GateHolding changes the place of the gates in the queue and holds a running gate.
type GateHolding interface {
	// SetGatePriority sets the place of the queued gate of an item to a level, P0 to P3.
	SetGatePriority(item, level string) error
	// HoldGate stops the build job of the running gate of an item and frees its slot on the host.
	HoldGate(item string) error
	// ReleaseGate puts the held gate of an item back on the queue of its project with its place.
	ReleaseGate(item string) error
}

const gatePriorityArgs = 2

// gateGroup is the gate command: the command that judges a pull request with its own gate program when the build has one, and the commands that hold, release and move a gate.
func (f flowCommands) gateGroup() *cobra.Command {
	group := &cobra.Command{Use: "gate", Short: "Hold a gate, release it, or change its place in the queue"}
	if f.ownProgram != nil {
		group = f.gateCommand()
	}

	group.AddCommand(f.gateHoldCommand(), f.gateReleaseCommand(), f.gatePriorityCommand())

	return group
}

func (f flowCommands) gateHoldCommand() *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "hold <item>",
		Short: "Stop the running gate of an item and free its host slot",
		Long: "Stop the build job of the running gate of an item at the next tick of the engine and free its slot on the host. The gate waits, and it holds no slot of the host and no place on the queue of its project, so another gate of the project starts. " +
			"Run beehave gate release to put the gate back on the queue of its project with the place that it had. " +
			"The command refuses an item with no open gate, a gate that waits in the queue, a gate that runs on this machine, and a gate that is held already.",
		Args: oneItem,
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withClient(cmd, flag, func(client Steering) error { return client.HoldGate(args[0]) })
		},
	}
	addConfigFlag(command, &flag)

	return command
}

func (f flowCommands) gateReleaseCommand() *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "release <item>",
		Short: "Put the held gate of an item back on the queue of its project",
		Long: "Put the held gate of an item back on the queue of its project. The gate keeps the place that it had, so it starts before the gates that came to the queue after it, and it reopens its build job on the queue of its host. " +
			"The command refuses an item with no open gate and a gate that is not held.",
		Args: oneItem,
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withClient(cmd, flag, func(client Steering) error { return client.ReleaseGate(args[0]) })
		},
	}
	addConfigFlag(command, &flag)

	return command
}

// priorityArgs takes the item and the level.
func priorityArgs(cmd *cobra.Command, args []string) error {
	if len(args) != gatePriorityArgs {
		return errors.New("give the item and the level: beehave gate priority <item> <level>")
	}

	return itemArgs(cmd, args)
}

func (f flowCommands) gatePriorityCommand() *cobra.Command {
	var flag string

	command := &cobra.Command{
		Use:   "priority <item> <level>",
		Short: "Change the place of the queued gate of an item",
		Long: "Change the place in the queue of the gate of an item that waits. The level is P0, P1, P2 or P3, and P0 comes first. " +
			"The command changes the gate that waits for its project and the build job of a gate that waits for its host. " +
			"It refuses an item with no open gate, a gate that runs, and a level that is not P0 to P3.",
		Args: priorityArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return f.withClient(cmd, flag, func(client Steering) error { return setPriority(cmd, client, args[0], args[1]) })
		},
	}
	addConfigFlag(command, &flag)

	return command
}

// setPriority changes the place of the gate of an item and says so.
func setPriority(cmd *cobra.Command, client GateHolding, item, level string) error {
	if err := client.SetGatePriority(item, level); err != nil {
		return err
	}

	_, err := fmt.Fprintf(cmd.OutOrStdout(), "The gate of item %s now has the place of %s.\n", item, level)

	return err
}
