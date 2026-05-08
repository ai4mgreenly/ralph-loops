# Ralph Loops

Go CLI that drives an iterative "ralph loop": spawns the `claude` CLI as a
child, feeds it an operator prompt built from a project's `reqs/` directory,
parses the stream-json event flow, and repeats until the agent reports DONE,
a wall-clock budget elapses, or the operator interrupts.

- Module: `github.com/ai4mgreenly/ralph-loops` (Go 1.26, minimal external deps)
- Build: `make build` → `bin/ralph`. Test: `make test`. Install: `make install`
  (copies to `$HOME/.local/bin`).

## Layout

```
cmd/ralph/         Entry point and embedded prompt.md. Thin: parses flags,
                   constructs loop.Config, calls loop.Run.
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
