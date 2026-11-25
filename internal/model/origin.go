package model

import (
	"errors"
	"fmt"
	"slices"
)

// Association is how the author of an issue or a pull request stands to its repository, as the forge reports it.
type Association string

// The associations that the forge reports. Only AssociationOwner is the owner.
const (
	AssociationOwner                Association = "OWNER"
	AssociationMember               Association = "MEMBER"
	AssociationCollaborator         Association = "COLLABORATOR"
	AssociationContributor          Association = "CONTRIBUTOR"
	AssociationFirstTimeContributor Association = "FIRST_TIME_CONTRIBUTOR"
	AssociationFirstTimer           Association = "FIRST_TIMER"
	AssociationMannequin            Association = "MANNEQUIN"
	AssociationNone                 Association = "NONE"
)

// ErrUnknownAssociation is returned for text that names no association that the forge reports.
var ErrUnknownAssociation = errors.New("unknown author association")

// ParseAssociation returns the association that text names.
func ParseAssociation(text string) (Association, error) {
	all := []Association{
		AssociationOwner, AssociationMember, AssociationCollaborator, AssociationContributor,
		AssociationFirstTimeContributor, AssociationFirstTimer, AssociationMannequin, AssociationNone,
	}
	if found := Association(text); slices.Contains(all, found) {
		return found, nil
	}

	return "", fmt.Errorf("%w %q", ErrUnknownAssociation, text)
}

// Foreign reports whether another account than the owner wrote it. An empty association counts as foreign.
func (a Association) Foreign() bool { return a != AssociationOwner }

// Authored is one issue or pull request of an item and the association of its author. Number 0 is an item with no such thing.
type Authored struct {
	Number      int
	Association Association
}

// Foreign reports whether another account than the owner wrote it.
func (a Authored) Foreign() bool { return a.Number != 0 && a.Association.Foreign() }

// Authored is the pull request of the facts with the association of its author. Facts with number 0 have no pull request.
func (f PullFacts) Authored() Authored { return Authored{Number: f.Number, Association: f.Association} }

// Origin is who wrote an item: its issue on the forge, and its pull request.
type Origin struct {
	Issue Authored
	Pull  Authored
}
