package ssh

import (
	"errors"
	"strings"
	"unicode"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/host/link"
	"github.com/tobocop2/beehest/internal/model"
)

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

// Block returns the schema of the ssh block: the link to the machine and the folder its repositories live in, every key optional.
func Block() config.Block {
	return config.Block{Name: "ssh", Args: []config.Arg{
		{Key: BlockAddress, Optional: true},
		{Key: BlockUser, Optional: true},
		{Key: BlockPort, Kind: config.WholeArg, Optional: true},
		{Key: BlockIdentity, Optional: true},
		{Key: BlockWorkRoot, Optional: true},
		{Key: BlockKnownHosts, Optional: true},
	}}
}

// block is the ssh block of the machine, as the project file wrote it. A key that the file leaves out stays empty.
// The link reads an empty port as the default port and an empty known_hosts as strict.
type block struct {
	at         string // the position of the block in the project file, for a refusal
	address    string
	user       string
	port       int
	identity   string
	workRoot   string
	knownHosts string
}

// decodeBlock reads the ssh block once. A nil block gives every key empty. The link refuses an address, a user or a known_hosts value that ssh could read as an option.
func decodeBlock(from *model.Block) (block, error) {
	settings := block{at: from.At()}

	settings.address, _ = from.Text(BlockAddress)
	settings.user, _ = from.Text(BlockUser)
	settings.identity, _ = from.Text(BlockIdentity)
	settings.workRoot, _ = from.Text(BlockWorkRoot)

	var err error

	if settings.knownHosts, err = decodeKnownHosts(from); err != nil {
		return block{}, err
	}

	if settings.port, err = decodePort(from); err != nil {
		return block{}, err
	}

	return settings, errors.Join(checkPath(from.At(), BlockIdentity, settings.identity), checkPath(from.At(), BlockWorkRoot, settings.workRoot))
}

// decodeKnownHosts reads known_hosts. A block that leaves it out gives empty, which the link reads as strict. A key that is written empty is refused.
func decodeKnownHosts(from *model.Block) (string, error) {
	value, given := from.Text(BlockKnownHosts)
	if given && value == "" {
		return "", link.Refusal(from.At(), "`host.ssh.known_hosts` must be %s or %s. A host key that changed is always refused", model.KnownHostsStrict, model.KnownHostsAcceptNew)
	}

	return value, nil
}

// decodePort reads the port. A block that leaves it out gives zero, which the link reads as the default port.
func decodePort(from *model.Block) (int, error) {
	port, given := from.Whole(BlockPort)
	if given && (port < 1 || port > maxPort) {
		return 0, link.Refusal(from.At(), "`host.ssh.port` must be from 1 to %d; got %d. Fix the value", maxPort, port)
	}

	return port, nil
}

// checkPath checks a path key of the ssh block: empty, or absolute (or under ~/), with no control character.
func checkPath(at, key, value string) error {
	switch {
	case value == "":
		return nil
	case strings.ContainsFunc(value, unicode.IsControl):
		return link.Refusal(at, "`host.ssh.%s` must hold no control character. Fix the value", key)
	case !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "~/"):
		return link.Refusal(at, "`host.ssh.%s` must be an absolute path or start with ~/; got `%s`. Fix the value", key, value)
	}

	return nil
}
