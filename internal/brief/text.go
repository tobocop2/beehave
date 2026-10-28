package brief

import (
	"embed"
	"fmt"
)

// texts holds the fixed text of a brief, one Markdown file for each part, read byte for byte.
//
//go:embed text/*.md
var texts embed.FS

// Part is one block of the fixed text of a brief. Its value is the name of the file under text/.
type Part string

// The parts of the fixed text.
const (
	PartSkeleton            Part = "skeleton"
	PartItemSkeleton        Part = "item_skeleton"
	PartLaneNames           Part = "lane_names"
	PartAcceptanceRules     Part = "acceptance_rules"
	PartDesignRules         Part = "design_rules"
	PartPapersRules         Part = "papers_rules"
	PartBeforeWriteRules    Part = "before_write_rules"
	PartCallSiteRules       Part = "call_site_rules"
	PartReviewRules         Part = "review_rules"
	PartLongJobsRules       Part = "long_jobs_rules"
	PartBuildHostRules      Part = "build_host_rules"
	PartHandoffFirstRules   Part = "handoff_first_rules"
	PartGateStepRules       Part = "gate_step_rules"
	PartEvidenceRules       Part = "evidence_rules"
	PartPrecedenceRules     Part = "precedence_rules"
	PartCompiledRules       Part = "compiled_rules"
	PartUnionRules          Part = "union_rules"
	PartProjectRulesHeading Part = "project_rules_heading"
	PartClosingRules        Part = "closing_rules"
)

// Parts lists every part of the fixed text.
func Parts() []Part {
	return []Part{
		PartSkeleton, PartItemSkeleton, PartLaneNames, PartAcceptanceRules, PartDesignRules, PartPapersRules,
		PartBeforeWriteRules, PartCallSiteRules, PartReviewRules, PartLongJobsRules, PartBuildHostRules,
		PartHandoffFirstRules, PartGateStepRules, PartEvidenceRules, PartPrecedenceRules, PartCompiledRules,
		PartUnionRules, PartProjectRulesHeading, PartClosingRules,
	}
}

// Text returns the text of a part, with its line endings as written.
func Text(part Part) (string, error) {
	data, err := texts.ReadFile("text/" + string(part) + ".md")
	if err != nil {
		return "", fmt.Errorf("no brief text named %s: %w", part, err)
	}

	return string(data), nil
}

// Part returns the part of the fixed text that a standing block adds.
func (b Block) Part() Part {
	switch b {
	case BlockBuildHost:
		return PartBuildHostRules
	case BlockCompiled:
		return PartCompiledRules
	case BlockUnion:
		return PartUnionRules
	}

	return ""
}
