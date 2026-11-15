package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/thellmwhisperer/runscreen"
	"github.com/thellmwhisperer/runscreen/markdown"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// screen is `no-mistakes attach` drawn with runscreen. The attach Model stays
// the owner of everything the screen does - the subscription, the gate keys,
// the findings selection, yolo, rerun, branch sync - and screen only draws it:
// its steps in runscreen's pipeline box, the log of the step that runs as the
// Agent box's events, and that step's findings in a box under it. The gate's
// action bar and the attach footer stay under runscreen's frame, and the
// Model's own full-screen views (help, diff, editors, sync confirmations)
// still draw as they always have.
type screen struct {
	m             Model
	rs            *runscreen.Model
	width, height int
	// logRun and logStep name the step whose log the Agent box holds; a log
	// line or a start of another step, or of another run, empties it.
	logRun  string
	logStep types.StepName
	// partial is the log's last line until its newline arrives.
	partial string
}

// runscreenKeys are the keys the attach Model does not bind that act on the
// boxes this screen fills: runscreen's own, which move the focus between its
// boxes and scroll the focused one.
var runscreenKeys = map[string]bool{"tab": true, "pgup": true, "pgdown": true}

func newScreen(m Model) *screen {
	s := &screen{m: m}
	s.rs = newRunscreenModel()
	s.sync()
	return s
}

func newRunscreenModel() *runscreen.Model {
	rs := runscreen.New(0, 0)
	rs.SetMarkdown(markdown.Inline)
	return rs
}

// Init starts the attach Model's subscription and runscreen's clock.
func (s *screen) Init() tea.Cmd {
	return tea.Batch(s.m.Init(), s.rs.Init())
}

// Update gives runscreen its own keys while it draws, and the attach Model
// everything else; runscreen also gets every message that is not
// a key, for its clock.
func (s *screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if s.drawsRunscreen() && runscreenKeys[msg.String()] {
			// Any key but x takes back a first x, as the attach Model does.
			s.m.confirmAbort = false
			s.rs.Update(msg)
			return s, nil
		}
		next, cmd := s.m.Update(msg)
		s.m = next.(Model)
		s.sync()
		return s, cmd
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case eventMsg:
		if msg.subscriptionID == s.m.subscriptionID {
			s.observe(msg.event)
		}
	}
	next, cmd := s.m.Update(msg)
	s.m = next.(Model)
	_, clock := s.rs.Update(msg)
	s.sync()
	return s, tea.Batch(cmd, clock)
}

// drawsRunscreen says whether the frame is runscreen's: not while the attach
// Model shows one of its own full-screen views.
func (s *screen) drawsRunscreen() bool {
	m := s.m
	return !m.showHelp && !m.showDiff && !m.editorActive() && !m.syncConfirm && !m.recoverConfirm
}

// observe notes what an event adds to the Agent box: a step that starts, or
// another step's log, empties it; each line of the log becomes what the agent
// says; a step's or the run's end flushes the line still waiting for its
// newline.
func (s *screen) observe(event ipc.Event) {
	if ipc.ClassOf(event.Type) == ipc.ClassState && event.StateRev != 0 && event.StateRev <= s.m.stateRev {
		return
	}
	switch event.Type {
	case ipc.EventStepStarted:
		if event.StepName != nil {
			s.follow(event.RunID, *event.StepName)
		}
	case ipc.EventLogChunk:
		if event.StepName == nil || event.Content == nil {
			return
		}
		s.follow(event.RunID, *event.StepName)
		lines := strings.Split(s.partial+*event.Content, "\n")
		s.partial = lines[len(lines)-1]
		for _, line := range lines[:len(lines)-1] {
			s.rs.Add(runscreen.Event{Kind: runscreen.Says, Text: line})
		}
	case ipc.EventStepCompleted, ipc.EventRunCompleted:
		s.rs.Add(runscreen.Event{Kind: runscreen.Says, Text: s.partial})
		s.partial = ""
	}
}

// follow makes the Agent box the log of step in run, empty when it held
// another's.
func (s *screen) follow(run string, step types.StepName) {
	if run == s.logRun && step == s.logStep {
		return
	}
	s.logRun, s.logStep, s.partial = run, step, ""
	s.rs = newRunscreenModel()
	s.sync()
}

// sync hands runscreen the pipeline and the boxes as the attach Model holds
// them now.
func (s *screen) sync() {
	s.rs.SetPipeline(s.pipeline())
	s.rs.SetBoxes(s.boxes())
}

// View is runscreen's frame over the attach Model's outcome banner, local
// branch status, action bar and footer; the Model's own full-screen views draw
// as they did.
func (s *screen) View() string {
	m := s.m
	if m.quitting {
		return ""
	}
	if !s.drawsRunscreen() {
		return m.View()
	}
	var under []string
	for _, section := range []string{
		renderOutcomeBanner(m.run, m.steps),
		renderLocalBranchStatus(m.branchSync, m.syncRefreshing, s.width),
		m.actionBarSection(),
		renderFooter(m.done, m.showHelp, m.confirmAbort, m.yoloMode, m.run, m.latestVersion, s.width),
	} {
		if section != "" {
			under = append(under, section)
		}
	}
	lower := strings.Join(under, "\n")
	s.rs.Update(tea.WindowSizeMsg{Width: s.width, Height: max(0, s.height-lipgloss.Height(lower))})
	return setTerminalTitle(m.terminalTitle()) + s.rs.View() + "\n" + lower
}

// pipeline is the run as runscreen's pipeline box draws it: its branch as its
// title, the run's state, and every step of the run in order.
func (s *screen) pipeline() runscreen.Pipeline {
	m := s.m
	p := runscreen.Pipeline{Title: "no-mistakes"}
	if m.run != nil {
		p.Title = m.run.Branch
		p.State = runState(m.run.Status, awaitingStep(m.steps) != nil)
	}
	for _, st := range m.steps {
		p.Steps = append(p.Steps, s.step(st))
	}
	return p
}

// step is st as a row of the pipeline box: its label, its state, its time and,
// as its note, what a parked or fixing state waits for or does.
func (s *screen) step(st ipc.StepResultInfo) runscreen.Step {
	row := runscreen.Step{Name: stepLabel(st.StepName), State: stepState(st.Status)}
	start := s.m.stepStartTimes[st.StepName]
	if start.IsZero() && st.StartedAt != nil {
		start = time.Unix(*st.StartedAt, 0)
	}
	switch {
	case st.DurationMS != nil:
		if start.IsZero() {
			start = time.Unix(0, 0)
		}
		row.Start, row.End = start, start.Add(time.Duration(*st.DurationMS)*time.Millisecond)
	case row.State == runscreen.Running:
		row.Start = start
	}
	switch st.Status {
	case types.StepStatusFixing:
		row.Note = "fixing"
	case types.StepStatusAwaitingApproval:
		row.Note = "approval"
	case types.StepStatusFixReview:
		row.Note = "fix review"
	}
	return row
}

// boxes is what runscreen shows under the Agent box: the error the attach
// Model holds, and the findings of the step parked at a gate, or else of the
// step whose log the Agent box holds.
func (s *screen) boxes() runscreen.Boxes {
	m := s.m
	var boxes runscreen.Boxes
	visibleErr := m.err
	if reviewErr := m.reviewRetryError(); reviewErr != nil {
		visibleErr = reviewErr
	}
	if visibleErr != nil {
		box := runscreen.Box{Title: "Error"}
		for _, line := range strings.Split(visibleErr.Error(), "\n") {
			box.Rows = append(box.Rows, runscreen.Row{Text: line})
		}
		boxes = append(boxes, box)
	}
	step, gate := s.logStep, false
	if parked := awaitingStep(m.steps); parked != nil {
		step, gate = parked.StepName, true
	}
	if box, ok := s.findingsBox(step, gate); ok {
		boxes = append(boxes, box)
	}
	return boxes
}

// findingsBox is step's findings, counted by severity in its title. At a gate
// each row also shows whether it is selected and where the cursor the gate
// keys move is; the cursor's row is open on where the finding is and what
// answers it.
func (s *screen) findingsBox(step types.StepName, gate bool) (runscreen.Box, bool) {
	items := s.m.findingItems(step)
	if len(items) == 0 {
		return runscreen.Box{}, false
	}
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Severity]++
	}
	title := "Findings · " + stepLabel(step)
	for _, sev := range []string{"error", "warning", "info"} {
		if counts[sev] > 0 {
			title += fmt.Sprintf(" · %s %d", severityIcon(sev), counts[sev])
		}
	}
	selected, chosen := s.m.findingSelections[step]
	cursor := s.m.findingCursor[step]
	box := runscreen.Box{Title: title}
	for i, item := range items {
		text := fmt.Sprintf("**%s** %s", severityIcon(item.Severity), item.Description)
		if gate {
			mark := "[ ]"
			if !chosen || selected[item.ID] {
				mark = "[x]"
			}
			pointer := "  "
			if i == cursor {
				pointer = "› "
			}
			text = pointer + mark + " " + text
		}
		row := runscreen.Row{Text: text, Open: gate && i == cursor}
		if item.File != "" {
			where := item.File
			if item.Line > 0 {
				where = fmt.Sprintf("%s:%d", item.File, item.Line)
			}
			row.Rows = append(row.Rows, runscreen.Row{Text: "`" + where + "`"})
		}
		if item.Action != "" {
			row.Rows = append(row.Rows, runscreen.Row{Text: "action: " + item.Action})
		}
		if note := s.m.findingInstructions[step][item.ID]; note != "" {
			row.Rows = append(row.Rows, runscreen.Row{Text: "note: " + note})
		}
		box.Rows = append(box.Rows, row)
	}
	return box, true
}

// runState is the run's status as runscreen's state beside the pipeline
// box's title; a running run parked at a gate is waiting.
func runState(status types.RunStatus, parked bool) runscreen.State {
	switch status {
	case types.RunRunning:
		if parked {
			return runscreen.Waiting
		}
		return runscreen.Running
	case types.RunCompleted, types.RunChecksPassed, types.RunHandedOff:
		return runscreen.Passed
	case types.RunFailed, types.RunCancelled:
		return runscreen.Failed
	case types.RunCIMonitorInterrupted:
		return runscreen.Waiting
	default:
		return runscreen.Pending
	}
}

// stepState is a step's status as runscreen's state: a fixing step runs, and
// a step parked at a gate waits.
func stepState(status types.StepStatus) runscreen.State {
	switch status {
	case types.StepStatusRunning, types.StepStatusFixing:
		return runscreen.Running
	case types.StepStatusAwaitingApproval, types.StepStatusFixReview:
		return runscreen.Waiting
	case types.StepStatusCompleted:
		return runscreen.Passed
	case types.StepStatusFailed:
		return runscreen.Failed
	case types.StepStatusSkipped:
		return runscreen.Skipped
	default:
		return runscreen.Pending
	}
}
