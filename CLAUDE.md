# Ralph Loops

Go CLI that drives an iterative "ralph loop": invoked from a scaffolded
project root, spawns `pi` (`@earendil-works/pi-coding-agent`) one-shot
per iteration (`pi -p --mode json`) with cwd set to the project's
`app-root/` directory. The build-agent persona in `app-root/AGENTS.md`
is injected as pi's system prompt via `--append-system-prompt` (with
`--no-context-files`, so pi does no AGENTS.md/CLAUDE.md discovery); a
short kickoff nudge is the positional prompt. ralph parses pi's native
JSONL event stream, and repeats until the agent's terminal `agent_end`
carries a `RALPH-STATUS: DONE` sentinel, a wall-clock budget elapses,
or the operator interrupts.

- Module: `github.com/ai4mgreenly/ralph-loops` (Go 1.26, minimal external deps)
- Build: `make build` → `bin/ralph`. Test: `make test`. Install: `make install`
  (copies to `$HOME/.local/bin`).

## Scaffold shape

`ralph init PATH` produces:

```
PATH/
  helper/
    AGENTS.md            # spec-helper persona (human cd's here for `pi`)
  reqs/
    OVERVIEW.md
  app-root/
    AGENTS.md            # build-agent persona; ralph's standing instructions
    .ralph/              # state, created on first run
```

No AGENTS.md or CLAUDE.md sits at the project root, and no CLAUDE.md
symlink is scaffolded anywhere. The two personas have opposite jobs —
the helper authors specs under `reqs/`, the build agent implements
them under `app-root/` — so each persona lives in its own directory
and ralph injects the build-agent persona explicitly as pi's system
prompt (`--append-system-prompt app-root/AGENTS.md` plus
`--no-context-files`); pi does no parent-directory walk-up, so
placement is plain role separation, not isolation engineering. The
human spec-author session is invoked from `helper/` (e.g.
`cd my-app/helper && pi`).

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

The `reset` subcommand is the inverse of init for `app-root/` only:
run from the project root, it removes every entry inside `app-root/`
(including `.ralph/` state and any nested `.git`) and rewrites the
templated build-agent `AGENTS.md`. It accepts the same `--reqs` /
`--app-root` / `--helper` flags as `init` and the same optional
`PROJECT_ROOT` positional as the loop. There is no prompt and no
`--force` — project-level git is the safety net. Refuses unless all
three subdirectories are present so it can't nuke a random cwd.

## Layout

```
cmd/ralph/         Entry point and init logic. Embeds the skel templates
                   (OVERVIEW.md, AGENTS-helper.md, AGENTS-app.md) and
                   substitutes `{{REQS}}` / `{{APP_ROOT}}` / `{{HELPER}}`
                   at scaffold time.
                   Per-iteration kickoff is a one-liner nudge ("perform
                   one iteration, then end with the RALPH-STATUS line") —
                   the persona is delivered via --append-system-prompt, so
                   the kickoff does NOT tell the agent to read a file.
                   No runtime templating in the binary. Thin: parses
                   flags, constructs loop.Config, calls loop.Run.
internal/loop/     The driver. Split by concern:
                     loop.go       Config, Run, signal plumbing
                     iteration.go  One iteration: kickoff, event pump,
                                   RALPH-STATUS parse (no correction
                                   retry — pi runs one-shot)
                     stats.go      Token/cost tallies and panel rendering
                                   from pi's per-turn usage
                   Subprocess mechanics live in internal/agent; output
                   rendering lives in internal/render. loop owns lifecycle.
internal/agent/    Wraps the pi CLI behind Spawner/Session interfaces.
                   Owns os/exec, the one-shot `pi -p --mode json` argv
                   (--no-session --no-context-files --append-system-prompt
                   --no-extensions/skills/prompt-templates/themes --tools),
                   /dev/null stdin, process-group plumbing, and a typed
                   ExitError. Production code uses agent.NewSpawner();
                   tests inject fakes.
internal/render/   Output layer: emit/format/diff/highlight. One generic
                   tool renderer plus the edit diff. Couples to stats via
                   a Recorder interface.
internal/stream/   Typed model of pi's native `-p --mode json` event
                   flow (session/message_end/tool_execution_*/agent_end,
                   …). Two-pass decode: route on "type", then decode the
                   concrete type; unknown types tolerated. The
                   DONE/CONTINUE control is a text sentinel parsed from
                   the terminal agent_end (StatusFromAgentEnd).
internal/reqs/     Reads spec requirement IDs and the agent's verified
                   ledger and computes the unverified set difference
                   (the read side of `ralph unverified`).
internal/idgen/    Mints/inverts R-XXXX-XXXX requirement IDs from wall-clock
                   ms via an affine bijection mod 36^8.
internal/ui/       Output helpers: ANSI-aware status lines, byte/time/number
                   formatters. No dependency on loop or stream.
examples/          Example reqs/ trees (e.g. ralph-scoops).
```

## Conventions

- Exit codes: 0 success, 1 runtime error, 2 usage error.
- All user-facing output goes through `internal/ui`; ANSI honors `NO_COLOR`.
- `cmd/ralph/run` is the testable shape of `main` — args in, writers in,
  exit code out.
