package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/model"
)

// forbidKinds returns every kind of the engine that a step lists in forbid_commands, except git-write, which a step turns on by itself.
func forbidKinds() []string {
	return slices.DeleteFunc(model.FindingKindNames(), func(name string) bool { return name == string(model.FindingGitWrite) })
}

func jsonEvent(command string) string {
	return `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":` + quoted(command) + `}}`
}

func quoted(text string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text) + `"`
}

func TestExample_TheHookOfTheExampleRefusesEachRuleOfTheCommandRows(t *testing.T) {
	t.Parallel()

	forbid := strings.Join(forbidKinds(), ",")
	protect := []string{"--forbid", forbid, "--protect", "/own/checkout"}

	for name, command := range hookRows() {
		code, stderr := hook(t, jsonEvent(command), protect...)

		assert.Equal(t, 2, code, "%s: %s", name, stderr)
	}

	for _, clean := range []string{"git commit -m x", "git push origin b", "git config --get core.editor", "cat main.go", "make check", "git -C /own/checkout log", "cat /own/checkout/main.go"} {
		code, stderr := hook(t, jsonEvent(clean), protect...)

		assert.Zero(t, code, "%s: %s", clean, stderr)
	}
}

func TestExample_EveryKindOfTheEngineThatAStepCanForbidHasARowThatTheHookRefuses(t *testing.T) {
	t.Parallel()

	for _, kind := range forbidKinds() {
		refused := false

		for _, command := range hookRows() {
			code, _ := hook(t, jsonEvent(command), "--forbid", kind, "--protect", "/own/checkout")
			refused = refused || code == 2
		}

		assert.True(t, refused, "no row is refused for the kind %s", kind)
	}
}

// hookRows returns the commands that the hook refuses, by the rule that each one breaks.
func hookRows() map[string]string {
	return map[string]string{
		"S1-L-94: a commit author":                   "git commit --author='A <a@x>' -m x",
		"S1-L-94: a configured author":               "git -c user.name=me commit -m x",
		"S1-M-34: no-verify":                         "git commit --no-verify -m x",
		"S1-M-34: stash":                             "git stash",
		"S1-M-34: a config write":                    "git config core.editor vi",
		"S1-M-34: a force-push":                      "git push --force origin b",
		"S1-M-34: a send-pack force":                 "git send-pack --force h b",
		"S1-M-34: a ref delete through the forge":    "gh api -X DELETE repos/o/r/git/refs/heads/b",
		"S1-M-34: a force update through the forge":  "gh api -X PATCH repos/o/r/git/refs/heads/b -F force=true -f sha=abc",
		"S1-M-34: a ref update through graphql":      "gh api graphql -f query='mutation{updateRef(input:{refId:\"x\",oid:\"y\",force:true}){clientMutationId}}'",
		"S1-S-09: a start file":                      "echo 'x' >> ~/.zshenv",
		"S1-S-09: a search path":                     "PATH=/x:$PATH make",
		"S1-S-14: a file with a key":                 "cat ~/.netrc",
		"S1-S-14: the whole config of git":           "git config --list",
		"S1-S-08: bulk staging stays refused":        "git add -A",
		"S1-S-08: a hidden command stays refused":    "$X -rf $Y",
		"S1-S-08: a delete of the laptop is refused": "rm -rf *",
		"S1-B-23: a write in the checkout":           "touch /own/checkout/a",
		"S1-B-23: git in the checkout":               "git -C /own/checkout commit -m x",
		"S1-B-23: git after a cd into the checkout":  "cd /own/checkout && git checkout -b x",
		"zsh-expansion: a zsh-only expansion":        "for n in ${=list}; do :; done",
		"unparsed: a quote that never closes":        "echo 'x",
		"delete-text: a delete that echo prints":     "echo 'rm -rf /tmp/x' > clean.sh",
		"laptop-copy: a copy to another machine":     "rsync -a ./src host:/work/src",
	}
}

func TestExample_TheHookRefusesACopyFromThisMachineToAnotherMachineUnlessTheRepositoryIsPrivate(t *testing.T) {
	t.Parallel()

	forbid := strings.Join(forbidKinds(), ",")
	base := []string{"--forbid", forbid, "--protect", "/own/checkout"}
	copies := []string{"rsync -a ./src host:/work/src", "scp a.txt me@host:/work", "tar cf - src | ssh host 'tar xf -'"}

	for _, command := range copies {
		for _, visibility := range [][]string{nil, {"--visibility", "public"}} {
			code, stderr := hook(t, jsonEvent(command), append(slices.Clone(base), visibility...)...)

			assert.Equal(t, 2, code, "%s: %s", command, stderr)
			assert.Contains(t, stderr, "laptop-copy")
			assert.Contains(t, stderr, "Clone the repository from the forge on the host")
		}

		code, stderr := hook(t, jsonEvent(command), append(slices.Clone(base), "--visibility", "private")...)

		assert.Zero(t, code, "%s: %s", command, stderr)
	}

	for _, clean := range []string{"scp host:/work/out.txt .", "rsync -a host:/work/out ./out", "rsync -a src/ dst/", "ssh host 'git clone https://example.com/o/r.git tree'"} {
		code, stderr := hook(t, jsonEvent(clean), base...)

		assert.Zero(t, code, "%s: %s", clean, stderr)
	}
}

func TestCheckCommand_AVisibilityThatNamesNothingIsRefused(t *testing.T) {
	t.Parallel()

	code, stderr := hook(t, jsonEvent("make check"), "--forbid", "laptop-copy", "--visibility", "secret")

	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "--visibility")
	assert.Contains(t, stderr, "unknown visibility")
}
