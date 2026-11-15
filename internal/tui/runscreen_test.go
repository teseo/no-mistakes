package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thellmwhisperer/runscreen"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// coreRun is a running run with every core step pending.
func coreRun() *ipc.RunInfo {
	run := &ipc.RunInfo{ID: "run-1", RepoID: "repo-1", Branch: "feature/rs", HeadSHA: "abc12345def", Status: types.RunRunning}
	for _, name := range types.AllSteps() {
		run.Steps = append(run.Steps, ipc.StepResultInfo{RunID: run.ID, StepName: name, StepOrder: name.Order(), Status: types.StepStatusPending})
	}
	return run
}

func sizedScreen(run *ipc.RunInfo) *screen {
	s := newScreen(NewModel("", nil, run))
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	return s
}

func screenFrame(s *screen) string { return plainFrame(s.View()) }

func sendEvent(s *screen, event ipc.Event) {
	s.Update(eventMsg{event: event, subscriptionID: s.m.subscriptionID})
}

func logChunk(run string, step types.StepName, content string) ipc.Event {
	return ipc.Event{Type: ipc.EventLogChunk, RunID: run, StepName: &step, Content: &content}
}

func stepStarted(run string, step types.StepName) ipc.Event {
	return ipc.Event{Type: ipc.EventStepStarted, RunID: run, StepName: &step}
}

func stepCompleted(run string, step types.StepName, status types.StepStatus, ms int64, findings string) ipc.Event {
	s := string(status)
	e := ipc.Event{Type: ipc.EventStepCompleted, RunID: run, StepName: &step, Status: &s, DurationMS: &ms}
	if findings != "" {
		e.Findings = &findings
	}
	return e
}

func key(k string) tea.KeyMsg {
	switch k {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

const twoFindings = `{"findings":[` +
	`{"id":"f1","severity":"error","file":"a.go","line":3,"description":"first finding","action":"auto-fix"},` +
	`{"id":"f2","severity":"warning","description":"second finding","action":"ask-user"}],"summary":"two"}`

func TestScreen_PipelineBoxListsEveryStepWithItsStateAndTime(t *testing.T) {
	run := coreRun()
	started := time.Now().Add(-time.Minute).Unix()
	for i := range run.Steps {
		st := &run.Steps[i]
		switch st.StepName {
		case types.StepIntent:
			st.Status, st.DurationMS = types.StepStatusSkipped, ptr(int64(40))
		case types.StepRebase:
			st.Status, st.StartedAt, st.DurationMS = types.StepStatusCompleted, &started, ptr(int64(1500))
		case types.StepReview:
			st.Status, st.DurationMS = types.StepStatusFixReview, ptr(int64(65000))
		}
	}
	frame := screenFrame(sizedScreen(run))
	for _, want := range [][]string{
		{"feature/rs", "⏸ waiting"},
		{"Intent", "0.0 s", "skipped"},
		{"Rebase", "1.5 s", "passed"},
		{"Review", "1 min 05 s", "fix review", "waiting"},
		{"Test", "pending"}, {"Document", "pending"}, {"Lint", "pending"},
		{"Push", "pending"}, {"PR", "pending"}, {"CI", "pending"},
	} {
		if !hasLineContainingAll(frame, want...) {
			t.Errorf("no line has %q:\n%s", want, frame)
		}
	}
}

func TestScreen_RunningStepCountsItsTimeFromItsStart(t *testing.T) {
	run := coreRun()
	s := sizedScreen(run)
	sendEvent(s, stepStarted(run.ID, types.StepIntent))
	sendEvent(s, stepCompleted(run.ID, types.StepIntent, types.StepStatusFixing, 0, ""))
	row := s.step(s.m.steps[0])
	if row.State != runscreen.Running || row.Note != "fixing" || row.Start.IsZero() || !row.End.IsZero() {
		t.Fatalf("a fixing step = %+v, want running from its start, noted fixing", row)
	}
	if !hasLineContainingAll(screenFrame(s), "Intent", "fixing", "running") {
		t.Errorf("the fixing step's row is missing:\n%s", screenFrame(s))
	}
}

func TestScreen_StepWithADurationButNoStartStillShowsItsTime(t *testing.T) {
	run := coreRun()
	run.Steps[0].Status, run.Steps[0].DurationMS = types.StepStatusCompleted, ptr(int64(2300))
	s := sizedScreen(run)
	row := s.step(s.m.steps[0])
	if got := row.End.Sub(row.Start); got != 2300*time.Millisecond {
		t.Fatalf("the step's time = %v, want 2.3s", got)
	}
}

func TestScreen_AgentBoxShowsTheRunningStepsLogAndEmptiesForTheNext(t *testing.T) {
	run := coreRun()
	s := sizedScreen(run)
	sendEvent(s, stepStarted(run.ID, types.StepReview))
	sendEvent(s, logChunk(run.ID, types.StepReview, "reviewing changes...\n\nclaude started"))
	frame := screenFrame(s)
	if !hasLineContainingAll(frame, "Agent") || !strings.Contains(frame, "reviewing changes...") {
		t.Fatalf("the Agent box does not show the review's log:\n%s", frame)
	}
	if strings.Contains(frame, "claude started") {
		t.Fatalf("a line still waiting for its newline was drawn:\n%s", frame)
	}
	sendEvent(s, logChunk(run.ID, types.StepReview, " pid=7\n"))
	if !strings.Contains(screenFrame(s), "claude started pid=7") {
		t.Fatalf("the line completed by the next chunk is missing:\n%s", screenFrame(s))
	}

	sendEvent(s, stepStarted(run.ID, types.StepTest))
	frame = screenFrame(s)
	if strings.Contains(frame, "reviewing changes...") || strings.Contains(frame, "Agent") {
		t.Fatalf("the Agent box kept the review's log once the test started:\n%s", frame)
	}
	sendEvent(s, logChunk(run.ID, types.StepTest, "running tests\n"))
	if !strings.Contains(screenFrame(s), "running tests") {
		t.Fatalf("the Agent box does not show the test's log:\n%s", screenFrame(s))
	}
}

func TestScreen_StepEndFlushesTheLogsLastLine(t *testing.T) {
	run := coreRun()
	s := sizedScreen(run)
	sendEvent(s, stepStarted(run.ID, types.StepLint))
	sendEvent(s, logChunk(run.ID, types.StepLint, "lint: 0 unresolved items"))
	sendEvent(s, stepCompleted(run.ID, types.StepLint, types.StepStatusCompleted, 10, ""))
	if !strings.Contains(screenFrame(s), "lint: 0 unresolved items") {
		t.Fatalf("the last line of the log was lost at the step's end:\n%s", screenFrame(s))
	}
}

func TestScreen_AnotherRunsLogEmptiesTheAgentBox(t *testing.T) {
	run := coreRun()
	s := sizedScreen(run)
	sendEvent(s, logChunk(run.ID, types.StepIntent, "first run's intent\n"))
	sendEvent(s, logChunk("run-2", types.StepIntent, "rerun's intent\n"))
	frame := screenFrame(s)
	if strings.Contains(frame, "first run's intent") || !strings.Contains(frame, "rerun's intent") {
		t.Fatalf("the Agent box mixed two runs' logs:\n%s", frame)
	}
}

func TestScreen_StaleStepStartLeavesTheAgentBox(t *testing.T) {
	run := coreRun()
	s := sizedScreen(run)
	fresh := stepStarted(run.ID, types.StepReview)
	fresh.StateRev = 5
	sendEvent(s, fresh)
	sendEvent(s, logChunk(run.ID, types.StepReview, "reviewing changes...\n"))
	stale := stepStarted(run.ID, types.StepRebase)
	stale.StateRev = 4
	sendEvent(s, stale)
	if !strings.Contains(screenFrame(s), "reviewing changes...") {
		t.Fatalf("a stale step start emptied the Agent box:\n%s", screenFrame(s))
	}
}

func TestScreen_FindingsBoxFollowsTheGatesSelectionAndCursor(t *testing.T) {
	run := coreRun()
	s := sizedScreen(run)
	sendEvent(s, stepStarted(run.ID, types.StepReview))
	sendEvent(s, stepCompleted(run.ID, types.StepReview, types.StepStatusAwaitingApproval, 1000, twoFindings))
	frame := screenFrame(s)
	for _, want := range [][]string{
		{"Findings · Review · E 1 · W 1"},
		{"› [x] E first finding"},
		{"a.go:3"},
		{"action: auto-fix"},
		{"[x] W second finding"},
		{"Review", "approval", "waiting"},
		{"Review awaiting action:"},
	} {
		if !hasLineContainingAll(frame, want...) {
			t.Errorf("no line has %q:\n%s", want, frame)
		}
	}

	// Space is the attach Model's: it deselects the cursor's finding and moves
	// the cursor on.
	s.Update(key(" "))
	frame = screenFrame(s)
	if !hasLineContainingAll(frame, "[ ] E first finding") || !hasLineContainingAll(frame, "› [x] W second finding") {
		t.Fatalf("the findings box does not follow the selection and the cursor:\n%s", frame)
	}
	if strings.Contains(frame, "a.go:3") {
		t.Fatalf("the row the cursor left is still open:\n%s", frame)
	}
}

func TestScreen_FindingsOfTheLoggedStepShowWithoutAGate(t *testing.T) {
	run := coreRun()
	s := sizedScreen(run)
	sendEvent(s, stepStarted(run.ID, types.StepReview))
	sendEvent(s, stepCompleted(run.ID, types.StepReview, types.StepStatusFixing, 1000, twoFindings))
	frame := screenFrame(s)
	if !hasLineContainingAll(frame, "E first finding") || strings.Contains(frame, "[x]") || strings.Contains(frame, "›") {
		t.Fatalf("a fixing step's findings should show without the gate's marks:\n%s", frame)
	}
}

func TestScreen_FindingNoteShowsUnderItsRow(t *testing.T) {
	run := coreRun()
	s := sizedScreen(run)
	sendEvent(s, stepCompleted(run.ID, types.StepReview, types.StepStatusAwaitingApproval, 1000, twoFindings))
	s.m.findingInstructions[types.StepReview] = map[string]string{"f1": "keep the guard"}
	s.sync()
	if !strings.Contains(screenFrame(s), "note: keep the guard") {
		t.Fatalf("the finding's note is missing:\n%s", screenFrame(s))
	}
}

func TestScreen_ErrorShowsInABox(t *testing.T) {
	s := sizedScreen(coreRun())
	s.Update(errMsg{err: errors.New("respond: daemon went away\nretry later")})
	frame := screenFrame(s)
	if !hasLineContainingAll(frame, "Error") || !strings.Contains(frame, "respond: daemon went away") || !strings.Contains(frame, "retry later") {
		t.Fatalf("the error box is missing:\n%s", frame)
	}
}

func TestScreen_RunscreensKeysStayOffTheAttachModel(t *testing.T) {
	s := sizedScreen(coreRun())
	sendEvent(s, logChunk("run-1", types.StepReview, "reviewing changes...\n"))
	before := s.m
	for _, k := range []string{"t", "h", "i", "tab"} {
		if _, cmd := s.Update(key(k)); cmd != nil {
			t.Errorf("runscreen's key %q returned a command", k)
		}
	}
	s.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	s.Update(tea.MouseMsg{X: 60, Y: 5, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if s.m.showHelp != before.showHelp || s.m.showDiff != before.showDiff || s.m.yoloMode != before.yoloMode {
		t.Fatal("runscreen's keys changed the attach Model")
	}
	if !strings.Contains(screenFrame(s), "hide tool use") {
		t.Fatalf("t did not reach runscreen:\n%s", screenFrame(s))
	}
}

func TestScreen_HelpAndOtherFullScreenViewsAreTheAttachModels(t *testing.T) {
	s := sizedScreen(coreRun())
	s.Update(key("?"))
	if !s.m.showHelp || s.View() != s.m.View() {
		t.Fatal("? should draw the attach Model's help")
	}
	// While the help shows, every key is the attach Model's, the mouse is
	// ignored, and t closes the help, as any key does, instead of folding
	// runscreen's tool calls.
	s.Update(tea.MouseMsg{X: 60, Y: 5, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	s.Update(key("t"))
	if s.m.showHelp || strings.Contains(screenFrame(s), "hide tool use") {
		t.Fatalf("t reached runscreen while the help showed:\n%s", screenFrame(s))
	}
}

func TestScreen_RunscreensKeyTakesBackAFirstAbort(t *testing.T) {
	s := sizedScreen(coreRun())
	s.Update(key("x"))
	if !s.m.confirmAbort {
		t.Fatal("x did not ask to confirm the abort")
	}
	s.Update(key("tab"))
	if s.m.confirmAbort {
		t.Fatal("a key other than x left the abort armed")
	}
}

func TestScreen_QuitClearsTheScreen(t *testing.T) {
	s := sizedScreen(coreRun())
	_, cmd := s.Update(key("q"))
	if cmd == nil || s.View() != "" {
		t.Fatal("q should leave through the attach Model and clear the screen")
	}
}

func TestScreen_InitStartsTheSubscriptionAndTheClock(t *testing.T) {
	if newScreen(NewModel("", nil, coreRun())).Init() == nil {
		t.Fatal("Init returned no command")
	}
}

func TestRunState(t *testing.T) {
	for _, tc := range []struct {
		status types.RunStatus
		parked bool
		want   runscreen.State
	}{
		{types.RunPending, false, runscreen.Pending},
		{types.RunRunning, false, runscreen.Running},
		{types.RunRunning, true, runscreen.Waiting},
		{types.RunCompleted, false, runscreen.Passed},
		{types.RunChecksPassed, false, runscreen.Passed},
		{types.RunHandedOff, false, runscreen.Passed},
		{types.RunFailed, false, runscreen.Failed},
		{types.RunCancelled, false, runscreen.Failed},
		{types.RunCIMonitorInterrupted, false, runscreen.Waiting},
	} {
		if got := runState(tc.status, tc.parked); got != tc.want {
			t.Errorf("runState(%s, %v) = %s, want %s", tc.status, tc.parked, got, tc.want)
		}
	}
}

func TestStepState(t *testing.T) {
	for status, want := range map[types.StepStatus]runscreen.State{
		types.StepStatusPending:          runscreen.Pending,
		types.StepStatusRunning:          runscreen.Running,
		types.StepStatusFixing:           runscreen.Running,
		types.StepStatusAwaitingApproval: runscreen.Waiting,
		types.StepStatusFixReview:        runscreen.Waiting,
		types.StepStatusCompleted:        runscreen.Passed,
		types.StepStatusFailed:           runscreen.Failed,
		types.StepStatusSkipped:          runscreen.Skipped,
	} {
		if got := stepState(status); got != want {
			t.Errorf("stepState(%s) = %s, want %s", status, got, want)
		}
	}
}
