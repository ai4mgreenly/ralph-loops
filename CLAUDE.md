# Ralph Loops

Go CLI that drives an iterative "ralph loop": invoked from a scaffolded
project root, spawns the `claude` CLI as a child with cwd set to the
project's `app-root/` directory, nudges it to read the standing
instructions in `app-root/AGENTS.md` (auto-loaded by claude), parses the
stream-json event flow, and repeats until the agent reports DONE, a
wall-clock budget elapses, or the operator interrupts.

- Module: `github.com/ai4mgreenly/ralph-loops` (Go 1.26, minimal external deps)
- Build: `make build` → `bin/ralph`. Test: `make test`. Install: `make install`
  (copies to `$HOME/.local/bin`).

## Scaffold shape

`ralph init PATH` produces:

```
PATH/
  helper/
    AGENTS.md            # spec-helper persona (human cd's here for `claude`)
  reqs/
    OVERVIEW.md
  app-root/
    AGENTS.md            # build-agent persona; ralph's standing instructions
    .ralph/              # state, created on first run
```

No AGENTS.md or CLAUDE.md sits at the project root, and no CLAUDE.md
symlink is scaffolded anywhere. The spec-helper persona lives in its
own sibling directory (`helper/`) so it stays off the build agent's
walk-up path: when ralph spawns the agent with cwd `app-root/`, claude
walks upward reading every AGENTS.md/CLAUDE.md it finds — a root-level
helper would leak conflicting instructions ("don't write code", "stay
out of app-root/") into the agent whose entire job is to write code in
`app-root/`. The human spec-author session is invoked from `helper/`
(e.g. `cd my-app/helper && claude`) so the helper AGENTS.md auto-loads
there.

`--reqs`, `--app-root`, and `--helper` rename the three subdirectories;
the chosen names are substituted into the AGENTS.md templates at
scaffold time. The loop subcommand is invoked from the project root
(the directory containing `helper/`, `reqs/`, and `app-root/`) and
accepts an optional `PROJECT_ROOT` positional that chdirs there first,
so `ralph /path/to/proj` is equivalent to `cd /path/to/proj && ralph`.
At runtime ralph itself stays at the project root and spawns the agent
with cwd set to `app-root/` via `cmd.Dir`; the matching `--reqs=PATH`
and `--app-root=PATH` loop flags (defaults `reqs` and `app-root`, both
project-root-relative) override the layout. The `unverified`
subcommand is unchanged: it is invoked by the agent from inside
`app-root/`, so its `--reqs` default stays `../reqs`. Missing
`app-root/AGENTS.md` is rejected at the project-root check.

## Layout

```
cmd/ralph/         Entry point and init logic. Embeds the skel templates
                   (OVERVIEW.md, AGENTS-helper.md, AGENTS-app.md) and
                   substitutes `{{REQS}}` / `{{APP_ROOT}}` / `{{HELPER}}`
                   at scaffold time.
                   Per-iteration kickoff is a one-liner pointing the agent
                   at AGENTS.md — no runtime templating in the binary.
                   Thin: parses flags, constructs loop.Config, calls loop.Run.
internal/loop/     The driver. Split by concern:
                     loop.go       Config, Run, signal plumbing
                     iteration.go  One iteration: kickoff, event pump, retry
                     stats.go      Token/cost tallies and panel rendering
                   Subprocess mechanics live in internal/agent; output
                   rendering lives in internal/render. loop owns lifecycle.
internal/agent/    Wraps the claude CLI behind Spawner/Session interfaces.
                   Owns os/exec, the user-message envelope, process-group
                   plumbing, and a typed ExitError. Production code uses
                   agent.NewClaude(); tests inject fakes.
internal/render/   Output layer: emit/format/diff/highlight. Couples to
                   stats via a 4-method Recorder interface.
internal/stream/   Typed model of the claude stream-json event flow.
                   Two-pass decode: RawEvent for routing, then concrete type.
internal/idgen/    Mints/inverts R-XXXX-XXXX requirement IDs from wall-clock
                   ms via an affine bijection mod 36^8.
internal/pricing/  Per-token USD cost table keyed by model alias
                   (haiku/sonnet/opus). Refresh from Anthropic pricing page.
internal/ui/       Output helpers: ANSI-aware status lines, byte/time/number
                   formatters. No dependency on loop or stream.
examples/          Example reqs/ trees (e.g. ralph-scoops).
```

## Conventions

- Exit codes: 0 success, 1 runtime error, 2 usage error.
- All user-facing output goes through `internal/ui`; ANSI honors `NO_COLOR`.
- `cmd/ralph/run` is the testable shape of `main` — args in, writers in,
  exit code out.
