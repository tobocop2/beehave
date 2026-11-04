package remediation_test

import (
	"bytes"
	"io/fs"
	"os"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
)

// factRule is one rule of the facts block of a prompt. The row is the conformance row that the rule closes.
type factRule struct {
	row    string
	phrase string
}

func factRules() []factRule {
	return []factRule{
		{"S1-O-02", "The run read these facts from the project file at its start"},
		{"S1-PS-01", "Repository identity and fork topology: repo"},
		{"S1-PS-02", "Build host:"},
		{"S1-PS-04", "The gate list comes from these workflow files:"},
		{"S1-PS-05", "Brief rules of the project:"},
		{"S1-PS-06", "Base branch:"},
		{"S1-PS-07", "Rules for a pull request that stacks on another:"},
		{"S1-PS-08", "People and the idle threshold for a third-party pull request:"},
		{"S1-PS-09", "Queue to epic:"},
		{"S1-PS-11", "Review against these convention files:"},
		{"S1-PS-13", "Token rules, cross-repository steps and release steps:"},
		{"S1-PS-14", "Hard limits, commands never run and real user data:"},
		{"S1-PS-15", "Measured traps and ownership rules:"},
		{"S1-PS-15", "A fact that is empty holds no fact: ask the owner, and do not guess it"},
	}
}

// factsLanes lists every prompt of the example: every agent the example starts gets the facts.
func factsLanes() []string {
	return []string{plan, implement, fix, review, "design", "take_review", "settle", integrate}
}

// promptData holds every input a prompt of the example reads, with the given facts.
func promptData(facts map[string]any) map[string]any {
	return map[string]any{
		"item": map[string]any{"id": "1", "title": "t", "body": "b"}, "plan": "p", "round": 1, "findings": "f", "violations": "", "facts": facts,
		"output_file": "/out.json", "mechanism": "m", "fixes": 1, "pr": "9", "ruling": "r", "integration": "8", "closes": "none",
	}
}

func TestPrompts_HoldTheFactsBlock(t *testing.T) {
	t.Parallel()

	folder := os.DirFS("prompts")

	for _, name := range factsLanes() {
		raw, err := fs.ReadFile(folder, name+".md")
		require.NoError(t, err)

		text := squash(string(raw))

		for _, rule := range factRules() {
			if !strings.Contains(text, rule.phrase) {
				t.Errorf("prompts/%s.md lost the phrase %q of the row %s", name, rule.phrase, rule.row)
			}
		}
	}
}

func facts() map[string]any {
	return map[string]any{
		"repo": "owner/name", "source": "/srv/clone", "forge": "github", "tracker": "beads", "base_branch": "integration", "gate": "make check", "build_host": "rig",
		"people": map[string]any{"owner": "ann", "third_party_idle_hours": "24"}, "epics": map[string]any{"bugs": "E-1"},
		"gate_files": []any{".github/workflows/ci.yml"}, "conventions": []any{"AGENTS.md"}, "brief_rules": []any{"name the tempting wrong fix"},
		"stacked_rules": []any{"merge the base first"}, "limits": []any{"never run rm on a glob"}, "traps": []any{"the cache hides a stale build"},
		"token_rules": []any{"read a log by its summary lines"}, "changelog": "CHANGELOG.md",
	}
}

// TestPrompts_TheFactsOfTheProjectReachTheText renders each prompt with facts and checks that every fact is in the text the agent reads.
func TestPrompts_TheFactsOfTheProjectReachTheText(t *testing.T) {
	t.Parallel()

	want := []string{
		"owner/name", "/srv/clone", "github", "beads", "integration", "make check", "rig", "`owner` is `ann`", "`third_party_idle_hours` is `24`", "`bugs` is `E-1`",
		".github/workflows/ci.yml", "AGENTS.md", "name the tempting wrong fix", "merge the base first", "never run rm on a glob", "the cache hides a stale build", "read a log by its summary lines",
	}

	for _, name := range factsLanes() {
		raw, err := fs.ReadFile(os.DirFS("prompts"), name+".md")
		require.NoError(t, err)

		tmpl, err := template.New(name).Option("missingkey=error").Parse(string(raw))
		require.NoError(t, err)

		var out bytes.Buffer

		require.NoError(t, tmpl.Execute(&out, promptData(facts())), name)

		for _, fact := range want {
			require.Contains(t, out.String(), fact, "prompts/%s.md does not put the fact %q in the text", name, fact)
		}
	}
}

func TestPrompts_AnEmptyFactRendersAsEmptyNotMissing(t *testing.T) {
	t.Parallel()

	empty := map[string]any{
		"repo": "", "source": "", "forge": "", "tracker": "", "base_branch": "", "gate": "", "build_host": "", "people": map[string]any{}, "epics": map[string]any{},
		"gate_files": []any{}, "conventions": []any{}, "brief_rules": []any{}, "stacked_rules": []any{}, "limits": []any{}, "traps": []any{}, "token_rules": []any{}, "changelog": "",
	}

	for _, name := range factsLanes() {
		raw, err := fs.ReadFile(os.DirFS("prompts"), name+".md")
		require.NoError(t, err)

		tmpl, err := template.New(name).Option("missingkey=error").Parse(string(raw))
		require.NoError(t, err)

		require.NoError(t, tmpl.Execute(&bytes.Buffer{}, promptData(empty)), name)
	}
}
