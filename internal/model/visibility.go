package model

import (
	"errors"
	"fmt"
)

// Visibility is whether a repository is open to the public. The empty value means unknown, and unknown counts as public.
type Visibility string

// The visibilities that a project file names.
const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

// ErrUnknownVisibility is returned when text names no visibility.
var ErrUnknownVisibility = errors.New("unknown visibility")

// Visibilities lists every visibility that a project file names.
func Visibilities() []Visibility {
	return []Visibility{VisibilityPublic, VisibilityPrivate}
}

// ParseVisibility returns the visibility that text names.
func ParseVisibility(text string) (Visibility, error) {
	for _, name := range Visibilities() {
		if string(name) == text {
			return name, nil
		}
	}

	return "", fmt.Errorf("%w %q: use one of %s", ErrUnknownVisibility, text, joinNames(Visibilities()))
}

// IsPrivate is true only when the project file says the repository is private. A project that says nothing is treated as public.
func (p Project) IsPrivate() bool { return p.Visibility == VisibilityPrivate }
