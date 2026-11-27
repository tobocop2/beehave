package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/model"
)

// resumeAdapter is a lineAdapter whose CLI has a resume option, and whose transcript is <configDir>/<session>.jsonl.
type resumeAdapter struct{ lineAdapter }

func (resumeAdapter) Profile() agent.Profile {
	return agent.Profile{BudgetFlag: "--max-budget-usd", Resume: func(id string) []string { return []string{"--resume", id} }}
}

func (resumeAdapter) TranscriptPath(configDir, _, session string) string {
	return filepath.Join(configDir, session+".jsonl")
}

// noResumeAdapter is a resumeAdapter whose CLI has no resume option.
type noResumeAdapter struct{ resumeAdapter }

func (noResumeAdapter) Profile() agent.Profile { return agent.Profile{BudgetFlag: "--max-budget-usd"} }

// lostFixture is an agent fixture over an adapter, whose login folder is a temporary folder, and a request that follows the loss of session s-lost.
func lostFixture(t *testing.T, adapter agent.Adapter) (*agentFixture, flow.AgentRequest) {
	t.Helper()

	f := newAgentFixture("session=s-lost\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.runners[0].ConfigDir = t.TempDir()
	f.agents.adapter = func(model.Agent) agent.Adapter { return adapter }

	req := request()
	req.Resume = ""
	req.Lost = &flow.LostRun{Session: "s-lost", Note: "This step ran before. 1 file changed", Continue: "Continue the step."}

	return f, req
}

func TestSessionTap_NamesEachNewSessionOnceAtTheLineThatHoldsIt(t *testing.T) {
	t.Parallel()

	var named []string

	tap := newSessionTap(lineAdapter{}, agent.DefaultPrices(), func(session string) { named = append(named, session) })
	write := func(lines string) {
		_, err := tap.Write([]byte(lines))
		require.NoError(t, err)
	}

	write("noise\n")
	assert.Empty(t, named)

	write("session=s1\n")
	assert.Equal(t, []string{"s1"}, named, "the id is named at the line that holds it, before the run ends")

	write("text=working\nsession=s1\n")
	assert.Equal(t, []string{"s1"}, named, "an id that repeats is named once")

	write("session=s2\nresult=end\n")
	assert.Equal(t, []string{"s1", "s2"}, named)
}

func TestSessionTap_WithNoCallbackKeepsTheSession(t *testing.T) {
	t.Parallel()

	tap := newSessionTap(lineAdapter{}, agent.DefaultPrices(), nil)
	_, err := tap.Write([]byte("session=s1\n"))
	require.NoError(t, err)

	session, _ := tap.found()
	assert.Equal(t, "s1", session)
}

func TestAgents_ALostStepWithATranscriptResumesTheSession(t *testing.T) {
	t.Parallel()

	f, req := lostFixture(t, resumeAdapter{})
	require.NoError(t, os.WriteFile(filepath.Join(f.agents.runners[0].ConfigDir, "s-lost.jsonl"), []byte("{}\n"), 0o600))

	var named []string

	req.Named = func(session string) { named = append(named, session) }

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, "s-lost", f.spec.Resume, "the saved session is resumed")
	assert.Equal(t, "Continue the step.", f.spec.Prompt, "a resumed session gets the short prompt: its brief is in the transcript")
	assert.True(t, got.Resumed)
	assert.Equal(t, []string{"s-lost"}, named, "the run names its session through the request")
}

func TestAgents_TheSavedSessionWinsOverTheResumeKeyword(t *testing.T) {
	t.Parallel()

	f, req := lostFixture(t, resumeAdapter{})
	require.NoError(t, os.WriteFile(filepath.Join(f.agents.runners[0].ConfigDir, "s-lost.jsonl"), []byte("{}\n"), 0o600))

	req.Resume = "old"

	_, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, "s-lost", f.spec.Resume)
}

func TestAgents_ALostStepWithNoTranscriptRunsFreshWithTheNoteAndTheDiffSummary(t *testing.T) {
	t.Parallel()

	f, req := lostFixture(t, resumeAdapter{})

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Empty(t, f.spec.Resume, "a session with no transcript file cannot resume")
	assert.Equal(t, "do it\n\nThis step ran before. 1 file changed", f.spec.Prompt, "the fresh run gets the whole prompt, the note and the diff summary")
	assert.False(t, got.Resumed)
}

func TestAgents_ALostStepOnACLIWithNoResumeOptionRunsFreshWithTheNote(t *testing.T) {
	t.Parallel()

	f, req := lostFixture(t, noResumeAdapter{})
	// The transcript exists, but the CLI has no option to resume it.
	require.NoError(t, os.WriteFile(filepath.Join(f.agents.runners[0].ConfigDir, "s-lost.jsonl"), []byte("{}\n"), 0o600))

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Empty(t, f.spec.Resume)
	assert.Equal(t, "do it\n\nThis step ran before. 1 file changed", f.spec.Prompt)
	assert.False(t, got.Resumed)
}

// resumeHomeAdapter is a resumeAdapter whose CLI keeps its config in the folder .cfg of the home folder when the runner sets none.
type resumeHomeAdapter struct{ resumeAdapter }

func (resumeHomeAdapter) Profile() agent.Profile {
	profile := resumeAdapter{}.Profile()
	profile.ConfigHome = ".cfg"

	return profile
}

func TestAgents_ALostStepOfARunnerWithNoConfigFolderResumesFromTheTranscriptInTheDefaultFolder(t *testing.T) {
	t.Parallel()

	f, req := lostFixture(t, resumeHomeAdapter{})
	home := t.TempDir()
	f.agents.home = func() (string, error) { return home, nil }
	f.agents.runners[0].ConfigDir = ""

	transcript := filepath.Join(home, ".cfg", "s-lost.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(transcript), 0o700))
	require.NoError(t, os.WriteFile(transcript, []byte("{}\n"), 0o600))

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.True(t, got.Resumed, "the transcript is in the default folder under the home folder")
	assert.Equal(t, "s-lost", f.spec.Resume)
	assert.Equal(t, transcript, got.Transcript, "the result names the transcript in the default folder, not a relative path")
}

func TestAgents_ARunnerWithAConfigFolderIgnoresTheDefaultFolderForTheTranscript(t *testing.T) {
	t.Parallel()

	f, req := lostFixture(t, resumeHomeAdapter{})
	home := t.TempDir()
	f.agents.home = func() (string, error) { return home, nil }

	// Only the default folder holds the transcript: the config folder of the runner wins, so the session does not resume.
	transcript := filepath.Join(home, ".cfg", "s-lost.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(transcript), 0o700))
	require.NoError(t, os.WriteFile(transcript, []byte("{}\n"), 0o600))

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.False(t, got.Resumed)
	assert.Equal(t, filepath.Join(f.agents.runners[0].ConfigDir, "s-lost.jsonl"), got.Transcript)
}

func TestAgents_ARunnerWithNoConfigFolderAndNoKnownHomeFolderDoesNotStart(t *testing.T) {
	t.Parallel()

	f, req := lostFixture(t, resumeHomeAdapter{})
	f.agents.home = func() (string, error) { return "", os.ErrNotExist }
	f.agents.runners[0].ConfigDir = ""

	got, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "config_dir", "the note names the fix")
	assert.Empty(t, f.spec.Prompt, "the agent never started")
}

func TestAgents_ConfigFolderOfACLIWithNoDefaultFolderNeedsNoHomeFolder(t *testing.T) {
	t.Parallel()

	noHome := agents{home: func() (string, error) { return "", os.ErrNotExist }, adapter: func(model.Agent) agent.Adapter { return resumeAdapter{} }}
	runner := model.Runner{Name: "main", Agent: model.Agent("fake")}
	folder, note := noHome.configFolder(runner, resumeAdapter{})

	assert.Empty(t, folder)
	assert.Empty(t, note)

	folder, note = noHome.configFolder(runner, nil)

	assert.Empty(t, folder, "an agent with no adapter has no default folder")
	assert.Empty(t, note, "its run is refused where the adapter is missing, not here")
}
