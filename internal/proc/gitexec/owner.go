package gitexec

import (
	"os"

	"github.com/tobocop2/beehest/internal/credential"
	"github.com/tobocop2/beehest/internal/model"
)

// OwnerEnv returns the environment of an owner git call that takes no token: the network names of base, the pairs, and no config file of the owner.
// A call to an ssh remote (ssh is true) also keeps HOME and the ssh agent socket of base, which ssh reads. ssh reads ~/.ssh/config itself, and that file is
// not a git config, so this environment does not cover it.
func OwnerEnv(base map[string]string, ssh bool, pairs ...string) []string {
	own := append(userConfigOff(), "XDG_CONFIG_HOME="+os.DevNull)

	if ssh {
		for _, name := range []string{"HOME", model.SSHAgentVar} {
			if value, found := base[name]; found {
				own = append(own, name+"="+value)
			}
		}
	}

	return credential.RoleEnv(base, append(own, pairs...)...)
}
