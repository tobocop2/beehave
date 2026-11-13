package model

// BlockEntry is one argument of an adapter block: the text of a text key, or the whole number of a whole key.
type BlockEntry struct {
	Key string
	// Text holds the value of a text key, and Number holds the value of a whole-number key.
	Text   string
	Number int
	// Whole says the key holds the whole number in Number rather than the text in Text.
	Whole bool
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
// reads each key once, at its edge, with Text and Whole; it holds no keys itself.
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

// Text returns the text of a key, and whether the call gave the key.
func (b *Block) Text(key string) (string, bool) {
	if b == nil {
		return "", false
	}

	for _, entry := range b.entries {
		if entry.Key == key && !entry.Whole {
			return entry.Text, true
		}
	}

	return "", false
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
