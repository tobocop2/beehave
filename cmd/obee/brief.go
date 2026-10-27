package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/brief"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/ledger"
	"github.com/tobocop2/obee/internal/model"
)

// briefModel is the model that every brief of the workflow names.
const briefModel = "sonnet"

// The modes of the folder and the files of a brief.
const (
	dirMode  = 0o700
	fileMode = 0o600
)

// briefs writes the brief of an agent phase with brief.Generate.
// The rules file of a project is `rules/<project>.md` in the state folder. A brief is `briefs/<run>/<item>-<step>-<round>.md` beside its slots file.
type briefs struct {
	env map[string]string
	now func() time.Time
}

func newBriefs(env map[string]string) briefs {
	return briefs{env: env, now: time.Now}
}

// kindOf returns the kind of brief for a step. A step that runs no agent has none.
func kindOf(step model.Step) (brief.Kind, error) {
	switch step {
	case model.StepPlan:
		return brief.KindDesign, nil
	case model.StepImplement:
		return brief.KindImpl, nil
	case model.StepReview:
		return brief.KindRev, nil
	case model.StepClaim, model.StepClaimGate, model.StepGate, model.StepRound, model.StepRecord,
		model.StepOwnerWait, model.StepReady, model.StepIntegrate, model.StepRelease:
		return "", fmt.Errorf("the step %s runs no agent and has no brief", step)
	}

	return "", fmt.Errorf("the step %q is unknown and has no brief", step)
}

// slug turns a name into one folder or file name.
func slug(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}

		return '-'
	}, name)
}

// Write generates the brief and returns its path.
// A round after the first is a rework, and the brief of the round before it is the handoff. The engine carries the findings, so no handoff check judges it.
func (b briefs) Write(_ context.Context, request flow.BriefRequest) (string, error) {
	where, err := b.placeOf(request)
	if err != nil {
		return "", err
	}

	text, err := b.generate(where, request)
	if err != nil {
		return "", err
	}

	path := filepath.Join(where.dir, where.stem+".md")
	if err := writeText(path, text+carried(request)); err != nil {
		return "", err
	}

	return path, nil
}

func (b briefs) placeOf(request flow.BriefRequest) (place, error) {
	state, err := config.StateDir(b.env)
	if err != nil {
		return place{}, err
	}

	return place{
		state: state,
		dir:   filepath.Join(state, "briefs", slug(request.Run)),
		stem:  fmt.Sprintf("%s-%s-%d", slug(request.Item), request.Step, request.Round),
	}, nil
}

// place names where the files of one brief live: the state folder, the folder of the run and the name of the brief.
type place struct {
	state string
	dir   string
	stem  string
}

// prepare makes the folder of the brief and writes its slots file.
func (p place) prepare(request flow.BriefRequest) (string, error) {
	if err := os.MkdirAll(p.dir, dirMode); err != nil {
		return "", fmt.Errorf("make %s: %w", p.dir, err)
	}

	slots := filepath.Join(p.dir, p.stem+".slots")
	header := fmt.Sprintf("LANE=%s\nONE LINE SAYING WHAT AND WHY=the %s of item %s, round %d\n", p.stem, request.Step, request.Item, request.Round)

	return slots, writeText(slots, header)
}

// lane describes the lane of a brief. A round after the first is a rework, and the brief of the round before it is the handoff.
func (p place) lane(request flow.BriefRequest) brief.Lane {
	kind, handoff := brief.LaneFix, ""

	if request.Round > 0 {
		kind = brief.LaneRework
		handoff = filepath.Join(p.dir, fmt.Sprintf("%s-%s-%d.md", slug(request.Item), request.Step, request.Round-1))
	}

	return brief.Lane{
		Key: request.Item, Kind: kind, Mechanism: slug(request.Item), Handoff: handoff,
		Acceptance: "none: the gate of the project is the acceptance",
		BaseResult: "none: the gate of the project is the acceptance",
		// The remaining fields stay at their zero value: no new behavior, no transcript, no review, no union.
		NewBehavior: false, Transcript: "", Cap: 0, ReviewPR: "", Expected: "", Union: false, Branch: "", ReadFirst: "", Body: nil,
	}
}

func (b briefs) generate(p place, request flow.BriefRequest) (string, error) {
	kind, err := kindOf(request.Step)
	if err != nil {
		return "", err
	}

	slots, err := p.prepare(request)
	if err != nil {
		return "", err
	}

	generated, err := brief.Generate(brief.Request{
		Kind: kind, Name: p.stem, Model: briefModel, Lane: p.lane(request), At: b.now(),
		RulesPath: filepath.Join(p.state, "rules", slug(request.Project)+".md"), SlotsFile: slots,
	}, brief.Deps{Book: ledger.Open(p.state), Handoff: acceptHandoff})

	return generated.Text, err
}

// writeText writes a file of the brief.
func writeText(path, text string) error {
	if err := os.WriteFile(path, []byte(text), fileMode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

// acceptHandoff passes the handoff of a rework round: it is the brief of the round before.
func acceptHandoff(string, string, string, int) ([]string, bool, error) { return nil, false, nil }

// carried returns the open findings and the last answer of the owner, which the engine holds and the template has no slot for.
func carried(request flow.BriefRequest) string {
	var text strings.Builder

	if request.Findings != "" {
		text.WriteString("\n## Open findings\n\n" + request.Findings + "\n")
	}

	if request.Answer != "" {
		text.WriteString("\n## Last answer of the owner\n\n" + request.Answer + "\n")
	}

	return text.String()
}
