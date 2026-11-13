package ssh

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/tobocop2/beehave/internal/config"
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

// portUnset is the value of the port key that the call leaves out, so the decode can tell an omitted key from a written one.
const portUnset = -1

// maxPort is the largest port number.
const maxPort = 65535

// hostWord is the pattern of a name that can follow ssh options: it cannot start an option or hold a space.
var hostWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]*$`)

// block is the ssh block of the machine, as the project file wrote it: the link to the machine and the folder its repositories live in.
type block struct {
	address    string
	user       string
	port       int
	identity   string
	workRoot   string
	knownHosts string
}

// decodeBlock reads the ssh block. A nil block gives the defaults: every key empty, known_hosts strict.
func decodeBlock(from *model.Block) (block, error) {
	settings := block{port: portUnset, knownHosts: model.KnownHostsStrict}
	if from == nil {
		return settings, nil
	}

	at := from.At()

	address, _ := from.Text(BlockAddress)
	user, _ := from.Text(BlockUser)
	identity, _ := from.Text(BlockIdentity)
	workRoot, _ := from.Text(BlockWorkRoot)
	knownHosts, _ := from.Text(BlockKnownHosts)

	for _, check := range []error{
		checkKnownHosts(at, knownHosts), checkWord(at, BlockAddress, address), checkWord(at, BlockUser, user),
		checkPath(at, BlockIdentity, identity), checkPath(at, BlockWorkRoot, workRoot),
	} {
		if check != nil {
			return block{}, check
		}
	}

	port, given := from.Whole(BlockPort)
	if given {
		if err := checkPort(at, port); err != nil {
			return block{}, err
		}

		settings.port = port
	}

	settings.address, settings.user, settings.identity, settings.workRoot, settings.knownHosts = address, user, identity, workRoot, knownHosts

	return settings, nil
}

// checkKnownHosts refuses a host key policy that beehave does not offer.
func checkKnownHosts(at, value string) error {
	if value != model.KnownHostsStrict && value != model.KnownHostsAcceptNew {
		return fmt.Errorf("%s: `host.ssh.known_hosts` must be %s or %s. A host key that changed is always refused.", at, model.KnownHostsStrict, model.KnownHostsAcceptNew)
	}

	return nil
}

// checkPort refuses a port outside the range of the protocol.
func checkPort(at string, port int) error {
	if port < 1 || port > maxPort {
		return fmt.Errorf("%s: `host.ssh.port` must be from 1 to %d; got %d. Fix the value.", at, maxPort, port)
	}

	return nil
}

// checkWord refuses a value that could start an ssh option or hold a space.
func checkWord(at, key, value string) error {
	if value != "" && !hostWord.MatchString(value) {
		return fmt.Errorf("%s: `host.ssh.%s` must start with a letter or digit and hold only letters, digits, dots, underscores, at signs and dashes; got `%s`. Fix the value.", at, key, value)
	}

	return nil
}

// checkPath checks a path key of the ssh block: empty, or absolute (or under ~/), with no control character.
func checkPath(at, key, value string) error {
	switch {
	case value == "":
		return nil
	case strings.ContainsFunc(value, unicode.IsControl):
		return fmt.Errorf("%s: `host.ssh.%s` must hold no control character. Fix the value.", at, key)
	case !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "~/"):
		return fmt.Errorf("%s: `host.ssh.%s` must be an absolute path or start with ~/; got `%s`. Fix the value.", at, key, value)
	}

	return nil
}
