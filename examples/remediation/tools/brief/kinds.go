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
