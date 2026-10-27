package brief

import (
	"cmp"
	"strconv"
	"strings"
)

// Spec is everything the text of one brief depends on.
// ReadFirst lists the paths of the read-first list. Rules is the lean cut of the project rules.
type Spec struct {
	Kind      Kind
	Model     Model
	Lane      Lane
	ReadFirst []string
	Rules     string
	Taken     Taken
}

// parts reads the fixed text of a brief and keeps the first part that it cannot read.
// The text of every part is embedded, and a test reads each one, so a missing part is a defect of the build.
type parts struct {
	err error
}

// fixed joins parts of the fixed text. A part that is missing adds no text and sets err.
func (p *parts) fixed(names ...Part) string {
	var text strings.Builder

	for _, name := range names {
		got, err := Text(name)
		if err != nil {
			p.err = cmp.Or(p.err, err)
		}

		text.WriteString(got)
	}

	return text.String()
}

// Emit returns the text of the brief: the fixed rules first, the lines of the item last.
// Two briefs of one kind share every byte up to the first line of the item, so a prompt cache reads the rules once.
// A part of the fixed text that cannot be read is an error, and no text comes back, so that a lane never reads a brief with a hole in it.
func Emit(spec Spec) (string, error) {
	var p parts

	text := p.fixed(PartSkeleton) +
		p.kindRules(spec.Kind) +
		p.laneRules(spec.Kind) +
		capText(cmp.Or(spec.Lane.Cap, ContextCap)) +
		p.standingRules(spec) +
		p.fixed(PartProjectRulesHeading) +
		spec.Rules +
		p.fixed(PartClosingRules) +
		p.itemLines(spec)
	if p.err != nil {
		return "", p.err
	}

	return text, nil
}

// kindRules returns the fixed rules of the kind. They hold no line of the item.
func (p *parts) kindRules(kind Kind) string {
	switch kind {
	case KindDesign:
		return p.fixed(PartDesignRules)
	case KindPapers:
		return p.fixed(PartPapersRules)
	case KindRev:
		return ""
	case KindImpl:
	}

	return "## Acceptance: the script decides, before the code and after it\n\n" + p.fixed(PartAcceptanceRules)
}

func (p *parts) laneRules(kind Kind) string {
	switch kind {
	case KindRev:
		return p.fixed(PartReviewRules)
	case KindImpl:
		return p.fixed(PartBeforeWriteRules, PartCallSiteRules)
	case KindDesign, KindPapers:
	}

	return p.fixed(PartBeforeWriteRules)
}

func capText(tokens int) string {
	return "\n## Your context cap: a trigger, not a hard stop\n\n" +
		"**Check your context size at every step boundary.** A step boundary is a commit, a job you have read, or a\n" +
		"finished review sweep. At each one, read your context size. " +
		"Past " + strconv.Itoa(tokens) + " tokens, finish the step you are in,\n" +
		"write the handoff and stop. Do not start the next step, and do not stop in the middle of one.\n"
}

// standingRules returns the rules that every lane takes, then the standing blocks the project takes.
func (p *parts) standingRules(spec Spec) string {
	var text strings.Builder

	for _, rule := range standingTable(spec) {
		if rule.on {
			text.WriteString(p.fixed(rule.part))
		}
	}

	return text.String()
}

// rule is a part of the fixed text and whether this brief takes it.
type rule struct {
	part Part
	on   bool
}

// standingTable lists the standing parts in the order they appear. The code kinds are impl and rev.
func standingTable(spec Spec) []rule {
	code := spec.Kind == KindImpl || spec.Kind == KindRev
	union := spec.Model.Tier == TierUnion || spec.Lane.Union

	return []rule{
		{PartLongJobsRules, true},
		{PartBuildHostRules, spec.Taken.Has(BlockBuildHost)},
		{PartHandoffFirstRules, true},
		{PartGateStepRules, code},
		{PartEvidenceRules, true},
		{PartPrecedenceRules, code},
		{PartCompiledRules, code && spec.Taken.Has(BlockCompiled)},
		{PartUnionRules, union && spec.Taken.Has(BlockUnion)},
	}
}

func header(model Model) string {
	tier := ""
	if model.Tier != TierNone {
		tier = " Tier row: `" + string(model.Tier) + "`."
	}

	return "# Lane `[LANE]`: [ONE LINE SAYING WHAT AND WHY]\n\n" +
		"**Dispatch model: `" + model.Name + "`.**" + tier +
		" Orchestrator: pass it as the `model` of the dispatch. A dispatch with no\nmodel inherits yours.\n\n"
}

func readFirst(spec Spec) string {
	var text strings.Builder

	for _, path := range spec.ReadFirst {
		if path != "" {
			text.WriteString("- `" + path + "`\n")
		}
	}

	if len(spec.ReadFirst) > 0 || spec.Lane.ReadFirst != "" {
		text.WriteString("\n")
	}

	return text.String()
}

// itemLines returns every line that differs from one item to the next. They end the brief.
// The title, the model, the skeleton of the item, the read-first list, the acceptance lines and the names of the lane all sit here.
func (p *parts) itemLines(spec Spec) string {
	text := header(spec.Model) + p.fixed(PartItemSkeleton) + readFirst(spec)
	if spec.Kind != KindImpl {
		return text + p.fixed(PartLaneNames)
	}

	lane := spec.Lane
	if lane.Branch != "" {
		text += "**Base branch of your pull request: `" + lane.Branch + "`.** Branch from it and open the pull request against it, never against the default branch.\n\n"
	}

	return text + "## Acceptance of this item\n\n" +
		"- Acceptance script: `" + lane.Acceptance + "`\n" +
		"- Result at the base: `" + lane.BaseResult + "`\n" +
		"- Expected behavior: `" + cmp.Or(lane.Expected, "[EXPECTED]") + "`\n" +
		p.fixed(PartLaneNames)
}
