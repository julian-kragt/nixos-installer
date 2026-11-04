# `internal/runner` — how to show progress in this repo

Read this before writing or changing any code that prints progress. Do not use `fmt.Print*` or `os.Stderr` for progress; do not add a second logger.

## Idea

The runner keeps a **tree of tasks**. The current task travels in the `context.Context`, so any code that receives `ctx` logs under the correct parent. This is the OpenTelemetry span pattern. If `ctx` has no runner, every call is a no-op (safe in tests).

On a TTY, finished top-level (root) tasks are printed permanently; running ones stay in a live area that is redrawn every 80 ms. Headers stay on top, their spinner turns into `✓`/`✗` in place, several tasks can spin at once. A non-TTY `Out` only gets finished blocks, no colors, no escape codes.

The runner also owns the terminal input: questions to the operator go through it, so they appear inside the task that asks.

## API

```go
import "installer/internal/runner"

// main only
ui := runner.New(runner.Options{Out: os.Stderr, In: os.Stdin, File: f, Verbose: v})
ctx = runner.NewContext(ctx, ui)
ui.Close()                             // must run before os.Exit, restores the cursor

// Tasks
runner.Run(ctx, "Title", func(ctx context.Context) error {...}) error   // preferred
ctx, t := runner.Start(ctx, "Title"); ...; t.End(err)                   // manual; nil = ✓
ctx, t := runner.StartDebug(ctx, "Title")      // hidden (with children) unless Verbose

// Steps one after another, stopping at the first error
runner.RunSteps(ctx, runner.Step{Title: "A", Fn: func(ctx context.Context) error {...}}, ...)

// Parallel: each step is its own child task, first error cancels siblings,
// errors are joined (sibling-cancel errors are dropped).
runner.Parallel(ctx, runner.Step{Title: "A", Fn: func(ctx context.Context) error {...}}, ...)

// Messages go into ctx's task
runner.Debug/Info/Warn/Error(ctx, "msg")

// Command output (tail under the task, full text in the log file).
// pipeline.Machine.Run already writes here.
runner.Output(ctx)                             // io.Writer

// Questions to the operator
line, err := runner.Ask(ctx, "Target IP address: ")
pw, err := runner.AskSecret(ctx, "SSH password: ")                // no echo on a terminal, never logged
i, err := runner.Choose(ctx, "Select the install disk:", items)   // numbered list, returns the index
```

## Rules

- **Always pass the `ctx` returned by `Start`/`Run`/`Parallel`** into the work and into goroutines. Logging with the parent's ctx puts lines under the wrong task.
- Logging goes through `ctx` only. `Task` handles lifecycle only.
- `pipeline.Run` already wraps each stage in `runner.Run(ctx, "<id> <name>", ...)`. A stage must **not** create its own header; use `runner.Run` for sub-steps.
- `go test -race` is required. Parallel steps must not write `env.State`: return results, assign after the join.
- Commands that handle secrets must not stream output via `runner.Output`: run them on `Machine.Quiet()`.
- Debug level: `Debug` lines and `StartDebug` tasks show only with `--verbose`, but always go to the log file.

## Behaviour worth knowing

- Roots are committed in start order. A root that finishes early stays live until all earlier roots are committed.
- Ending a task closes still-running children as `✗ … (parent ended)`.
- A task started under a parent that has already ended, or a message logged into a task that is already printed, gets its own entry at its indentation below everything that started before it. This only happens when a ctx outlives its task.
- Output tail: 5 lines shown under a running task, dropped on success; on failure the last 20 lines are kept under the `✗`.
- Output writer: buffers partial lines, `\r` resets the line, strips ANSI/control chars.
- Live area taller than the terminal: header, `… N lines`, newest lines; the commit prints everything.
- `Ask`: the live area is frozen while a question is open. On a TTY the question and the answer are erased again afterwards, so log the answer if it should stay visible. EOF with nothing typed is `io.EOF`; a last line without newline counts as an answer. Without a runner in `ctx`, or without `Options.In`, there is nobody to ask: `io.EOF`.
- Log file: `${XDG_STATE_HOME:-~/.local/state}/nixos-installer/logs/install-<time>.log`, debug level, no ANSI, format `15:12:03.501 │ Base install › Partitioning disk | message`. `main` prints `Full log: <path>` on failure.
- GoLand's console is not a TTY and paints stderr red; enable "Emulate terminal in output console" to see the live view.

## Files

`runner.go` Runner/Options/ctx/ticker/level funcs/file sink · `task.go` nodes, Start/Run/End/RunSteps/Parallel · `render.go` commit, live area, layout · `ask.go` Ask/Choose, prompt layout and erasing · `output.go` command-output writer · `format.go` formatting helpers, colors, spinner frames.

Tests build a Runner with `newRunner(opts, tty, size)`, a fake clock (`r.now`) and call `r.draw()` directly (see `runner_test.go` for the fake terminal; `ask_test.go` for an open question).

Not built yet (design leaves room): full plain/CI mode, handing the terminal to an interactive command (`ssh -t`).
