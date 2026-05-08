# Ralph Loops

A small Go CLI that drives an iterative "ralph loop". `ralph` spawns
the `claude` CLI as a child, feeds it an operator prompt assembled
from your project's `reqs/` directory, parses the stream-json event
flow, and repeats until the agent reports DONE, a wall-clock budget
expires, or you Ctrl-C. The spec under `reqs/` is treated as
read-only input to the agent; each iteration reads it, picks the
smallest meaningful unit of work, makes one focused change in your
project source, runs whatever tests the project defines, and reports
back CONTINUE or DONE.

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

## Requirement IDs

By default, requirements in the spec are tagged with IDs of the
form `R-XXXX-XXXX`. `ralph newid` mints a fresh one; `ralph
time-of R-XXXX-XXXX` decodes it back to the UTC instant it was
minted from. The build agent references these IDs in code
comments and test names so the spec stays traceable to the
implementation, and changing an ID is the signal to re-evaluate a
requirement from scratch on the next iteration.
