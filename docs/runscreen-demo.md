# Demo: `no-mistakes attach` drawn with runscreen

[runscreen](https://github.com/thellmwhisperer/runscreen) draws the run screen of an agent pipeline: a pipeline box of steps on the left, the agent's events on the right, and boxes the caller adds under them.
On this branch, `no-mistakes attach` (and bare `no-mistakes`, which attaches the same way) draws with it.
It uses the same terminal libraries as the TUI in `internal/tui`: bubbletea v1.3.10, lipgloss v1.1.0 and termenv v0.16.0.

The pipeline is unchanged: nothing in `internal/pipeline`, `internal/daemon` or `internal/db` moved.
The attach model in `internal/tui` still owns the subscription, the gate keys, the findings selection, yolo, rerun and branch sync.
`internal/tui/runscreen.go` only draws what that model holds:

- the pipeline box lists the run's own steps (intent, rebase, review, test, document, lint, push, pr, ci, plus any repository gates) with their state and time, and the branch as its title;
- the Agent box shows the log of the step that runs, one event per line; it empties when another step starts;
- a Findings box under it lists that step's findings, or the findings of the step parked at a gate, with the selection and the cursor the gate keys move;
- the gate's action bar and the attach footer stay under runscreen's frame, so `a`, `f`, `s`, `x`, `y`, `r`, `o` and `q` work as before;
- runscreen's own keys are the ones attach does not bind: `tab` moves between boxes, and `pgup`/`pgdown` scroll the focused one; the mouse is left to the terminal, so text on the screen can be selected and copied;
- help (`?`), the diff (`d`), the finding editors and the branch-sync confirmations still draw the old full-screen views.

## The recorded run

`internal/e2e/runscreen_attach_test.go` drives the real binary's `no-mistakes attach` through a pseudo-terminal while a run reaches the review gate, approves it with `a` from the screen, and waits for the run to pass.
It also records the run's event stream as attach receives it, which `internal/tui/testdata/runscreen-demo/recording.json` keeps:

```sh
NM_RUNSCREEN_RECORDING=$PWD/internal/tui/testdata/runscreen-demo/recording.json \
  go test -tags=e2e -count=1 -run TestAttachDrawsWithRunscreen ./internal/e2e/
```

`TestRunscreenDemo_RecordedRunAtTheReviewGate` in `internal/tui` replays that recording through the old screen and the new one, both 100 columns by 34 rows, up to the moment the review parks at its gate.
It checks that the frames below are those two frames.
Run it with `-update-runscreen-demo` to redraw them after a change:

```sh
go test -count=1 -run TestRunscreenDemo ./internal/tui/ -update-runscreen-demo
```

## Side by side

The review is parked on one warning. The old screen is on the left and runscreen on the right.

<!-- runscreen-demo:frames -->

```text
Before: the attach TUI's own screen                                                                    After: the same run drawn with runscreen

╭─ Pipeline ─────────────────────────╮  Review awaiting action:                                        ╭─ Pipeline ─────────────────────────╮  ╭─ Agent* ─────────────────────────────────────────────────╮
│ feature/runscreen          running │   a approve  f fix  s skip  x abort │ ␣ toggle  e edit  + add   │ feature/runscreen        ⏸ waiting │  │                                                          │
A all  N none                                                                                          │                                    │  │                                                          │
│                                    │                                                                 │ – Intent  0.0 s            skipped │  │                                                          │
│ – Intent  50ms                     │  ╭─ Findings - W 1 ─────────────────────────────────────────╮   │ │                                  │  │                                                          │
│ │                                  │  │ Risk: MEDIUM - warning requires human review             │   │ ✓ Rebase  0.0 s             passed │  │                                                          │
│ ✓ Rebase  95ms                     │  │ > [x] W feature.txt:1                                    │   │ │                                  │  │                                                          │
│ │                                  │  │         the flag is read before the config loader sets   │   │ ⏸ Review  15.0 s approv… · waiting │  │                                                          │
│ ⏸ Review  15.1s - awaiting approval                                                                  │ │                                  │  │                                                          │
│                                       │         it                                               │   │ ○ Test                     pending │  │                                                          │
│ │                                  │  ╰──────────────────────────────────────────────────────────╯   │ │                                  │  │                                                          │
│ ○ Test                             │                                                                 │ ○ Document                 pending │  │                                                          │
│ │                                  │  ╭─ Log ────────────────────────────────────────────────────╮   │ │                                  │  │                                                          │
│ ○ Document                         │  │ claude started pid=3759371                               │   │ ○ Lint                     pending │  │                                                          │
│ │                                  │  │                                                          │   │ │                                  │  │                                                          │
│ ○ Lint                             │  │ review found a warningreview found a warning             │   │ ○ Push                     pending │  │                                                          │
│ │                                  │  │ claude exited pid=3759371 status=success                 │   │ │                                  │  │                                                          │
│ ○ Push                             │  │                                                          │   │ ○ PR                       pending │  │ ✎ reviewing changes...                                   │
│ │                                  │  ╰──────────────────────────────────────────────────────────╯   │ │                                  │  │ ✎ claude started pid=3759371                             │
│ ○ PR                               │                                                                 │ ○ CI                       pending │  │ ✎ review found a warningreview found a warning           │
│ │                                  │                                                                 ╰────────────────────────────────────╯  │ ✎ claude exited pid=3759371 status=success               │
│ ○ CI                               │                                                                                                         ╰──────────────────────────────────────────────────────────╯
╰────────────────────────────────────╯                                                                                                         ╭─ Findings · Review · W 1 ────────────────────────────────╮
                                                                                                                                               │ ▾ › [x] W the flag is read before the config loader sets │
  q detach  x abort  ? help  y yolo                                                                                                            │   it                                                     │
                                                                                                                                               │     feature.txt:1                                        │
                                                                                                                                               │     action: ask-user                                     │
                                                                                                                                               ╰──────────────────────────────────────────────────────────╯

                                                                                                         q quit  t show tool use  h show thinking
                                                                                                       Review awaiting action:
                                                                                                        a approve  f fix  s skip  x abort │ ␣ toggle  e edit  + add  A all  N none
                                                                                                         q detach  x abort  ? help  y yolo
```

<!-- /runscreen-demo:frames -->
