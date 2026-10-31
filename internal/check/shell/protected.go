package shell

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// Protection holds the setting of the kind protected-path: the directories no command may write in, and the directory the command starts in.
// Dirs are absolute paths; Base is the working directory of the command and is absolute too.
type Protection struct {
	Dirs []string
	Base string
	// IgnoresCase says whether the file system of a protected directory ignores case. A nil function probes the file system once for each directory.
	IgnoresCase func(dir string) bool
}

const (
	maxExpansions = 8
	metaChars     = "$`*?[{"
)

// protector holds the protected directories as the caller wrote them and with their symbolic links resolved.
// Each directory is held twice, as written and with its links resolved, and it knows whether its file system ignores case.
type protector struct {
	dirs []guarded
}

type guarded struct {
	dir  string
	fold bool
}

func newProtector(p Protection) *protector {
	if len(p.Dirs) == 0 {
		return nil
	}

	g := &protector{}

	for _, dir := range p.Dirs {
		g.dirs = append(g.dirs, guard(p, dir)...)
	}

	return g
}

// guard returns the directory as written and with its links resolved, both with the case setting of its file system.
func guard(p Protection, dir string) []guarded {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(p.Base, dir)
	}

	dir = filepath.Clean(dir)

	ignoresCase := p.IgnoresCase
	if ignoresCase == nil {
		ignoresCase = foldsCase
	}

	fold := ignoresCase(dir)

	return []guarded{{dir, fold}, {realPath(dir), fold}}
}

// foldsCase reports whether the file system of a directory ignores case: the same path with its case swapped names the same file.
// A directory that does not exist is judged by its nearest parent that does. The loop ends at the root, which is its own parent.
func foldsCase(dir string) bool {
	cur := filepath.Clean(dir)

	for {
		info, err := os.Stat(cur)
		if err != nil && filepath.Dir(cur) != cur {
			cur = filepath.Dir(cur)

			continue
		}

		swapped := strings.Map(swapCase, cur)
		other, swappedErr := os.Stat(swapped)

		return err == nil && swapped != cur && swappedErr == nil && os.SameFile(info, other)
	}
}

func swapCase(r rune) rune {
	switch {
	case unicode.IsUpper(r):
		return unicode.ToLower(r)
	case unicode.IsLower(r):
		return unicode.ToUpper(r)
	}

	return r
}

// realPath resolves the symbolic links of the longest part of the path that exists, and keeps the rest as written.
func realPath(name string) string {
	if resolved, err := filepath.EvalSymlinks(name); err == nil {
		return resolved
	}

	rest := ""
	cur := filepath.Clean(name)

	for cur != "/" && cur != "." {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}

		rest = filepath.Join(filepath.Base(cur), rest)
		cur = filepath.Dir(cur)
	}

	return filepath.Clean(name)
}

// inside reports whether name is dir or below it.
// With fold, the compare ignores case.
func inside(name, dir string, fold bool) bool {
	if fold {
		name, dir = strings.ToLower(name), strings.ToLower(dir)
	}

	return name == dir || strings.HasPrefix(name, strings.TrimSuffix(dir, "/")+"/")
}

// holds reports whether an absolute path is in a protected directory.
func (g *protector) holds(name string) bool {
	lexical, real := filepath.Clean(name), realPath(name)

	return slices.ContainsFunc(g.dirs, func(held guarded) bool {
		return inside(lexical, held.dir, held.fold) || inside(real, held.dir, held.fold)
	})
}

// reaches reports whether a directory is protected or holds a protected directory. A glob under the directory can name a protected path.
func (g *protector) reaches(dir string) bool {
	if g.holds(dir) {
		return true
	}

	lexical, real := filepath.Clean(dir), realPath(dir)

	return slices.ContainsFunc(g.dirs, func(held guarded) bool {
		return inside(held.dir, lexical, held.fold) || inside(held.dir, real, held.fold)
	})
}

// whereFrom returns the absolute path that a word names for a command that runs in cwd, or false when no static read can tell:
// a variable, a glob or a relative path from an unknown directory. The directory cwd is "" when no static read can tell.
func (s *scanner) whereFrom(cwd, word string) (string, bool) {
	word = s.expandPlain(word)

	if strings.HasPrefix(word, "~") || strings.ContainsAny(word, metaChars) {
		return "", false
	}

	switch {
	case path.IsAbs(word):
		return path.Clean(word), true
	case cwd == "":
		return "", false
	}

	return path.Join(cwd, word), true
}

// expandPlain replaces the variables that the command text set to a plain value.
func (s *scanner) expandPlain(word string) string {
	for range maxExpansions {
		next := varRef.ReplaceAllStringFunc(word, func(ref string) string {
			match := varRef.FindStringSubmatch(ref)
			if value, ok := s.plain[match[1]+match[2]]; ok {
				return value
			}

			return ref
		})
		if next == word {
			break
		}

		word = next
	}

	return word
}

// touches reports whether a word names a protected path. A word with a glob is read up to its first glob: a path that may reach a protected directory counts.
// A word that no static read can place (a variable the command did not set, a substitution, a path from an unknown directory) is a write the check cannot judge, so it counts and marks the command as blind.
func (s *scanner) touches(word string) bool { return s.touchesFrom(s.cwd, word) }

// touchesFrom is touches for a command that runs in cwd.
func (s *scanner) touchesFrom(cwd, word string) bool {
	word = s.expandPlain(word)

	if unsettled(word) {
		s.blind = true

		return true
	}

	if abs, ok := s.whereFrom(cwd, word); ok {
		return s.prot.holds(abs)
	}

	abs, ok := s.whereFrom(cwd, globStem(word))
	if !ok {
		s.blind = true

		return true
	}

	return s.prot.reaches(abs)
}

// unsettled reports whether a word still holds a variable or a substitution after the plain variables are expanded.
func unsettled(word string) bool { return strings.ContainsAny(word, "$`") }

// globStem returns the directory part of a word before its first glob. A word with no glob returns itself.
func globStem(word string) string {
	at := strings.IndexAny(word, metaChars)
	if at < 0 {
		return word
	}

	stem := word[:at]
	stem = stem[:strings.LastIndex(stem, "/")+1]

	if stem == "" {
		stem = "."
	}

	return stem
}
