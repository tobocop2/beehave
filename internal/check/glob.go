package check

import (
	"strings"
	"unicode"
)

const (
	setNegate  = '!'
	setClose   = ']'
	setRange   = '-'
	rangeWidth = 3
)

// runeSet is one `[...]` group of a shell pattern.
type runeSet struct {
	negate bool
	single []rune
	ranges [][2]rune
}

// globToken is one element of a compiled shell pattern: a star, any one rune, a set or a literal.
type globToken struct {
	star, any bool
	lit       rune
	set       *runeSet
}

func (t globToken) matches(char rune) bool {
	switch {
	case t.any:
		return true
	case t.set != nil:
		return t.set.has(char) != t.set.negate
	}

	return t.lit == char
}

func (s *runeSet) has(char rune) bool {
	for _, one := range s.single {
		if one == char {
			return true
		}
	}

	for _, span := range s.ranges {
		if span[0] <= char && char <= span[1] {
			return true
		}
	}

	return false
}

// compileGlob reads a pattern as `fnmatch` does: `*` and `?`, `[set]` with `!` to negate, and no escape character.
// A `[` with no closing `]` is a plain `[`. A `^` in a set is a plain `^`.
func compileGlob(pattern string) []globToken {
	runes := []rune(pattern)
	tokens := make([]globToken, 0, len(runes))

	for i := 0; i < len(runes); {
		token, next := compileOne(runes, i)
		tokens = append(tokens, token)
		i = next
	}

	return tokens
}

// compileOne reads the token that starts at index i and returns it and the index after it.
func compileOne(runes []rune, i int) (globToken, int) {
	switch runes[i] {
	case '*':
		return globToken{star: true}, i + 1
	case '?':
		return globToken{any: true}, i + 1
	case '[':
		return compileSet(runes, i+1)
	}

	return globToken{lit: runes[i]}, i + 1
}

// compileSet reads the set that starts after a `[` and returns its token and the index after it.
func compileSet(runes []rune, start int) (globToken, int) {
	end := setEnd(runes, start)
	if end >= len(runes) {
		return globToken{lit: '['}, start
	}

	body, set := runes[start:end], &runeSet{}
	if body[0] == setNegate {
		set.negate, body = true, body[1:]
	}

	fillSet(set, body)

	return globToken{set: set}, end + 1
}

// setEnd returns the index of the `]` that closes the set, or the length of the pattern when none does.
func setEnd(runes []rune, start int) int {
	end := start
	if end < len(runes) && runes[end] == setNegate {
		end++
	}

	if end < len(runes) && runes[end] == setClose {
		end++
	}

	for end < len(runes) && runes[end] != setClose {
		end++
	}

	return end
}

// fillSet splits the members of a set into single runes and `a-z` spans. A dash that starts or ends the members is a plain dash.
// A span whose end is below its start holds nothing.
func fillSet(set *runeSet, body []rune) {
	for i := 0; i < len(body); {
		if i+rangeWidth <= len(body) && body[i+1] == setRange {
			if body[i] <= body[i+2] {
				set.ranges = append(set.ranges, [2]rune{body[i], body[i+2]})
			}

			i += rangeWidth

			continue
		}

		set.single = append(set.single, body[i])
		i++
	}
}

// globMatches reports whether the whole name matches the compiled pattern. A star also matches a slash.
func globMatches(tokens []globToken, name string) bool {
	at, ok := consume(tokens, []rune(name))
	if !ok {
		return false
	}

	for at < len(tokens) && tokens[at].star {
		at++
	}

	return at == len(tokens)
}

// consume matches the runes against the tokens, going back to the last star when a token fails. It returns the index of the next token.
func consume(tokens []globToken, runes []rune) (int, bool) {
	at, starAt, starIn := 0, -1, 0

	for in := 0; in < len(runes); {
		switch {
		case at < len(tokens) && tokens[at].star:
			starAt, starIn = at, in
			at++
		case at < len(tokens) && tokens[at].matches(runes[in]):
			at++
			in++
		case starAt >= 0:
			starIn++
			in, at = starIn, starAt+1
		default:
			return at, false
		}
	}

	return at, true
}

// pyFields splits on white space as `str.split()` does, which also counts the four separator controls.
func pyFields(text string) []string {
	const firstSeparator, lastSeparator = 0x1c, 0x1f

	return strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= firstSeparator && r <= lastSeparator)
	})
}
