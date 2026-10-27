package brief

// Kind is what a brief asks of a lane.
type Kind string

// The kinds of brief. An impl lane changes the product, a rev lane reviews, a design lane writes the rule and its acceptance script, a papers lane writes proof and papers only.
const (
	KindImpl   Kind = "impl"
	KindRev    Kind = "rev"
	KindDesign Kind = "design"
	KindPapers Kind = "papers"
)

const kindList = "impl, rev, design and papers"

// Kinds lists every kind of brief.
func Kinds() []Kind {
	return []Kind{KindImpl, KindRev, KindDesign, KindPapers}
}

// ParseKind returns the kind that text names.
func ParseKind(text string) (Kind, error) {
	for _, kind := range Kinds() {
		if string(kind) == text {
			return kind, nil
		}
	}

	return "", refuse("'%s' is not a kind of brief. The kinds are %s.", text, kindList)
}

// Block is a standing block of rules that a project takes or leaves.
type Block string

// The standing blocks.
const (
	BlockBuildHost Block = "build-host"
	BlockCompiled  Block = "compiled"
	BlockUnion     Block = "union"
)

// Blocks lists every standing block.
func Blocks() []Block {
	return []Block{BlockBuildHost, BlockCompiled, BlockUnion}
}

// ParseBlock returns the standing block that text names.
func ParseBlock(text string) (Block, error) {
	for _, block := range Blocks() {
		if string(block) == text {
			return block, nil
		}
	}

	return "", refuse("'%s' is not a standing block. The blocks are build-host, compiled and union.", text)
}

// Taken is the set of standing blocks a project takes.
type Taken []Block

// Has reports whether the set holds block.
func (t Taken) Has(block Block) bool {
	for _, taken := range t {
		if taken == block {
			return true
		}
	}

	return false
}

// Tier is the row of the model table that lets a strong model run. TierNone is a model that needs no row.
type Tier string

// The tier rows.
const (
	TierNone   Tier = ""
	TierSubtle Tier = "subtle"
	TierUnion  Tier = "union"
	TierStuck  Tier = "stuck"
)

// Tiers lists every tier row.
func Tiers() []Tier {
	return []Tier{TierSubtle, TierUnion, TierStuck}
}

// ParseTier returns the tier row that text names.
func ParseTier(text string) (Tier, error) {
	for _, tier := range Tiers() {
		if string(tier) == text {
			return tier, nil
		}
	}

	return TierNone, refuse("'%s' is not a tier row. The rows are subtle, union and stuck.", text)
}
