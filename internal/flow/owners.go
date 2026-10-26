package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/model"
)

const (
	ownersDir  = "owners"
	ownersMode = 0o700
	ownerMode  = 0o600
)

// errNoOwner is returned when a run recorded no claim.
var errNoOwner = errors.New("the run recorded no claim")

// ownerPath is the file that holds the token of the claim that obee resume took for a run.
func ownerPath(dir, id string) string {
	return filepath.Join(dir, ownersDir, id)
}

// saveOwner records the token of a fresh claim for a run. The run replays its recorded claim step, whose token was released, so the fresh token wins.
func saveOwner(dir, id, owner string) error {
	if err := os.MkdirAll(filepath.Join(dir, ownersDir), ownersMode); err != nil {
		return fmt.Errorf("save the claim of run %s: %w", id, err)
	}

	if err := os.WriteFile(ownerPath(dir, id), []byte(owner), ownerMode); err != nil {
		return fmt.Errorf("save the claim of run %s: %w", id, err)
	}

	return nil
}

// savedOwner returns the token that obee resume saved for a run, or an empty text when it saved none.
func savedOwner(dir, id string) (string, error) {
	data, err := os.ReadFile(ownerPath(dir, id))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("read the claim of run %s: %w", id, err)
	}

	return string(data), nil
}

// currentOwner returns the token of the claim a run holds: the one that obee resume saved, else the one of the recorded claim step.
func currentOwner(client dbos.Client, dir, id string) (string, error) {
	saved, err := savedOwner(dir, id)
	if err != nil || saved != "" {
		return saved, err
	}

	recorded, err := recordedClaim(client, id)
	if err == nil && recorded == "" {
		err = fmt.Errorf("%w: %s", errNoOwner, id)
	}

	return recorded, err
}

// recordedClaim returns the token that the claim step of a run recorded, or an empty text when that step has no result: it did not run or it failed.
func recordedClaim(client dbos.Client, id string) (string, error) {
	steps, err := dbos.GetWorkflowSteps(client, id, dbos.WithStepsLoadOutput(true))
	if err != nil {
		return "", fmt.Errorf("read the steps of %s: %w", id, err)
	}

	for _, step := range steps {
		if owner := claimOutput(step); owner != "" {
			return owner, nil
		}
	}

	return "", nil
}

// claimOutput returns the owner token that a claim step returned, or an empty text for any other step and for a claim that failed.
// The store keeps the output of a step as JSON text.
func claimOutput(step dbos.StepInfo) string {
	text, ok := step.Output.(string)
	if !ok || step.Error != nil || step.StepName != string(model.StepClaim) {
		return ""
	}

	var owner string
	if json.Unmarshal([]byte(text), &owner) != nil {
		return ""
	}

	return owner
}

// workflowID returns the id of the run in ctx, or an empty text outside a workflow.
func workflowID(ctx dbos.Context) string {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return ""
	}

	return id
}
