package flow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plannerPause is the time that the fake sleep program takes, so the planner asks again after the workers had time to publish.
const plannerPause = 20 * time.Millisecond

// plannerRig runs examples/planner/workflow.star, copied with its prompts, over the fake collaborators. The fake agent of a worker holds until the planner has
// reported what it read, so the read happens while the workers run.
func plannerRig(t *testing.T) (*agentRig, string) {
	t.Helper()

	folder := filepath.Join(t.TempDir(), "planner")
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "planner"))))

	rig := newAgentRig(t)
	rig.reply("", costing(1.5))
	rig.h.opts.Env = map[string]string{"TASKS": "21 22"}
	rig.h.agents.gate = make(chan struct{})

	var once sync.Once

	rig.h.commands.before = func(ctx context.Context, request CommandRequest) error {
		switch {
		case request.Run[0] == "sleep":
			time.Sleep(plannerPause)
		case request.Run[0] == "echo" && strings.Contains(request.Run[1], "while the workers run"):
			once.Do(func() { close(rig.h.agents.gate) })
		}

		return ctx.Err()
	}

	return rig, filepath.Join(folder, "workflow.star")
}

func TestExamplePlanner_ThePlannerReadsTheProgressOfTheWorkersWhileTheyRun(t *testing.T) {
	t.Parallel()

	rig, file := plannerRig(t)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)

	ran := rig.h.commands.ranBy(testItem)
	require.NotEmpty(t, ran)
	assert.Contains(t, ran, "echo while the workers run: 21=started 22=started", "no agent had ended when the planner read, and each worker had published")
	assert.Equal(t, "echo when the workers ended: 21=done 22=done cost_usd=3.0", ran[len(ran)-1], "each worker spent 1.5 and the planner adds the cost of its results")
}

func TestExamplePlanner_AWorkerThatPublishedLastShowsItsValueToStatus(t *testing.T) {
	t.Parallel()

	rig, file := plannerRig(t)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)
	require.NoError(t, err)

	got, err := rig.h.client(t, testVersion).Published("item-22-r1")

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "progress", got[0].Key)
	assert.JSONEq(t, `{"stage":"done","cost":1.5}`, string(got[0].Value))
}
