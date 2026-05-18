# Ralph Loops

A small Go CLI that drives an iterative "ralph loop": invoked from
the project root, it spawns `pi`
(`@earendil-works/pi-coding-agent`) one-shot per iteration
(`pi -p --mode json`) with cwd set to the project's `app-root/`
directory, injects the standing instructions from `app-root/AGENTS.md`
as pi's system prompt, parses pi's native JSONL event stream, and
repeats until the agent reports DONE, a wall-clock budget expires, or
you Ctrl-C.

A scaffolded project ships two `AGENTS.md` files in sibling
subdirectories: one in `helper/` for the interactive spec-helper
persona, and one in `app-root/` for the build agent ralph drives.
ralph injects the build-agent persona explicitly via
`--append-system-prompt` and runs pi with `--no-context-files`, so pi
does no AGENTS.md/CLAUDE.md discovery or parent-directory walk-up. The
two personas have opposite jobs — the helper authors specs under
`reqs/`, the build agent implements them under `app-root/` — so each
lives in its own directory as plain role separation, not isolation
engineering.

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
directly. Requires Go 1.26+ and the `pi` CLI on `$PATH`.

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

Scaffold a new project:

    ralph init my-app

This creates the following tree:

    my-app/
      helper/
        AGENTS.md            # spec-helper persona
      reqs/
        OVERVIEW.md
      app-root/
        AGENTS.md            # build-agent persona

`reqs/` holds the spec — read-only input as far as ralph is
concerned. `app-root/` is where the build agent works. `helper/` is
where the human (with an interactive `pi` session) sharpens the
spec. All AGENTS.md files are baked at scaffold time with the right
paths substituted in; the binary carries no runtime templating.

Override the directory names with `--reqs`, `--app-root`, and
`--helper`:

    ralph init --reqs=spec --app-root=src --helper=designer my-app

To sharpen the spec, `cd` into the `helper/` subdirectory and start
an interactive `pi` session. The spec-helper `AGENTS.md` is
auto-loaded by pi from that directory:

    cd my-app/helper
    pi
    > help me build out the spec

The helper interviews you about goals, audience, constraints, and
out-of-scope items, then proposes a file layout and starts writing
requirements with you. **You need at least a rough spec before
`ralph` has anything to build from** — the scaffold is just a
stub. Spend time here first; a sharper spec makes for a tighter
ralph run.

**A split-terminal setup is the most ergonomic way to work.** Put
the interactive `pi` session in one pane and a plain shell in
the other. Sharpen the spec on the left until it describes
something the build agent can actually start on, then kick off
`ralph` in the right pane:

    cd my-app
    ralph

`ralph` is run from the project root (the directory containing
`helper/`, `reqs/`, and `app-root/`). An optional `PROJECT_ROOT`
positional lets you point it at a project without `cd`'ing first —
`ralph /path/to/proj` is equivalent to `cd /path/to/proj && ralph`.
Layout
flags `--reqs=PATH` (default `reqs`) and `--app-root=PATH` (default
`app-root`) override the subdirectory names; both are project-root
relative. ralph itself stays at the project root and spawns the
agent with cwd set to `app-root/`, so the agent reads the spec from
`../reqs/` and writes state to `./.ralph/`. ralph carries no
provider/model/thinking defaults of its own — with no flags set, pi
uses its own `~/.pi/agent/settings.json` configuration — and it
iterates until the agent reports DONE or you interrupt. Each
iteration is bracketed by a banner so you can see the cadence;
per-event stream output appears underneath, with a `waiting for pi
(Xs)` spinner during long pauses. At the end of the run a summary
panel reports start/end times, per-event counts, tokens, pi's
authoritative cost (broken down by provider/model), and time spent
in LLM vs. tools. The same data is appended as one JSON line per run
to `~/.ralph-loops/results.jsonl` for later inspection.

To tune the run (all pass-throughs to pi; ralph sets no defaults):

    ralph --model=opus --duration=2h
    ralph --provider=anthropic --reqs=../shared-spec
    ralph --thinking=high --tools=read,bash,edit,write

See `ralph help` for the full flag list.

## The intended loop

ralph is designed to be one half of a two-loop workflow.

The **outer loop** is you and an interactive agent (`pi` or
similar) iterating on the spec under `reqs/`. After `ralph init`,
you start a `pi` session in `helper/` and `helper/AGENTS.md`
(the spec-helper persona) auto-loads. The helper
interviews you about goals, audience, hard constraints, and what's
out of scope, then proposes a file layout and writes individual
requirements. The discipline it holds to is **WHAT-and-WHY, not
HOW** — every requirement describes an observable property of the
finished system and, when useful, the reason it matters.
Implementation choices belong to the build agent on the inside
loop.

The **inner loop** is ralph driving pi against that spec,
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
cmd/ralph/         Entry point and embedded skel templates
                   (OVERVIEW.md, AGENTS-helper.md, AGENTS-app.md) used
                   by `ralph init`. Thin: parses flags, constructs
                   loop.Config, calls loop.Run. The per-iteration
                   kickoff is a one-liner nudge ("perform one
                   iteration, then end with the RALPH-STATUS line") —
                   the persona is delivered via --append-system-prompt,
                   so the kickoff does not tell the agent to read a
                   file; no runtime templating.
internal/loop/     The driver. Split by concern:
                     loop.go       Config, Run, signal plumbing
                     iteration.go  One iteration: kickoff, event pump,
                                   RALPH-STATUS parse (no correction
                                   retry — pi runs one-shot)
                     stats.go      Token/cost tallies, panel rendering
                                   from pi's per-turn usage
                     agent.go      Spawner/Session interfaces consumed here
                   Subprocess mechanics live in internal/agent; output
                   rendering lives in internal/render. loop owns lifecycle.
internal/agent/    Wraps the pi CLI behind Spawner/Session interfaces.
                   Owns os/exec, the one-shot `pi -p --mode json` argv,
                   /dev/null stdin, process-group plumbing, and a typed
                   ExitError. Production code uses agent.NewSpawner();
                   tests inject fakes.
internal/render/   Output layer: emit/format/diff/highlight. One generic
                   tool renderer plus the edit diff. Couples to stats
                   via a Recorder interface defined here (consumer-side).
internal/stream/   Typed model of pi's native `-p --mode json` event
                   flow. Two-pass decode: a small head struct routes on
                   the "type" discriminator, then the line is decoded
                   into the matching concrete type (Session, MessageEnd,
                   ToolExecutionStart, ToolExecutionEnd, TurnEnd,
                   AgentEnd, UnknownEvent). The DONE/CONTINUE control is
                   a text sentinel parsed from the terminal agent_end.
internal/reqs/     Reads spec requirement IDs and the agent's verified
                   ledger and computes the unverified set difference
                   (the read side of `ralph unverified`).
internal/idgen/    Mints/inverts R-XXXX-XXXX requirement IDs from
                   wall-clock ms via an affine bijection mod 36^8.
internal/ui/       Output primitives: ANSI-aware status lines, byte/
                   time/number formatters, the spinner, and the Theme
                   that owns colour and width. No dependency on loop
                   or stream.
examples/          Example reqs/ trees (e.g. ralph-scoops).
```

The dependency arrows in the package graph all point inward:
`cmd/ralph -> loop -> {agent, render, stream, ui}`,
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
   (`internal/agent/engine.go:Spawner.Spawn`) to fork a fresh
   one-shot `pi -p --mode json` process in its own process group,
   with stdin set to `/dev/null` and stdout wired for pi's native
   JSONL event stream. The build-agent persona from
   `app-root/AGENTS.md` is injected via `--append-system-prompt`
   (with `--no-context-files`, so pi does no file discovery). ralph
   itself stays at the project root; the child's working directory
   is set to `app-root/` via `cmd.Dir`.
4. **Deliver the kickoff.** The kickoff nudge ("perform one
   iteration of work, then end with the RALPH-STATUS line") is the
   trailing positional argument to `pi -p`; pi has no stdin
   user-message protocol, so `Session.Send` is a no-op (the prompt
   was already baked into the argv at spawn).
5. **Read events.** `internal/loop/iteration.go` pumps
   `Session.Events()` (a `*stream.Reader`) until the terminal
   `agent_end` event arrives. Each line is decoded into a typed
   `stream.Event` and dispatched to the `render.Emitter`.
6. **Render and tally.** The emitter pretty-prints each event
   (`internal/render/emit*.go`) and updates the loop's stats
   accumulator through the `render.Recorder` interface (block
   counts, LLM time, tool time, pi's per-turn token/cost usage).
7. **Inspect the result.** When `agent_end` lands the loop reads
   the `RALPH-STATUS` text sentinel from the last assistant
   message via `stream.StatusFromAgentEnd`. `DONE` ends the run;
   `CONTINUE` (or a missing/garbled sentinel, which defaults to
   CONTINUE) loops back to step 3.
8. **Reap the session.** Either way, `Session.Close` drains
   stdout to EOF (with a 5-second backstop) and waits for the
   child to exit; there is no stdin pipe to close (pi's stdin is
   `/dev/null`). A non-zero exit becomes a typed `agent.ExitError`.
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
| 2    | Usage error — bad flags, unknown subcommand, or no `app-root/AGENTS.md` found at the project root. |

A second SIGINT during shutdown maps to `130` (the conventional
"terminated by SIGINT" status), via the second-Ctrl-C escape hatch in
`installForceQuit`.

## Glossary

- **Ralph loop** — the outer iteration: spawn pi, feed it the
  spec and the current workdir, observe a single focused change,
  ask CONTINUE or DONE, repeat. Named after the cadence rather
  than any particular component.
- **`reqs/`** — the project's spec directory. Read-only from
  ralph's perspective; the operator (with help from an interactive
  agent) edits it between runs.
- **Kickoff** — the trailing positional prompt ralph passes to
  `pi -p` each iteration. The build-agent persona lives in
  `app-root/AGENTS.md`, injected as pi's system prompt via
  `--append-system-prompt`; the kickoff itself is a brief "perform
  one iteration, then end with the RALPH-STATUS line" nudge.
- **pi event stream** — the newline-delimited JSON event protocol
  pi emits in `pi -p --mode json`. Each line carries a `type`
  discriminator; ralph decodes them into the typed `stream.Event`
  family, terminating in `agent_end`.
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
