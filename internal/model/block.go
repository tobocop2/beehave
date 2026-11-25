package model

import (
	"fmt"
	"slices"
	"strings"
)

// BlockEntry is one argument of an adapter block: the text of a text key, the whole number of a whole key, or the True or False of a bool key.
type BlockEntry struct {
	Key string
	// Text holds the value of a text key, Number holds the value of a whole-number key, and Truth holds the value of a bool key.
	Text   string
	Number int
	Truth  bool
	// Whole says the key holds the whole number in Number rather than the text in Text. Flag says the key holds the bool in Truth.
	Whole bool
	Flag  bool
}

// BoolEntry returns the entry of a bool key.
func BoolEntry(key string, value bool) BlockEntry {
	return BlockEntry{Key: key, Truth: value, Flag: true}
}

// TextEntry returns the entry of a text key.
func TextEntry(key, value string) BlockEntry {
	return BlockEntry{Key: key, Text: value}
}

// WholeEntry returns the entry of a whole-number key.
func WholeEntry(key string, value int) BlockEntry {
	return BlockEntry{Key: key, Number: value, Whole: true}
}

// Block is the config block of one adapter, as the project file wrote it. The adapter that owns the block
// reads each key once, at its edge, with Text, Whole and Bool; it holds no keys itself.
type Block struct {
	at      string
	entries []BlockEntry
}

// NewBlock returns the block that the call at position at built from its entries.
func NewBlock(at string, entries []BlockEntry) *Block {
	return &Block{at: at, entries: entries}
}

// At returns the position of the call that built the block, for the adapter's refusals.
func (b *Block) At() string {
	if b == nil {
		return ""
	}

	return b.at
}

// String returns the entries of the block in key order and leaves out its position, so a value that holds a block prints the same for the same definition.
func (b *Block) String() string {
	if b == nil {
		return "<nil>"
	}

	entries := slices.SortedFunc(slices.Values(b.entries), func(one, other BlockEntry) int { return strings.Compare(one.Key, other.Key) })
	parts := make([]string, len(entries))

	for i, entry := range entries {
		switch {
		case entry.Whole:
			parts[i] = fmt.Sprintf("%s=%d", entry.Key, entry.Number)
		case entry.Flag:
			parts[i] = fmt.Sprintf("%s=%t", entry.Key, entry.Truth)
		default:
			parts[i] = fmt.Sprintf("%s=%q", entry.Key, entry.Text)
		}
	}

	return "{" + strings.Join(parts, " ") + "}"
}

// Text returns the text of a key, and whether the call gave the key.
func (b *Block) Text(key string) (string, bool) {
	if b == nil {
		return "", false
	}

	for _, entry := range b.entries {
		if entry.Key == key && !entry.Whole && !entry.Flag {
			return entry.Text, true
		}
	}

	return "", false
}

// Bool returns the True or False of a key, and whether the call gave the key.
func (b *Block) Bool(key string) (value, given bool) {
	if b == nil {
		return false, false
	}

	for _, entry := range b.entries {
		if entry.Key == key && entry.Flag {
			return entry.Truth, true
		}
	}

	return false, false
}

// Whole returns the whole number of a key, and whether the call gave the key.
func (b *Block) Whole(key string) (int, bool) {
	if b == nil {
		return 0, false
	}

	for _, entry := range b.entries {
		if entry.Key == key && entry.Whole {
			return entry.Number, true
		}
	}

	return 0, false
}
