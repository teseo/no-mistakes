//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// runscreenScenario parks the review on one warning after a pause long enough
// for `attach` to see the review run, then lets every later step pass.
func runscreenScenario(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runscreen-scenario.yaml")
	content := `actions:
  - match: "Review the code changes and return structured findings"
    delay_ms: 15000
    text: "review found a warning"
    structured:
      findings:
        - id: "rs-1"
          severity: warning
          file: "feature.txt"
          line: 1
          description: "the flag is read before the config loader sets it"
          action: ask-user
      summary: "found 1 issue"
      risk_level: medium
      risk_rationale: "warning requires human review"
      risk_scope: source-or-external
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no risks detected in the diff"
      risk_scope: source-or-external
      tested:
        - "fakeagent: simulated test run"
      testing_summary: "simulated tests passed"
      scenarios:
        - name: "fakeagent: simulated end-to-end scenario"
          result: pass
          live: true
          evidence: "fakeagent: simulated test run"
          reason: ""
      verdict: go
      artifacts: []
      title: "feat: fakeagent change"
      body: "## Summary\nfakeagent canned PR body"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write runscreen scenario: %v", err)
	}
	return path
}

// TestAttachDrawsWithRunscreen drives the real `no-mistakes attach` through a
// pseudo-terminal while a run goes through the review gate: the pipeline box
// lists the run's steps with their state and time, the Agent box shows the
// running step's log, the review's findings show in a box of their own, the
// gate keys still work through runscreen's frame, and the run ends passed.
// It also records the run's event stream; NM_RUNSCREEN_RECORDING names a file
// to keep it in, the replay internal/tui draws through both screens.
func TestAttachDrawsWithRunscreen(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the pty driver is script(1) from util-linux")
	}
	h := newHarnessAt(t, SetupOpts{Agent: "claude", Scenario: runscreenScenario(t)}, shortHarnessRoot(t))
	h.CommitChange("init-runscreen", "seed.txt", "seed\n", "seed for the runscreen journey")
	if out, err := h.RunInDir(h.AddWorktree("init-runscreen"), "init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	const branch = "feature/runscreen"
	h.CommitChange(branch, "feature.txt", "flag = true\n", "add the feature flag")
	fw := h.AddWorktree(branch)
	h.PushToGate(branch)
	run := h.WaitForRunRunning(branch, 60*time.Second)
	recording := recordRun(t, h, run.ID)

	screen := startPTY(t, fw, h.NMBin, "attach")
	from := 0
	frame := screen.waitFor(from, 60*time.Second, branch, "Agent", "review found a warning", "claude exited pid=", "running")
	for _, step := range []string{"Intent", "Rebase", "Review", "Test", "Document", "Lint", "Push", "PR", "CI"} {
		if !strings.Contains(frame, step) {
			t.Errorf("the pipeline box is missing step %q:\n%s", step, frame)
		}
	}
	t.Logf("EVIDENCE runscreen while the review runs:\n%s", lastScreenFrame(frame))

	frame = screen.waitFor(from, 60*time.Second, "Findings · Review · W 1", "[x] W the flag is read before the config loader sets it",
		"approval · waiting", "Review awaiting action:", "a approve")
	t.Logf("EVIDENCE runscreen at the review gate:\n%s", lastScreenFrame(frame))

	// runscreen's own keys reach it: t unfolds tool use, and its key line says so.
	from = screen.send("t")
	screen.waitFor(from, 15*time.Second, "hide tool use")
	// The mouse wheel goes to runscreen too; the frame keeps the gate.
	screen.send("\x1b[<65;100;10M")
	// The attach Model's own full-screen views still draw: ? opens its help.
	from = screen.send("?")
	screen.waitFor(from, 15*time.Second, "toggle current", "select all")
	from = screen.send("?")
	screen.waitFor(from, 15*time.Second, "Findings · Review")

	// The gate keys go to the attach Model: a approves, and the run goes on.
	from = screen.send("a")
	frame = screen.waitFor(from, 120*time.Second, "Pipeline passed")
	t.Logf("EVIDENCE runscreen once the run passed:\n%s", lastScreenFrame(frame))

	screen.send("q")
	if err := screen.wait(30 * time.Second); err != nil {
		t.Fatalf("attach did not exit after q: %v\n%s", err, screen.after(0))
	}

	final := h.RunInfo(run.ID)
	if final.Status != types.RunCompleted {
		t.Fatalf("run status = %s, want completed", final.Status)
	}
	if review, ok := findStep(final.Steps, types.StepReview); !ok || review.Status != types.StepStatusCompleted {
		t.Fatalf("review step = %+v, want completed after the TUI approved it", review)
	}
	recording.save(t, h)
}

// shortHarnessRoot is an empty harness root whose daemon socket fits the
// kernel's limit even under a long TMPDIR, where a random temporary name does
// not: a fixed short name under it, the first free one, which the e2e
// daemon reaper takes for a temporary root by its nmh- prefix.
func shortHarnessRoot(t *testing.T) string {
	t.Helper()
	for i := range 100 {
		root := filepath.Join(os.TempDir(), "nmh-"+strconv.Itoa(i))
		if err := os.Mkdir(root, 0o700); err == nil {
			return root
		} else if !os.IsExist(err) {
			t.Fatalf("mkdir harness root: %v", err)
		}
	}
	t.Fatal("no free short harness root")
	return ""
}

// runRecording is a run as attach sees it: the snapshot it starts from and
// every event after it, until the run ends.
type runRecording struct {
	mu     sync.Mutex
	Run    *ipc.RunInfo `json:"run"`
	Events []ipc.Event  `json:"events"`
	done   chan struct{}
}

// recordRun subscribes to the run's events, then reads its snapshot, as
// attach does.
func recordRun(t *testing.T, h *Harness, runID string) *runRecording {
	t.Helper()
	events, cancel, err := ipc.Subscribe(paths.WithRoot(h.NMHome).Socket(), &ipc.SubscribeParams{RunID: runID})
	if err != nil {
		t.Fatalf("subscribe to run %s: %v", runID, err)
	}
	t.Cleanup(cancel)
	r := &runRecording{Run: h.RunInfo(runID), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for event := range events {
			r.mu.Lock()
			r.Events = append(r.Events, event)
			r.mu.Unlock()
		}
	}()
	return r
}

// save writes the recording, once the run's stream ended, to the file
// NM_RUNSCREEN_RECORDING names, the harness's own paths replaced by short
// stand-ins.
func (r *runRecording) save(t *testing.T, h *Harness) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the run's event stream did not end")
	}
	out := os.Getenv("NM_RUNSCREEN_RECORDING")
	if out == "" {
		return
	}
	r.mu.Lock()
	data, err := json.MarshalIndent(r, "", "  ")
	r.mu.Unlock()
	if err != nil {
		t.Fatalf("encode recording: %v", err)
	}
	root := filepath.Dir(h.NMHome)
	data = bytes.ReplaceAll(data, []byte(root), []byte("/tmp/nm"))
	data = bytes.ReplaceAll(data, []byte(os.TempDir()), []byte("/tmp"))
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write recording: %v", err)
	}
	t.Logf("recorded %d events of run %s to %s", len(r.Events), r.Run.ID, out)
}

// ptyScreen is a command on a 40 by 140 pseudo-terminal: what it printed, and
// its input.
type ptyScreen struct {
	t       *testing.T
	in      io.WriteCloser
	mu      sync.Mutex
	out     []byte
	changed chan struct{}
	exited  chan error
}

func startPTY(t *testing.T, dir, bin string, args ...string) *ptyScreen {
	t.Helper()
	scriptBin, err := exec.LookPath("script")
	if err != nil {
		t.Fatalf("the pty driver needs script(1): %v", err)
	}
	command := "stty rows 40 cols 140; exec " + shellQuote(bin)
	for _, arg := range args {
		command += " " + shellQuote(arg)
	}
	cmd := exec.Command(scriptBin, "-qfec", command, "/dev/null")
	cmd.Dir = dir
	cmd.Env = mergedEnv(os.Environ(), map[string]string{"TERM": "xterm-256color"})
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, outW
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", command, err)
	}
	inR.Close()
	outW.Close()
	s := &ptyScreen{t: t, in: inW, changed: make(chan struct{}, 1), exited: make(chan error, 1)}
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := outR.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.out = append(s.out, buf[:n]...)
				s.mu.Unlock()
				select {
				case s.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				outR.Close()
				s.exited <- cmd.Wait()
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		if cmd.ProcessState == nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return s
}

// send types keys and returns where the output stood before them.
func (s *ptyScreen) send(keys string) int {
	s.t.Helper()
	s.mu.Lock()
	from := len(s.out)
	s.mu.Unlock()
	if _, err := io.WriteString(s.in, keys); err != nil {
		s.t.Fatalf("type %q: %v", keys, err)
	}
	return from
}

// after is the output printed from byte from on, without its terminal
// controls.
func (s *ptyScreen) after(from int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ansi.Strip(string(s.out[from:]))
}

// waitFor waits until every want shows in the output printed from byte from
// on, and returns that output.
func (s *ptyScreen) waitFor(from int, timeout time.Duration, wants ...string) string {
	s.t.Helper()
	deadline := time.After(timeout)
	for {
		text := s.after(from)
		missing := ""
		for _, want := range wants {
			if !strings.Contains(text, want) {
				missing = want
				break
			}
		}
		if missing == "" {
			return text
		}
		select {
		case <-s.changed:
		case err := <-s.exited:
			s.t.Fatalf("the screen exited (%v) before showing %q:\n%s", err, missing, lastScreenFrame(s.after(from)))
		case <-deadline:
			s.t.Fatalf("the screen did not show %q in %v:\n%s", missing, timeout, lastScreenFrame(text))
		}
	}
}

// wait waits for the command to exit.
func (s *ptyScreen) wait(timeout time.Duration) error {
	select {
	case err := <-s.exited:
		return err
	case <-time.After(timeout):
		return io.ErrNoProgress
	}
}
