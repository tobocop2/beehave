package ssh

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
)

// Name is the name the ssh host provider is chosen by.
const Name model.HostProvider = "ssh"

// Block returns the schema of the ssh block: the link to the machine and the folder its repositories live in, every key optional.
func Block() config.Block {
	return config.Block{Name: "ssh", Args: []config.Arg{
		{Key: BlockAddress, Optional: true}, {Key: BlockUser, Optional: true},
		{Key: BlockPort, Kind: config.WholeArg, Optional: true}, {Key: BlockIdentity, Optional: true},
		{Key: BlockWorkRoot, Optional: true}, {Key: BlockKnownHosts, Optional: true},
	}}
}

// The keys of the ssh block.
const (
	BlockAddress    = "address"
	BlockUser       = "user"
	BlockPort       = "port"
	BlockIdentity   = "identity"
	BlockWorkRoot   = "work_root"
	BlockKnownHosts = "known_hosts"
)

// maxPort is the largest port number.
const maxPort = 65535

// block is the ssh block of the machine, as the project file wrote it: the link to the machine and the folder its repositories live in.
// A key that the file leaves out stays empty, and the link reads an empty port as the default port and an empty known_hosts as strict.
type block struct {
	address    string
	user       string
	port       int
	identity   string
	workRoot   string
	knownHosts string
}

// decodeBlock reads the ssh block once. A nil block gives every key empty. The link refuses an address, a user or a known_hosts value that ssh could read as an option.
func decodeBlock(from *model.Block) (block, error) {
	var settings block

	if from == nil {
		return settings, nil
	}

	at := from.At()

	settings.address, _ = from.Text(BlockAddress)
	settings.user, _ = from.Text(BlockUser)
	settings.identity, _ = from.Text(BlockIdentity)
	settings.workRoot, _ = from.Text(BlockWorkRoot)
	settings.knownHosts, _ = from.Text(BlockKnownHosts)

	if port, given := from.Whole(BlockPort); given {
		if port < 1 || port > maxPort {
			return block{}, fmt.Errorf("%w: %s: `host.ssh.port` must be from 1 to %d; got %d. Fix the value", link.ErrConfig, at, maxPort, port)
		}

		settings.port = port
	}

	for _, key := range []struct{ name, value string }{{BlockIdentity, settings.identity}, {BlockWorkRoot, settings.workRoot}} {
		if err := checkPath(at, key.name, key.value); err != nil {
			return block{}, err
		}
	}

	return settings, nil
}

// checkPath checks a path key of the ssh block: empty, or absolute (or under ~/), with no control character.
func checkPath(at, key, value string) error {
	switch {
	case value == "":
		return nil
	case strings.ContainsFunc(value, unicode.IsControl):
		return fmt.Errorf("%w: %s: `host.ssh.%s` must hold no control character. Fix the value", link.ErrConfig, at, key)
	case !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "~/"):
		return fmt.Errorf("%w: %s: `host.ssh.%s` must be an absolute path or start with ~/; got `%s`. Fix the value", link.ErrConfig, at, key, value)
	}

	return nil
}
