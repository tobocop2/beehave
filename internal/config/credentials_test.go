package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestLoadFolder_TheWriterCredentialReachesTheProfileAsAFileOrAsACommand(t *testing.T) {
	t.Parallel()

	file, problems := loadProjectText(t, call(localProject+`, credentials = {"writer": credential(file = "~/.config/beehave/credentials/w.token")}`))
	require.Empty(t, problems)
	require.Equal(t, map[model.Role]model.Credential{model.RoleWriter: {File: "~/.config/beehave/credentials/w.token"}}, file.Credentials)

	command, problems := loadProjectText(t, call(localProject+`, credentials = {"writer": credential(command = ["security", "find-generic-password", "-w"])}`))
	require.Empty(t, problems)
	require.Equal(t, map[model.Role]model.Credential{model.RoleWriter: {Command: []string{"security", "find-generic-password", "-w"}}}, command.Credentials)

	none, problems := loadProjectText(t, call(localProject))
	require.Empty(t, problems)
	require.Nil(t, none.Credentials)
}

func TestLoadFolder_ACredentialThatIsNotExactlyOneSourceOrNotInAWriterEntryIsRefused(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ args, want string }{
		"both sources":       {`credentials = {"writer": credential(file = "a", command = ["b"])}`, "exactly one of `file` and `command`"},
		"no source":          {`credentials = {"writer": credential()}`, "exactly one of `file` and `command`"},
		"an empty file":      {`credentials = {"writer": credential(file = " ")}`, "`file` of `credential` is empty"},
		"an empty command":   {`credentials = {"writer": credential(command = [])}`, "holds no program"},
		"an empty word":      {`credentials = {"writer": credential(command = ["a", ""])}`, "holds no program or an empty word"},
		"a role that is not": {`credentials = {"reader": credential(file = "a")}`, "is not a role"},
		"a key that is text": {`credentials = {1: credential(file = "a")}`, "is not a role"},
		"a value not a call": {`credentials = {"writer": "token"}`, "want credential(...)"},
		"not a dict":         {`credentials = ["writer"]`, "want a dict"},
	} {
		_, problems := loadProjectText(t, "\n"+call(localProject+", "+test.args))

		require.NotEmpty(t, problems, name)
		require.Contains(t, problems[0].Error(), test.want, name)
	}
}

func TestLoadFolder_ACredentialBlockThatNoCredentialsArgumentHoldsIsRefusedAndNamesTheProjectCall(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "w = credential(file = \"a\")\n"+call(localProject))

	require.NotEmpty(t, problems)
	require.Contains(t, problems[0].Error(), "builds a block that no `project` argument holds")
}
