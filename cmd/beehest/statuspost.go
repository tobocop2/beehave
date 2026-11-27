package main

import (
	"context"
	"fmt"

	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/merge"
	"github.com/tobocop2/beehest/internal/model"
)

// noWriterNote is why no status was posted for a project with no writer role.
const noWriterNote = "no commit status was posted: the project has no `credentials` key, so the engine has no writer to post as"

// noStatusNote is why no status was posted for a forge that cannot post one.
const noStatusNote = "no commit status was posted: the forge of the project cannot post commit statuses"

// statusPost posts the statuses of one merge through the forge of the project, which runs as the writer role of a project that declares one.
type statusPost struct {
	project  model.Project
	forge    forge.Forge
	statuses []model.CommitStatus
}

// posting is the poster of a merge request, or nil when the merge posts no status.
func posting(project model.Project, built forge.Forge, statuses []model.CommitStatus) merge.Poster { //nolint:ireturn // the request holds the poster as its interface
	if len(statuses) == 0 {
		return nil
	}

	return statusPost{project: project, forge: built, statuses: statuses}
}

// Post sets each status as the writer role. A project with no writer role posts none, and neither does a forge that cannot post one: the answer says why.
func (s statusPost) Post(ctx context.Context) (string, error) {
	if !s.project.HasWriter() {
		return noWriterNote, nil
	}

	poster, ok := s.forge.(forge.Statuses)
	if !ok {
		return noStatusNote, nil
	}

	for _, status := range s.statuses {
		if err := poster.PostStatus(ctx, status); err != nil {
			return "", fmt.Errorf("post the status %s on %s: %w", status.Context, status.SHA, err)
		}
	}

	return "", nil
}
