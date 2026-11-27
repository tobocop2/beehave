package githubactions_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/ci"
	"github.com/tobocop2/beehest/internal/ci/githubactions"
)

// readOnlyFile is the top of a workflow file whose token of the run can only read.
const readOnlyFile = "on: push\npermissions:\n  contents: read\n"

// formStep puts a body of steps in a job of a read-only file, before a step that checks and never publishes.
func formStep(body string) string {
	return readOnlyFile + "jobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n" + body + "      - run: make test\n"
}

func formRun(command string) string { return formStep("      - run: " + command + "\n") }

func formUses(action, with string) string {
	return formStep("      - uses: " + action + "\n" + with)
}

func formJobs(job string) string {
	return readOnlyFile + "jobs:\n" + job + "  t:\n    runs-on: x\n    steps:\n      - run: make test\n"
}

// structuralForms are the forms that the label refuses by the structure of the file: an action, a reusable workflow, a secret, a shell.
func structuralForms() map[string]string {
	return map[string]string{
		"goreleaser docker image": formUses("docker://goreleaser/goreleaser:latest", "        with:\n          args: release --clean\n"),
		"goreleaser action":       formUses("goreleaser/goreleaser-action@v6", "        with:\n          args: ${{ env.ARGS }}\n"),
		"commit action":           formUses("EndBug/add-and-commit@v9", ""),
		"github-script":           formUses("actions/github-script@v7", "        with:\n          script: github.rest.repos.createRelease({owner:'o',repo:'r',tag_name:'v1'})\n"),
		"build push action":       formUses("docker/build-push-action@v6", "        with:\n          push: True\n"),
		"changesets action":       formUses("changesets/action@v1", "        with:\n          publish: npm run release\n"),
		"pypi action":             formUses("pypa/gh-action-pypi-publish@release/v1", ""),
		"deploy pages":            formUses("actions/deploy-pages@v4", ""),
		"ssh action":              formUses("appleboy/ssh-action@v1", "        with:\n          script: ./deploy.sh\n"),
		"local composite action":  formUses("./.github/actions/release", ""),
		"reusable workflow":       formJobs("  rel:\n    uses: o/r/.github/workflows/release.yaml@main\n    secrets: inherit\n"),
		"local reusable workflow": formJobs("  rel:\n    uses: ./.github/workflows/release.yaml\n"),
		"shell ruby":              formStep("      - shell: ruby {0}\n        run: |\n          system('git push')\n"),
		"shell pwsh":              formStep("      - shell: pwsh\n        run: git push origin main\n"),
		"a secret in a step":      formJobs("  s:\n    steps:\n      - env:\n          K: ${{ secrets.KEY }}\n        run: make publish-docs\n"),
		"a secret in a with":      formJobs("  s:\n    steps:\n      - uses: actions/checkout@v7\n        with:\n          token: ${{ secrets.PAT }}\n"),
	}
}

func TestDerive_EveryStructuralFormIsLeftOutOfTheList(t *testing.T) {
	t.Parallel()

	for name, text := range structuralForms() {
		got, err := githubactions.Derive([]ci.File{{Path: "w.yaml", Text: text}})

		require.NoError(t, err, name)
		commands := make([]string, len(got.Steps))
		for i, step := range got.Steps {
			commands[i] = step.Command
		}

		assert.Equal(t, []string{"make test"}, commands, name)
		assert.NotEmpty(t, got.Refused, name)
	}
}

func TestDerive_ATestOfTheRepositoryThatOnlyNamesAPublishingToolIsKept(t *testing.T) {
	t.Parallel()

	for name, command := range map[string]string{
		"echo of a command":  "echo git push is not run here",
		"grep for a command": "grep -r 'npm publish' docs",
		"git status":         "git status --short",
		"docker build":       "docker build -t img .",
		"npm test":           "npm test",
		"cargo test":         "cargo test --all",
		"curl get":           "curl -fsS https://example.com/health",
		"a make target named for the release tool": "make goreleaser-install",
		"go proxy off":        "GOPROXY=off go test ./...",
		"make with a target":  "make check",
		"a gate with options": "go test -race -count=1 ./...",
	} {
		got := derivedAll(t, formRun(command))

		assert.Len(t, got.Steps, 2, name)
		assert.Empty(t, got.Refused, name)
	}
}

func TestDerive_ASetupOrCheckoutActionGivesNoStepAndIsNotRefused(t *testing.T) {
	t.Parallel()

	got := derivedAll(t, formUses("actions/setup-go@v6", "        with:\n          go-version: '1.27'\n"))

	assert.Len(t, got.Steps, 1)
	assert.Empty(t, got.Refused)
}

func TestDerive_ASetupActionPathThatIsNotOneSetupActionIsRefused(t *testing.T) {
	t.Parallel()

	for name, action := range map[string]string{
		"a path under a setup action":    "actions/setup-go/sub/dir@v1",
		"a setup action with no name":    "actions/setup-@v1",
		"a path under a checkout action": "actions/checkout/sub@v4",
		"a setup action of another org":  "actionsx/setup-go@v1",
	} {
		got := derivedAll(t, formUses(action, ""))

		require.Len(t, got.Refused, 1, name)
		assert.Contains(t, got.Refused[0].Reason, "neither a checkout nor a setup", name)
	}
}

func TestDerive_AStepThatOnlyMentionsAPublishingCommandIsNotRefused(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"echo of a command":  formRun(`echo "git push is not run here"`),
		"grep for a command": formRun("grep -r 'npm publish' docs"),
		"setup action":       formUses("actions/setup-go@v6", "        with:\n          go-version: '1.27'\n"),
		"other setup action": formUses("actions/setup-ruby@v1", "        with:\n          ruby-version: '3.3'\n"),
	} {
		got := derivedAll(t, text)

		assert.Empty(t, got.Refused, name)
	}
}
