package tui

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/thellmwhisperer/runscreen"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

var updateRunscreenDemo = flag.Bool("update-runscreen-demo", false,
	"rewrite the frames of docs/runscreen-demo.md from the recorded run")

// runscreenRecording is a run attach saw: the snapshot it started from and
// every event after it. internal/e2e's TestAttachDrawsWithRunscreen recorded
// it from the real binary, with NM_RUNSCREEN_RECORDING naming the file.
const runscreenRecording = "testdata/runscreen-demo/recording.json"

const (
	runscreenDemoDoc = "../../docs/runscreen-demo.md"
	framesStart      = "<!-- runscreen-demo:frames -->"
	framesEnd        = "<!-- /runscreen-demo:frames -->"
	demoWidth        = 100
	demoHeight       = 34
	frameGap         = "   "
)

type recordedRun struct {
	Run    *ipc.RunInfo `json:"run"`
	Events []ipc.Event  `json:"events"`
}

// loadRecordedRun reads the recording afresh, so each screen, and each read
// of its snapshot, is given a run of its own.
func loadRecordedRun(t *testing.T) recordedRun {
	t.Helper()
	data, err := os.ReadFile(runscreenRecording)
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	var r recordedRun
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("decode recording: %v", err)
	}
	return r
}

// atReviewGate says whether event is the review parking at its gate.
func atReviewGate(event ipc.Event) bool {
	return event.Type == ipc.EventStepCompleted && event.StepName != nil && *event.StepName == types.StepReview &&
		event.Status != nil && types.StepStatus(*event.Status) == types.StepStatusAwaitingApproval
}

// replayToReviewGate replays the recorded run through the old screen and the
// new, at the same size, until the review parks at its gate, and returns both
// frames without their terminal controls. A stream gap is answered as the
// daemon answered attach's read after it: with the run's snapshot.
func replayToReviewGate(t *testing.T) (oldFrame, newFrame string) {
	t.Helper()
	rec := loadRecordedRun(t)
	size := tea.WindowSizeMsg{Width: demoWidth, Height: demoHeight}
	var old tea.Model = NewModel("", nil, loadRecordedRun(t).Run)
	old, _ = old.Update(size)
	s := newScreen(NewModel("", nil, loadRecordedRun(t).Run))
	s.Update(size)
	reached := false
	for _, event := range rec.Events {
		old, _ = old.Update(eventMsg{event: event, subscriptionID: 1})
		s.Update(eventMsg{event: event, subscriptionID: 1})
		if event.Type == ipc.EventStreamGap {
			old, _ = old.Update(runReconciledMsg{run: loadRecordedRun(t).Run, subscriptionID: 1})
			s.Update(runReconciledMsg{run: loadRecordedRun(t).Run, subscriptionID: 1})
		}
		if atReviewGate(event) {
			reached = true
			break
		}
	}
	if !reached {
		t.Fatal("the recording never reaches the review gate")
	}
	return plainFrame(old.View()), plainFrame(s.View())
}

// plainFrame is a frame as the terminal shows it, without its controls or
// the spaces ending its lines.
func plainFrame(view string) string {
	lines := strings.Split(ansi.Strip(view), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// sideBySide is the old frame and the new one beside it, under their names.
func sideBySide(oldFrame, newFrame string) string {
	left := append([]string{"Before: the attach TUI's own screen", ""}, strings.Split(oldFrame, "\n")...)
	right := append([]string{"After: the same run drawn with runscreen", ""}, strings.Split(newFrame, "\n")...)
	var b strings.Builder
	for i := range max(len(left), len(right)) {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		line := l + strings.Repeat(" ", max(0, demoWidth-lipgloss.Width(l))) + frameGap + r
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	return b.String()
}

// framesBlock is the part of docs/runscreen-demo.md the replay draws.
func framesBlock(oldFrame, newFrame string) string {
	return framesStart + "\n\n```text\n" + sideBySide(oldFrame, newFrame) + "```\n\n" + framesEnd
}

// TestRunscreenDemo_RecordedRunAtTheReviewGate replays the run the e2e
// recorded from the real binary through the old screen and the new one. The
// new frame has every step of the run with its state and time, the review's
// log as the Agent box's events and its finding in a box of its own, and the
// two frames side by side are the ones docs/runscreen-demo.md shows.
func TestRunscreenDemo_RecordedRunAtTheReviewGate(t *testing.T) {
	oldFrame, newFrame := replayToReviewGate(t)
	var reviewTook string
	for _, event := range loadRecordedRun(t).Events {
		if atReviewGate(event) && event.DurationMS != nil {
			reviewTook = runscreen.Human(time.Duration(*event.DurationMS) * time.Millisecond)
		}
	}

	for _, want := range [][]string{
		{"feature/runscreen", "⏸ waiting"},
		{"Intent", "0.0 s", "skipped"},
		{"Rebase", "0.0 s", "passed"},
		{"Review", reviewTook, "waiting"},
		{"Test", "pending"},
		{"Document", "pending"},
		{"Lint", "pending"},
		{"Push", "pending"},
		{"PR", "pending"},
		{"CI", "pending"},
		{"Agent"},
		{"reviewing changes..."},
		{"claude exited pid="},
		{"Findings · Review · W 1"},
		{"› [x] W the flag is read before the config loader"},
		{"feature.txt:1"},
		{"Review awaiting action:"},
		{"a approve", "f fix", "s skip"},
		{"q detach", "x abort", "? help", "y yolo"},
	} {
		if !hasLineContainingAll(newFrame, want...) {
			t.Errorf("the runscreen frame has no line with %q:\n%s", want, newFrame)
		}
	}
	if strings.Contains(newFrame, "rebase") && strings.Contains(newFrame, "fetching latest upstream state") {
		t.Errorf("the Agent box still holds the rebase's log at the review gate:\n%s", newFrame)
	}
	if !strings.Contains(oldFrame, "the flag is read before the config loader") {
		t.Errorf("the old frame lost the finding, so the comparison is not of the same run:\n%s", oldFrame)
	}

	doc, err := os.ReadFile(runscreenDemoDoc)
	if err != nil {
		t.Fatalf("read %s: %v", runscreenDemoDoc, err)
	}
	text := string(doc)
	start, end := strings.Index(text, framesStart), strings.Index(text, framesEnd)
	if start < 0 || end < start {
		t.Fatalf("%s has no %s ... %s block", runscreenDemoDoc, framesStart, framesEnd)
	}
	want := framesBlock(oldFrame, newFrame)
	got := text[start : end+len(framesEnd)]
	if got == want {
		return
	}
	if !*updateRunscreenDemo {
		t.Fatalf("%s's frames are not the recorded run's; rerun with -update-runscreen-demo.\nwant:\n%s",
			runscreenDemoDoc, want)
	}
	text = text[:start] + want + text[end+len(framesEnd):]
	if err := os.WriteFile(filepath.Clean(runscreenDemoDoc), []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", runscreenDemoDoc, err)
	}
}
