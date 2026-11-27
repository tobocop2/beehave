package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehest/internal/atomicfile"
	"github.com/tobocop2/beehest/internal/model"
)

const (
	ownersDir  = "owners"
	ownersMode = 0o700
	ownerMode  = 0o600
	// ownersLockName names the lock that every process holds while it writes a token file or a cancel marker, or compares a token file or a cancel marker with a token and removes it.
	ownersLockName = "owners.lock"
)

// errNoOwner is returned when a run recorded no claim.
var errNoOwner = errors.New("the run recorded no claim")

// ownerPath is the file that holds the token of the claim that beehest resume took for a run.
func ownerPath(dir, id string) string {
	return filepath.Join(dir, ownersDir, runFile(id))
}

// underOwners runs f while it holds the owners lock, so a compare and the removal that follows it are one step for every process.
func underOwners(dir string, f func() error) error {
	held := lockFile(dir, ownersLockName)
	if err := held.Lock(); err != nil {
		return fmt.Errorf("lock %s: %w", ownersLockName, err)
	}

	return errors.Join(f(), held.Unlock())
}

// saveOwner records the token of a fresh claim for a run. The run replays its recorded claim step, whose token was released, so the fresh token wins.
func saveOwner(dir, id, owner string) error {
	err := underOwners(dir, func() error {
		if err := os.MkdirAll(filepath.Join(dir, ownersDir), ownersMode); err != nil {
			return err
		}

		return atomicfile.Write(ownerPath(dir, id), []byte(owner), ownerMode)
	})
	if err != nil {
		return fmt.Errorf("save the claim of run %s: %w", id, err)
	}

	return nil
}

// removeOwnedToken removes the token file of a run when it holds owner. A token that a later resume saved stays. The caller holds the owners lock.
func removeOwnedToken(dir, id, owner string) error {
	saved, err := savedOwner(dir, id)
	if err != nil || saved != owner {
		return err
	}

	return removeFile(ownerPath(dir, id))
}

// dropOwner removes the token file of a run when it still holds owner.
func dropOwner(dir, id, owner string) error {
	return underOwners(dir, func() error { return removeOwnedToken(dir, id, owner) })
}

// releaseOwner returns the token that the give-back of an attempt releases: the token of its claim, else the one of the recorded claim step.
// It never reads the token file, which belongs to the attempt that beehest resume made last.
func releaseOwner(client dbos.Client, a *attempt) (string, error) {
	if a.owner != "" {
		return a.owner, nil
	}

	recorded, err := recordedClaim(client, a.id)
	if err == nil && recorded == "" {
		err = fmt.Errorf("%w: %s", errNoOwner, a.id)
	}

	return recorded, err
}

// savedOwner returns the token that beehest resume saved for a run, or an empty text when it saved none.
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

// savedOwnerAge returns when beehest resume saved the token of a run, and false when it saved none.
func savedOwnerAge(dir, id string) (time.Time, bool, error) {
	info, err := os.Stat(ownerPath(dir, id))
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, false, nil
	}

	if err != nil {
		return time.Time{}, false, fmt.Errorf("read the claim of run %s: %w", id, err)
	}

	return info.ModTime(), true, nil
}

// currentOwner returns the token of the claim a run holds: the one that beehest resume saved, else the one of the recorded claim step.
func currentOwner(client dbos.Client, dir, id string) (string, error) {
	saved, err := savedOwner(dir, id)
	if err != nil || saved != "" {
		return saved, err
	}

	return releaseOwner(client, &attempt{id: id})
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
