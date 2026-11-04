package flow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/atomicfile"
	"github.com/tobocop2/obee/internal/review"
)

const (
	pollName         = "review_poll"
	pollScheduleName = "review-poll"
	pollStepPrefix   = "poll:"
	// pollProjectsStep records the project list, so a replay loops over the list of the first run.
	pollProjectsStep = pollStepPrefix + "projects"
	// pollScheduleFileName holds the cause when the last launch could not set the poll schedule. It is empty when the launch set it.
	pollScheduleFileName = "poll-schedule-error"
	// pollOff is the status text when the interval is 0.
	pollOff = "agents.review_poll_minutes is 0, so no review poll runs. Set it to a number of minutes to turn the poll on."
)

// ErrNoPollSource is returned by the poll reader for a project that has no forge. The poll skips that project.
var ErrNoPollSource = errors.New("the project has no forge to poll")

// PollSource is the forge reads of one review poll. It has the read methods of the poll and no write method, so the poll cannot change the forge.
type PollSource interface {
	review.Reader
	review.Own
}

// pollReader finds what the review poll reads. Projects lists the names of the configured projects.
// Source returns the forge of a project and the login of the account that polls. ErrNoPollSource means the project has no forge.
type pollReader interface {
	Projects() []string
	Source(ctx context.Context, project string) (PollSource, string, error)
}

// PollProject is the result of one project in a poll. Unread is set when the poll could not read the project: then Rows is empty, and the text says why.
// NoForge is set for a project with no forge, which the poll does not read.
type PollProject struct {
	Project string       `json:"project"`
	Rows    []review.Row `json:"rows,omitempty"`
	Unread  string       `json:"unread,omitempty"`
	NoForge bool         `json:"no_forge,omitempty"`
}

// PollOutput is what one tick of the review poll recorded. At is the time of the tick.
// Failure is set only by Client.Poll, for a run that failed as a whole: then At is the time the run ended and Projects is empty.
type PollOutput struct {
	At       time.Time     `json:"at"`
	Projects []PollProject `json:"projects"`
	Failure  string        `json:"failure,omitempty"`
}

// poll is the scheduled workflow of the review poll. It records the project list in its first step, then reads each project in one recorded step. It writes nothing to a forge.
// An error of the forge read makes that project Unread. An error of DBOS (a step that does not match on replay, a canceled workflow) is the error of the poll.
func (e *Engine) poll(ctx dbos.Context, in dbos.ScheduledWorkflowInput) (PollOutput, error) {
	out := PollOutput{At: in.ScheduledTime.UTC(), Projects: []PollProject{}}

	if e.deps.Poll == nil {
		return out, nil
	}

	names, err := runStep(e, ctx, pollProjectsStep, func(context.Context) ([]string, error) {
		return e.deps.Poll.Projects(), nil
	})
	if err == nil {
		out.Projects, err = e.pollEach(ctx, names, in.ScheduledTime)
	}

	return out, err
}

// pollEach runs the step of each project, in order.
func (e *Engine) pollEach(ctx dbos.Context, names []string, now time.Time) ([]PollProject, error) {
	projects := make([]PollProject, 0, len(names))

	for _, project := range names {
		got, err := e.pollStep(ctx, project, now)
		if err != nil {
			return nil, err
		}

		projects = append(projects, got)
	}

	return projects, nil
}

// pollStep runs the step of one project. A forge error becomes an Unread result. An error of DBOS is returned.
func (e *Engine) pollStep(ctx dbos.Context, project string, now time.Time) (PollProject, error) {
	got, err := runStep(e, ctx, pollStepPrefix+project, func(c context.Context) (PollProject, error) {
		return e.pollProject(c, project, now)
	})

	var engineErr *dbos.Error
	if errors.As(err, &engineErr) {
		return PollProject{}, err
	}

	if err != nil {
		return PollProject{Project: project, Unread: fmt.Sprintf("could not read %s: %s", project, err)}, nil
	}

	return got, nil
}

// pollProject runs the poll of one project. A read that failed is the error of the step, so the record keeps the cause.
// A project with no forge returns a result with NoForge set.
func (e *Engine) pollProject(ctx context.Context, project string, now time.Time) (PollProject, error) {
	source, me, err := e.deps.Poll.Source(ctx, project)
	if errors.Is(err, ErrNoPollSource) {
		return PollProject{Project: project, NoForge: true}, nil
	}

	if err != nil {
		return PollProject{}, err
	}

	rows, err := review.Poll(ctx, source, source, me, now)
	if err != nil {
		return PollProject{}, err
	}

	return PollProject{Project: project, Rows: rows}, nil
}

// pollScheduleFile returns the path of the record of the poll schedule in a state folder.
func pollScheduleFile(stateDir string) string {
	return filepath.Join(stateDir, pollScheduleFileName)
}

// recordSchedule keeps the outcome of the schedule at launch for obee status, which runs in another process.
// A failure is logged and the engine goes on: the poll is optional. A nil err records that the poll is scheduled.
func (e *Engine) recordSchedule(err error) {
	var text string

	if err != nil {
		text = e.redact(err.Error())
		e.logger.Warn("the review poll is not scheduled", "error", text)
	}

	if werr := atomicfile.Write(pollScheduleFile(e.dir), []byte(text), ownerMode); werr != nil {
		e.logger.Warn("could not record the state of the review poll schedule", "error", e.redact(werr.Error()))
	}
}

// schedulePoll makes the schedule of the review poll when the interval is set and the build can read, and deletes it otherwise.
// ApplySchedules replaces a schedule of the same name, so a restart or a changed interval needs no other step.
func (e *Engine) schedulePoll() error {
	if e.reviewPoll <= 0 || e.deps.Poll == nil {
		return dbos.DeleteSchedule(e.ctx, pollScheduleName)
	}

	spec := dbos.ScheduleSpec{ScheduleName: pollScheduleName, Schedule: fmt.Sprintf("@every %s", e.reviewPoll), WorkflowName: pollName}

	return dbos.ApplySchedules(e.ctx, []dbos.ScheduleSpec{spec})
}
