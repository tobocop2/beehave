package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
)

// The Claude Code plugin in plugins/claude-code is a thin layer: each command file runs an obee command. This file checks the plugin folder against the
// manifest rules of the Claude Code plugin docs (https://code.claude.com/docs/en/plugins/manifest-reference) and against the real command tree of obee.

const pluginFolder = "../../plugins/claude-code"

// pluginActions are the five actions of the plugin. Each has one command file.
var pluginActions = []string{"queue", "run", "status", "answer", "cancel"}

// obeeTree is the command tree of obee, read from the help text that the real command line prints.
type obeeTree struct {
	// subs maps a command path such as "item add" to the names of its subcommands.
	subs map[string][]string
	// flags maps a command path to its flags: long names without the dashes, and short names with the dash, such as "-h".
	flags map[string]map[string]bool
}

var (
	subLine  = regexp.MustCompile(`^  ([a-z][a-z-]*)\s`)
	flagLine = regexp.MustCompile(`^\s+(?:(-[A-Za-z]), )?--([a-z][a-z-]*)`)
)

// helpOf runs obee with the arguments plus --help and returns what it prints.
func helpOf(t *testing.T, args []string) string {
	t.Helper()

	var out, errOut bytes.Buffer
	env := map[string]string{}
	code := cli.Execute(context.Background(), append(append([]string{}, args...), "--help"), buildInfo(env, "dev", flowFactory()), env, &out, &errOut)
	require.Zero(t, code, errOut.String())

	return out.String()
}

// addFlags records the long name and the short name of the flag that a help line lists.
func addFlags(flags map[string]bool, line string) {
	m := flagLine.FindStringSubmatch(line)
	if m == nil {
		return
	}
	flags[m[2]] = true
	if m[1] != "" {
		flags[m[1]] = true
	}
}

// parseHelp returns the subcommand names and the flag names that a help text lists.
func parseHelp(help string) ([]string, map[string]bool) {
	var subs []string
	flags := map[string]bool{}
	section := ""
	for _, line := range strings.Split(help, "\n") {
		switch {
		case strings.HasSuffix(line, ":") && !strings.HasPrefix(line, " "):
			section = line
		case line == "":
			section = ""
		case section == "Available Commands:":
			if m := subLine.FindStringSubmatch(line); m != nil {
				subs = append(subs, m[1])
			}
		case section == "Flags:" || section == "Global Flags:":
			addFlags(flags, line)
		}
	}

	return subs, flags
}

// readTree walks the help of every command, from the root down.
func readTree(t *testing.T) obeeTree {
	t.Helper()

	tree := obeeTree{subs: map[string][]string{}, flags: map[string]map[string]bool{}}

	var walk func(args []string)
	walk = func(args []string) {
		key := strings.Join(args, " ")
		tree.subs[key], tree.flags[key] = parseHelp(helpOf(t, args))
		for _, name := range tree.subs[key] {
			if name == "help" || name == "completion" {
				continue
			}
			walk(append(append([]string{}, args...), name))
		}
	}
	walk(nil)

	return tree
}

// problemsOf returns what is wrong with one obee command line: a subcommand or a flag the command line does not have.
func (tree obeeTree) problemsOf(line string) []string {
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != "obee" {
		return []string{"not an obee command: " + line}
	}

	var walked []string
	var problems []string
	descending := true
	for _, token := range fields[1:] {
		key := strings.Join(walked, " ")
		switch {
		case isFlag(token):
			descending = false
			if name, shown := flagName(token); !tree.flags[key][name] {
				problems = append(problems, "obee "+key+" has no flag "+shown+" in: "+line)
			}
		case !descending, strings.ContainsAny(token[:1], `<$["'`):
			descending = false
		case contains(tree.subs[key], token):
			walked = append(walked, token)
		case len(tree.subs[key]) > 0:
			problems = append(problems, "obee "+key+" has no subcommand "+token+" in: "+line)
			descending = false
		default:
			descending = false
		}
	}

	return problems
}

// isFlag tells whether a token is a long flag (--name) or a short flag (-n).
func isFlag(token string) bool {
	return strings.HasPrefix(token, "--") || (len(token) >= 2 && token[0] == '-')
}

// flagName returns the key under which the tree stores a flag, and the text to show in a message. A long flag is stored without dashes, a short flag with its dash.
func flagName(token string) (string, string) {
	if long, found := strings.CutPrefix(token, "--"); found {
		name, _, _ := strings.Cut(long, "=")

		return name, "--" + name
	}

	return token[:2], token[:2]
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}

	return false
}

var (
	fenceLine   = regexp.MustCompile("^\\s*```")
	spanOfObee  = regexp.MustCompile("`(obee [^`]+)`")
	allowedTool = regexp.MustCompile(`Bash\((obee[^:)]*)`)
)

// obeeLinesIn returns every obee command line in a Markdown file: the lines of the fenced blocks that start with obee, and the inline code spans that do.
func obeeLinesIn(text string) []string {
	var lines []string
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case fenceLine.MatchString(line):
			fenced = !fenced
		case fenced && strings.HasPrefix(strings.TrimSpace(line), "obee "):
			lines = append(lines, strings.TrimSpace(line))
		case !fenced:
			for _, m := range spanOfObee.FindAllStringSubmatch(line, -1) {
				lines = append(lines, m[1])
			}
		}
	}

	return lines
}

// frontmatterOf returns the key and value pairs of the frontmatter of a Markdown file.
func frontmatterOf(text string) map[string]string {
	pairs := map[string]string{}
	rest, found := strings.CutPrefix(text, "---\n")
	if !found {
		return pairs
	}
	block, _, _ := strings.Cut(rest, "\n---")
	for _, line := range strings.Split(block, "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			pairs[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}

	return pairs
}

var kebab = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// pluginProblems returns every way the plugin folder breaks its rules. An empty result means the folder is sound.
func pluginProblems(plugin fs.FS, tree obeeTree) []string {
	problems, name := manifestProblems(plugin)
	problems = append(problems, marketplaceProblems(plugin, name)...)
	problems = append(problems, commandFileProblems(plugin)...)
	problems = append(problems, skillProblems(plugin)...)

	return append(problems, commandLineProblems(plugin, tree)...)
}

// missingKeys names each key that the frontmatter of a file lacks.
func missingKeys(file, text string, keys ...string) []string {
	var problems []string
	front := frontmatterOf(text)
	for _, key := range keys {
		if front[key] == "" {
			problems = append(problems, file+" lacks "+key+" in its frontmatter")
		}
	}

	return problems
}

// manifestProblems checks the manifest and returns its name. The docs require only name; claude plugin validate warns when version, description or author are missing.
func manifestProblems(plugin fs.FS) ([]string, string) {
	var manifest struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
		Author      struct {
			Name string `json:"name"`
		} `json:"author"`
	}
	raw, err := fs.ReadFile(plugin, ".claude-plugin/plugin.json")
	switch {
	case err != nil:
		return []string{"no manifest: " + err.Error()}, ""
	case json.Unmarshal(raw, &manifest) != nil:
		return []string{"the manifest is not valid JSON"}, ""
	}

	var problems []string
	if !kebab.MatchString(manifest.Name) {
		problems = append(problems, "the manifest needs a kebab-case name")
	}
	for field, value := range map[string]string{"version": manifest.Version, "description": manifest.Description, "author.name": manifest.Author.Name} {
		if value == "" {
			problems = append(problems, "the manifest lacks "+field)
		}
	}

	return problems, manifest.Name
}

// commandFileProblems checks that each action has a command file with the frontmatter fields and at least one obee command.
func commandFileProblems(plugin fs.FS) []string {
	var problems []string
	for _, action := range pluginActions {
		file := "skills/" + action + "/SKILL.md"
		text, err := fs.ReadFile(plugin, file)
		if err != nil {
			problems = append(problems, "no command file "+file)
			continue
		}
		problems = append(problems, missingKeys(file, string(text), "description", "argument-hint", "allowed-tools")...)
		front := frontmatterOf(string(text))
		if front["disable-model-invocation"] != "true" {
			problems = append(problems, file+" lets the model start the action by itself: set disable-model-invocation: true")
		}
		for _, rule := range strings.Split(front["allowed-tools"], ",") {
			if rule = strings.TrimSpace(rule); rule != "" && !strings.Contains(rule, "(") {
				problems = append(problems, file+" allows "+rule+" with no scope, which pre-approves every use of the tool")
			}
		}
		if len(obeeLinesIn(string(text))) == 0 {
			problems = append(problems, file+" runs no obee command")
		}
	}

	return problems
}

// skillProblems checks the one skill of the plugin.
func skillProblems(plugin fs.FS) []string {
	const file = "skills/obee/SKILL.md"
	skill, err := fs.ReadFile(plugin, file)
	if err != nil {
		return []string{"no skill at " + file}
	}

	return missingKeys(file, string(skill), "name", "description")
}

// marketplaceProblems checks the marketplace file against the rules of the docs: name, owner and plugins, with the entry name equal to the manifest name.
func marketplaceProblems(plugin fs.FS, manifestName string) []string {
	var market struct {
		Name  string `json:"name"`
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	raw, err := fs.ReadFile(plugin, ".claude-plugin/marketplace.json")
	switch {
	case err != nil:
		return []string{"no marketplace file: " + err.Error()}
	case json.Unmarshal(raw, &market) != nil:
		return []string{"the marketplace file is not valid JSON"}
	}

	var problems []string
	if market.Name == "" || market.Owner.Name == "" || len(market.Plugins) == 0 {
		problems = append(problems, "the marketplace file needs name, owner.name and plugins")
	}
	for _, entry := range market.Plugins {
		if entry.Name != manifestName {
			problems = append(problems, "the marketplace entry "+entry.Name+" differs from the manifest name "+manifestName)
		}
		if _, err := fs.Stat(plugin, path.Join(entry.Source, ".claude-plugin/plugin.json")); err != nil {
			problems = append(problems, "the marketplace source "+entry.Source+" holds no plugin")
		}
	}

	return problems
}

// commandLineProblems checks every obee command line in the command files and the skill, and the obee command in each allowed-tools line.
func commandLineProblems(plugin fs.FS, tree obeeTree) []string {
	var problems []string
	files, err := fs.Glob(plugin, "skills/*/SKILL.md")
	if err != nil {
		return []string{err.Error()}
	}
	for _, file := range files {
		raw, err := fs.ReadFile(plugin, file)
		if err != nil {
			problems = append(problems, "cannot read "+file)
			continue
		}
		text := string(raw)
		for _, line := range obeeLinesIn(text) {
			for _, problem := range tree.problemsOf(line) {
				problems = append(problems, file+": "+problem)
			}
		}
		for _, m := range allowedTool.FindAllStringSubmatch(text, -1) {
			for _, problem := range tree.problemsOf(strings.TrimSpace(m[1])) {
				problems = append(problems, file+": allowed-tools: "+problem)
			}
		}
	}

	return problems
}

func TestPlugin_FolderIsSound(t *testing.T) {
	t.Parallel()

	tree := readTree(t)
	assert.Empty(t, pluginProblems(os.DirFS(filepath.Clean(pluginFolder)), tree))
}

func TestPlugin_CommandTreeHasTheFiveActions(t *testing.T) {
	t.Parallel()

	tree := readTree(t)
	for _, name := range []string{"run", "status", "answer", "resume", "cancel", "item"} {
		assert.Contains(t, tree.subs[""], name)
	}
	assert.Contains(t, tree.subs["item"], "add")
	assert.True(t, tree.flags["item add"]["title"])
	assert.True(t, tree.flags["run"]["workflow"])
}

// pluginFixture is a sound plugin folder in memory, small enough to break one rule at a time.
func pluginFixture() fstest.MapFS {
	command := "---\ndescription: d\nargument-hint: <item>\nallowed-tools: Bash(obee status:*)\ndisable-model-invocation: true\n---\nRun:\n\n```sh\nobee status\n```\n"
	plugin := fstest.MapFS{
		".claude-plugin/plugin.json":      {Data: []byte(`{"name":"obee","version":"0.1.0","description":"d","author":{"name":"a"}}`)},
		".claude-plugin/marketplace.json": {Data: []byte(`{"name":"m","owner":{"name":"o"},"plugins":[{"name":"obee","source":"./"}]}`)},
		"skills/obee/SKILL.md":            {Data: []byte("---\nname: obee\ndescription: d\n---\nUse `obee status`.\n")},
	}
	for _, action := range pluginActions {
		plugin["skills/"+action+"/SKILL.md"] = &fstest.MapFile{Data: []byte(command)}
	}

	return plugin
}

func TestPluginProblems_AcceptsTheFixture(t *testing.T) {
	t.Parallel()

	assert.Empty(t, pluginProblems(pluginFixture(), readTree(t)))
}

func TestPluginProblems_NamesEachBrokenRule(t *testing.T) {
	t.Parallel()

	tree := readTree(t)
	cases := map[string]struct {
		edit func(fstest.MapFS)
		want string
	}{
		"manifest is not JSON": {func(p fstest.MapFS) { p[".claude-plugin/plugin.json"] = &fstest.MapFile{Data: []byte("{")} }, "not valid JSON"},
		"manifest lacks name": {func(p fstest.MapFS) {
			p[".claude-plugin/plugin.json"] = &fstest.MapFile{Data: []byte(`{"version":"1"}`)}
		}, "kebab-case name"},
		"manifest lacks version": {func(p fstest.MapFS) {
			p[".claude-plugin/plugin.json"] = &fstest.MapFile{Data: []byte(`{"name":"obee","description":"d","author":{"name":"a"}}`)}
		}, "lacks version"},
		"manifest is missing":     {func(p fstest.MapFS) { delete(p, ".claude-plugin/plugin.json") }, "no manifest"},
		"marketplace is missing":  {func(p fstest.MapFS) { delete(p, ".claude-plugin/marketplace.json") }, "no marketplace file"},
		"marketplace is not JSON": {func(p fstest.MapFS) { p[".claude-plugin/marketplace.json"] = &fstest.MapFile{Data: []byte("[")} }, "marketplace file is not valid JSON"},
		"marketplace lacks owner": {func(p fstest.MapFS) {
			p[".claude-plugin/marketplace.json"] = &fstest.MapFile{Data: []byte(`{"name":"m","plugins":[{"name":"obee","source":"./"}]}`)}
		}, "needs name, owner.name and plugins"},
		"marketplace entry name differs": {func(p fstest.MapFS) {
			p[".claude-plugin/marketplace.json"] = &fstest.MapFile{Data: []byte(`{"name":"m","owner":{"name":"o"},"plugins":[{"name":"other","source":"./"}]}`)}
		}, "differs from the manifest name"},
		"marketplace source is empty": {func(p fstest.MapFS) {
			p[".claude-plugin/marketplace.json"] = &fstest.MapFile{Data: []byte(`{"name":"m","owner":{"name":"o"},"plugins":[{"name":"obee","source":"./nowhere"}]}`)}
		}, "holds no plugin"},
		"command file is missing": {func(p fstest.MapFS) { delete(p, "skills/cancel/SKILL.md") }, "no command file skills/cancel/SKILL.md"},
		"command lacks frontmatter": {func(p fstest.MapFS) {
			p["skills/run/SKILL.md"] = &fstest.MapFile{Data: []byte("```sh\nobee status\n```\n")}
		}, "skills/run/SKILL.md lacks description"},
		"command runs nothing": {func(p fstest.MapFS) {
			p["skills/run/SKILL.md"] = &fstest.MapFile{Data: []byte("---\ndescription: d\nargument-hint: x\nallowed-tools: Read\n---\ntext\n")}
		}, "runs no obee command"},
		"command renames a subcommand": {func(p fstest.MapFS) {
			p["skills/status/SKILL.md"] = &fstest.MapFile{Data: []byte("---\ndescription: d\nargument-hint: x\nallowed-tools: Read\n---\n```sh\nobee statuz\n```\n")}
		}, "has no subcommand statuz"},
		"command names a flag the cli lacks": {func(p fstest.MapFS) {
			p["skills/run/SKILL.md"] = &fstest.MapFile{Data: []byte("---\ndescription: d\nargument-hint: x\nallowed-tools: Read\n---\n```sh\nobee run <item> --fast\n```\n")}
		}, "has no flag --fast"},
		"command names a short flag the cli lacks": {func(p fstest.MapFS) {
			p["skills/cancel/SKILL.md"] = &fstest.MapFile{Data: []byte("---\ndescription: d\nargument-hint: x\nallowed-tools: Bash(obee cancel:*)\n---\n```sh\nobee cancel -f <item>\n```\n")}
		}, "has no flag -f"},
		"command allows a tool with no scope": {func(p fstest.MapFS) {
			p["skills/queue/SKILL.md"] = &fstest.MapFile{Data: []byte("---\ndescription: d\nargument-hint: x\nallowed-tools: Bash(obee item add:*), Write\n---\n```sh\nobee status\n```\n")}
		}, "allows Write with no scope"},
		"command allows a tool with no scope after a comma with no space": {func(p fstest.MapFS) {
			p["skills/queue/SKILL.md"] = &fstest.MapFile{Data: []byte("---\ndescription: d\nargument-hint: x\nallowed-tools: Bash(obee item add:*),Write\ndisable-model-invocation: true\n---\n```sh\nobee status\n```\n")}
		}, "allows Write with no scope"},
		"command lets the model start it": {func(p fstest.MapFS) {
			p["skills/cancel/SKILL.md"] = &fstest.MapFile{Data: []byte("---\ndescription: d\nargument-hint: x\nallowed-tools: Bash(obee cancel:*)\n---\n```sh\nobee cancel <item>\n```\n")}
		}, "skills/cancel/SKILL.md lets the model start the action by itself"},
		"inline span names a bad subcommand": {func(p fstest.MapFS) {
			p["skills/run/SKILL.md"] = &fstest.MapFile{Data: []byte("---\ndescription: d\nargument-hint: x\nallowed-tools: Read\n---\nRun `obee go <item>` then:\n```sh\nobee status\n```\n")}
		}, "has no subcommand go"},
		"allowed-tools names a bad subcommand": {func(p fstest.MapFS) {
			p["skills/run/SKILL.md"] = &fstest.MapFile{Data: []byte("---\ndescription: d\nargument-hint: x\nallowed-tools: Bash(obee launch:*)\n---\n```sh\nobee status\n```\n")}
		}, "allowed-tools"},
		"skill is missing": {func(p fstest.MapFS) { delete(p, "skills/obee/SKILL.md") }, "no skill"},
		"skill lacks description": {func(p fstest.MapFS) {
			p["skills/obee/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: obee\n---\n")}
		}, "SKILL.md lacks description"},
		"skill names a bad flag": {func(p fstest.MapFS) {
			p["skills/obee/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: obee\ndescription: d\n---\nUse `obee item add p i --titel x`.\n")}
		}, "no flag --titel"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			plugin := pluginFixture()
			tc.edit(plugin)
			assert.Contains(t, strings.Join(pluginProblems(plugin, tree), "\n"), tc.want)
		})
	}
}

func TestProblemsOf_ReadsPlaceholdersAndRefusesOtherCommands(t *testing.T) {
	t.Parallel()

	tree := readTree(t)
	assert.Empty(t, tree.problemsOf(`obee item add <project> <id> --title "<title>" --body-file=<file>`))
	assert.Empty(t, tree.problemsOf("obee run $ARGUMENTS"))
	assert.Empty(t, tree.problemsOf("obee --version"))
	assert.Empty(t, tree.problemsOf("obee run -h"))
	assert.NotEmpty(t, tree.problemsOf("obee run -f"))
	assert.Empty(t, tree.problemsOf("obee run -"))
	assert.NotEmpty(t, tree.problemsOf("git status"))
	assert.NotEmpty(t, tree.problemsOf(""))
	assert.NotEmpty(t, tree.problemsOf("obee item zap"))
}

func TestObeeLinesIn_ReadsFencesAndSpans(t *testing.T) {
	t.Parallel()

	text := "Run `obee status` and `git log`.\n```sh\n  obee run 1\nls\n```\nAfter, `obee cancel 1`.\n"
	assert.Equal(t, []string{"obee status", "obee run 1", "obee cancel 1"}, obeeLinesIn(text))
	assert.Empty(t, frontmatterOf("no frontmatter"))
}
