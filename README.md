# Ralph Loops

A small Go CLI that drives an iterative "ralph loop": it spawns the
`claude` CLI as a child, feeds it an operator prompt assembled from
your project's `reqs/` directory, parses the stream-json event flow,
and repeats until the agent reports DONE, a wall-clock budget expires,
or you Ctrl-C.

[![Go](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/ai4mgreenly/ralph-loops.svg)](https://pkg.go.dev/github.com/ai4mgreenly/ralph-loops)

The spec under `reqs/` is treated as read-only input. Each iteration
reads it, picks the smallest meaningful unit of work, makes one
focused change in the workdir source, runs whatever tests the project
defines, and reports back CONTINUE or DONE.

- [Build](#build)
- [Use](#use)
- [The intended loop](#the-intended-loop)
- [Architecture](#architecture)
- [How one iteration runs](#how-one-iteration-runs)
- [Exit codes](#exit-codes)
- [Glossary](#glossary)
- [Requirement IDs](#requirement-ids)
- [License](#license)

## Build

The recommended path is `make install`, which builds the binary and
copies it to `$HOME/.local/bin/ralph` so you can invoke `ralph` from
anywhere:

    git clone https://github.com/ai4mgreenly/ralph-loops
    cd ralph-loops
    make install

Make sure `$HOME/.local/bin` is on your `$PATH`. If you prefer not to
install, `make build` produces `bin/ralph` and you can invoke it
directly. Requires Go 1.26+ and the `claude` CLI on `$PATH`.

### Build, test, lint

The Makefile wraps the usual Go workflow. Run `make help` for the
full list; the most useful targets are:

- `make build` — compile `bin/ralph`.
- `make test` — run the test suite.
- `make test-race` — run the test suite with the race detector.
- `make cover` — produce `coverage.out` and print a summary.
- `make lint` — run `golangci-lint` (skipped with a hint if it isn't
  installed; `make tools` installs the pinned version).
- `make fmt` / `make fmt-check` — apply (or assert) `gofmt`.
- `make tidy` / `make tidy-check` — `go mod tidy`, with a check
  variant that fails CI when go.mod or go.sum drifts.
- `make check` — `test-race lint fmt-check tidy-check` together;
  this is what CI runs.

## Use

Scaffold a new project with a starter spec directory:

    ralph init my-app

This creates `my-app/reqs/` with two files:

- `OVERVIEW.md` — an empty project-shape stub for you to fill in
  (what the project is, who uses it, hard constraints, what's out
  of scope).
- `INTERACTIVE.md` — a brief for an interactive helper agent that
  knows how to help you sharpen the spec without writing code
  itself.

Both files are plain Markdown — `ralph` reads everything under
`reqs/` each iteration but never modifies it. Only you (with the
helper agent's assistance) edit the spec.

Now `cd` into the project and start an interactive `claude`
session, asking it to read `reqs/INTERACTIVE.md` first:

    cd my-app
    claude
    > read reqs/INTERACTIVE.md and help me build out the spec

The helper will interview you about goals, audience, constraints,
and out-of-scope items, then propose a file layout and start
writing requirements with you. **You need at least a rough spec
before `ralph` has anything to build from** — the bare scaffold
produced by `ralph init` is just a stub. Spend time here first; a
sharper spec makes for a tighter ralph run.

**A split-terminal setup is the most ergonomic way to work.** Put
the interactive `claude` session in one pane and a plain shell in
the other. Sharpen the spec on the left until it describes
something the build agent can actually start on, then kick off
`ralph` in the right pane:

    ralph .

`ralph` reads `./reqs/`, treats the current directory as the
workdir, and calls opus at medium effort with the 1M-token context
window enabled, iterating until the agent reports DONE or you
interrupt. Each iteration is bracketed by a banner so you can see
the cadence; per-event stream output appears underneath, with a
`waiting for claude (Xs)` spinner during long pauses. At the end of
the run a summary panel reports start/end times, per-event counts,
tokens, cost, and time spent in LLM vs. tools. The same data is
appended as one JSON line per run to `~/.ralph-loops/results.jsonl`
for later inspection.

To tune the run:

    ralph --model=sonnet --duration=2h .
    ralph --1m-context=false --reqs=../shared-spec ./app
    ralph --effort=high --tools=Bash,Read,Write,Edit .

See `ralph help` for the full flag list.

## The intended loop

ralph is designed to be one half of a two-loop workflow.

The **outer loop** is you and an interactive agent (Claude Code or
similar) iterating on the spec under `reqs/`. After `ralph init`,
you point the helper agent at `reqs/INTERACTIVE.md` and have a
conversation: the helper interviews you about goals, audience,
hard constraints, and what's out of scope, then proposes a file
layout and writes individual requirements. The discipline it
holds to is **WHAT-and-WHY, not HOW** — every requirement
describes an observable property of the finished system and,
when useful, the reason it matters. Implementation choices belong
to the build agent on the inside loop.

The **inner loop** is ralph driving claude against that spec,
iteration after iteration. Each invocation reads the whole `reqs/`
tree, inspects the current state of the workdir, picks the
smallest meaningful unit of work, makes one focused change, and
runs whatever tests the project defines. The orchestrator never
modifies the spec — it's read-only input. After many iterations
the build catches up to the spec, or surfaces something the spec
doesn't actually answer.

You move between loops as the work demands:

1. Start with a rough spec — even one paragraph in `OVERVIEW.md`
   is enough to begin. Run ralph. Watch the iteration banners and
   per-event stream go by.
2. When ralph stalls, drifts toward something you didn't want, or
   a requirement turns out to be ambiguous, stop the loop. Bring
   the helper agent back, sharpen the relevant section of the
   spec, and mint a fresh requirement ID (`ralph newid`) for any
   requirement whose meaning materially changed — the new ID
   signals the build agent to re-evaluate it from scratch on the
   next pass.
3. Run ralph again. Repeat.

This is why ralph treats `reqs/` as read-only and why the build
agent is told never to expand scope: the spec is the steering
wheel, and you turn it between runs. The build agent's job is
just to drive toward whatever the spec currently says.

## Architecture

[CLAUDE.md](./CLAUDE.md) holds the design notes for contributors —
this section mirrors them for readers landing here first.

```
cmd/ralph/         Entry point and embedded prompt.md. Thin: parses
                   flags, constructs loop.Config, calls loop.Run.
internal/loop/     The driver. Split by concern:
                     loop.go       Config, Run, signal plumbing
                     iteration.go  One iteration: kickoff, event pump
                     stats.go      Token/cost tallies, panel rendering
                     agent.go      Spawner/Session interfaces consumed here
                   Subprocess mechanics live in internal/agent; output
                   rendering lives in internal/render. loop owns lifecycle.
internal/agent/    Wraps the claude CLI behind Spawner/Session interfaces.
                   Owns os/exec, the user-message envelope, process-group
                   plumbing, and a typed ExitError. Production code uses
                   agent.NewSpawner(); tests inject fakes.
internal/render/   Output layer: emit/format/diff/highlight. Couples to
                   stats via a 4-method Recorder interface defined here
                   (consumer-side). The Emitter is split into emit_bash,
                   emit_read, emit_edit, emit_write to keep tool-family
                   rendering isolated.
internal/stream/   Typed model of the claude stream-json event flow.
                   Two-pass decode: a small head struct routes on the
                   "type" discriminator, then the line is decoded into
                   the matching concrete type (Assistant, User, Result,
                   System, RateLimit, UnknownEvent).
internal/idgen/    Mints/inverts R-XXXX-XXXX requirement IDs from
                   wall-clock ms via an affine bijection mod 36^8.
internal/pricing/  Per-token USD cost table keyed by model alias
                   (haiku/sonnet/opus). Refresh from the Anthropic
                   pricing page when a new family ships.
internal/ui/       Output primitives: ANSI-aware status lines, byte/
                   time/number formatters, the spinner, and the Theme
                   that owns colour and width. No dependency on loop
                   or stream.
examples/          Example reqs/ trees (e.g. ralph-scoops).
```

The dependency arrows in the package graph all point inward:
`cmd/ralph -> loop -> {agent, render, stream, pricing, ui}`,
`render -> {stream, ui}`, `agent -> stream`. Nothing in `internal/*`
imports another internal package upward, and nothing depends on
`cmd/ralph`.

## How one iteration runs

The flow inside `loop.Run` (see `internal/loop/loop.go`) per
iteration:

1. **Validate config.** `loop.validate` rejects an empty Config or
   an out-of-range option before any subprocess is forked.
2. **Set up signal handling.** `loop.Run` wraps the parent context
   with `signal.NotifyContext` so the first SIGINT cancels the
   run; a second SIGINT triggers a hard exit via the
   `installForceQuit` helper.
3. **Spawn the agent.** Each iteration calls `Spawner.Spawn`
   (`internal/agent/claude.go:Spawner.Spawn`) to fork a fresh
   `claude` process in its own process group, with stdin/stdout
   pipes wired for the stream-json protocol.
4. **Send the kickoff.** `Session.Send` writes a single
   user-message envelope to the child's stdin (see
   `stream.WriteUserMessage`). The envelope carries the operator
   prompt assembled from `cmd/ralph/prompt.md` plus the per-run
   path substitutions (`{{REQS}}`, `{{WORKDIR}}`).
5. **Read events.** `internal/loop/iteration.go` pumps
   `Session.Events()` (a `*stream.Reader`) until a terminal
   `result` event arrives. Each line is decoded into a typed
   `stream.Event` and dispatched to the `render.Emitter`.
6. **Render and tally.** The emitter pretty-prints each event
   (`internal/render/emit*.go`) and updates the loop's stats
   accumulator through the four-method `render.Recorder`
   interface (block counts, LLM time, tool time, token usage).
7. **Inspect the result.** When the `result` event lands the loop
   reads its schema-constrained `status` field via
   `stream.ParseStatus`. `DONE` ends the run; `CONTINUE` (or any
   non-terminal value) loops back to step 3.
8. **Reap the session.** Either way, `Session.Close` shuts down
   stdin, drains stdout to EOF (with a 5-second backstop), and
   waits for the child to exit. A non-zero exit becomes a typed
   `agent.ExitError`.
9. **Repeat or exit.** The driver loops until DONE, the wall-clock
   budget exhausts (`ErrTimedOut`), the operator interrupts
   (`ErrInterrupted`), or a runtime error bubbles up.
10. **Write the summary.** On exit `loop.Run` prints the panel
    with start/end times, per-event counts, tokens, cost, and
    LLM/tool/other time breakdowns, and appends one JSON line per
    run to `~/.ralph-loops/results.jsonl`.

## Exit codes

Following Unix CLI convention. Source of truth:
[`cmd/ralph/main.go`](./cmd/ralph/main.go).

| Code | Meaning                                         |
|------|-------------------------------------------------|
| 0    | Success — the agent reported DONE.              |
| 1    | Runtime error — invalid config, subprocess failure, I/O error, etc. |
| 2    | Usage error — bad flags, missing WORKDIR, unknown subcommand. |

A second SIGINT during shutdown maps to `130` (the conventional
"terminated by SIGINT" status), via the second-Ctrl-C escape hatch in
`installForceQuit`.

## Glossary

- **Ralph loop** — the outer iteration: spawn claude, feed it the
  spec and the current workdir, observe a single focused change,
  ask CONTINUE or DONE, repeat. Named after the cadence rather
  than any particular component.
- **`reqs/`** — the project's spec directory. Read-only from
  ralph's perspective; the operator (with help from an interactive
  agent) edits it between runs.
- **Kickoff** — the first user-message ralph sends after spawning
  a claude session each iteration. Carries the operator prompt
  assembled from `prompt.md` with `{{REQS}}` / `{{WORKDIR}}`
  substituted in.
- **stream-json** — the newline-delimited JSON event protocol
  spoken by `claude --output-format stream-json`. Each line
  carries a `type` discriminator; ralph decodes them into the
  typed `stream.Event` family.
- **R-XXXX-XXXX requirement ID** — eight-character base-36
  identifier derived from the wall-clock minute via an affine
  bijection. Minted by `ralph newid`, decoded back to its instant
  by `ralph time-of`. The build agent references these in code
  comments and test names so the spec stays traceable.

## Requirement IDs

By default, requirements in the spec are tagged with IDs of the
form `R-XXXX-XXXX`. `ralph newid` mints a fresh one; `ralph
time-of R-XXXX-XXXX` decodes it back to the UTC instant it was
minted from. The build agent references these IDs in code
comments and test names so the spec stays traceable to the
implementation, and changing an ID is the signal to re-evaluate a
requirement from scratch on the next iteration.

## License

MIT — see [LICENSE](./LICENSE).
