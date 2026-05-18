# Pi Migration — Progress Ledger

Mutable progress ledger maintained by the orchestrator (see
`PI-MIGRATION-ORCHESTRATION.md`). The decision record
(`PI-MIGRATION-DECISIONS.md`) is the immutable spec; this file is the
how-far. **The repo is fact; this ledger is a claim** — on any
disagreement, the repo wins and this file is reconciled to it before
work proceeds.

- Module: `github.com/ai4mgreenly/ralph-loops` (Go 1.26)
- Rollback tag: `claude-restore` → `376c5f4` (do not delete)
- Baseline at ledger creation: commit `94913d4`, pre-migration claude
  code, `make build` + `make test` **green** (verified).
- pi reality-check: `pi 0.75.0` installed, `~/.pi/agent/auth.json`
  present (openai-codex), live `pi -p --mode json` probe confirms the
  event vocabulary and `Usage`/`agent_end` shapes match the decision
  record exactly. **No contradiction with the locked record.**

## Status legend

`pending` · `in-progress` · `done-verified` (repo proves it: build +
test green + the goal's own acceptance check).

## Why the spine is one goal

`internal/stream`'s public types are imported by `internal/render`,
`internal/loop`, and `internal/agent`. No ordering of those four
rewrites yields an intermediate state that compiles, so the pi
event-pipeline rewrite is **irreducibly one goal**, executed as an
ordered cluster of dispatches. The build is RED *between* dispatches
within G1 and GREEN only at G1 close. Per-dispatch acceptance inside G1
is package-local (`go build`/`go test ./internal/<pkg>/...`,
`gofmt`/`go vet`); the goal-level acceptance is full
`make build && make test`. `internal/pricing` deletion folds into G1
(stats.go consumes both `stream.Usage` and `pricing` inseparably;
decoupling pre-rewrite would need a throwaway `stream.Result` cost
shim the principles reject).

## Roadmap

### G1 — Stream-pipeline spine (the keystone) — `done-verified` (2026-05-17)

Implements Q1, Q2, Q3, Q5, Q6 (core), Q9, Q10, Q11, Q12, Q14a, Q15,
plus the Q6 `internal/pricing` deletion. Executed as a dispatch
cluster:

- **D1 — `internal/stream` rewrite + real fixtures.** pi event
  vocabulary (two-pass decode; concrete types for `session`,
  `message_end`, `tool_execution_start`, `tool_execution_end`,
  `agent_end`, optional `turn_end`; known-but-unused tallied;
  `UnknownEvent`); pi `Usage` shape
  (`input/output/cacheRead/cacheWrite/totalTokens/cost.{input,output,
  cacheRead,cacheWrite,total}`); `RALPH-STATUS` sentinel parser from
  `agent_end.messages[]` last assistant text; delete `SchemaJSON`,
  `Result`, Anthropic event/tool-name/sidecar types; pi kickoff
  envelope. Real-captured fixtures under `internal/stream/testdata/`
  (orchestrator captures, hands to subagent). Acceptance:
  `go build/test ./internal/stream/...` green, `gofmt`/`go vet` clean.
  Status: **`done` (package-local verified 2026-05-17)** — stream.go
  + 4 test files rewritten to pi vocabulary; old `session.jsonl`
  deleted; `gofmt`/`vet`/`build`/`test ./internal/stream/` green;
  whole repo RED as expected (G1 closes at D4). Exported API:
  `Reader`/`Event`/`Session`/`MessageEnd`/`ToolExecutionStart`/
  `ToolExecutionEnd`/`TurnEnd`/`AgentEnd`/`KnownEvent`/`UnknownEvent`/
  `PiMessage`/`ContentBlock`/`Usage`/`Cost`/`Status`/`ParseStatus`/
  `StatusFromAgentEnd`/`DecodeError`/`ErrUnknownType`/`ErrMalformed`.
- **D2 — `internal/agent` rewrite.** pi `buildArgs`
  (`-p --mode json --no-session --no-context-files
  --append-system-prompt <abs AGENTS.md> --no-extensions --no-skills
  --no-prompt-templates --no-themes
  --tools read,bash,edit,write,grep,find,ls`, optional
  `--provider`/`--model`/`--thinking` pass-throughs, `--raw`); stdin
  `/dev/null`; SIGTERM `Cmd.Cancel` then SIGKILL; drop `CLAUDE_*`
  env, `--engine`, ikigai paths; `ExitError` advisory-only (Q9).
  Depends on D1. Status: **`done` (package-local verified
  2026-05-17)**. Final API: `NewSpawner(string)*Spawner`;
  `Config{Prompt,SystemPromptFile,Provider,Model,Thinking,Tools,
  WorkDir string; Raw bool}`; `Session{Events()*stream.Reader;
  Send(string)error; Close()error}` (Send = documented no-op in
  one-shot pi, kept for the test seam); `ExitError{Code int;
  Signaled bool; Signal syscall.Signal}`. Kickoff=`Config.Prompt`
  (trailing positional), AGENTS.md abs path=`Config.SystemPromptFile`
  (`--append-system-prompt`, omitted if empty). `Cmd.Cancel`→SIGTERM
  to `-pgid`, `WaitDelay=10s`→SIGKILL. Probed real pi 0.75.0: argv
  works, stdin `/dev/null` works, terminates in `agent_end`.
- **D3 — `internal/render` B-lite.** Single generic tool renderer
  (Q11); keep `edit` diff via `args.oldText`/`newText`; delete
  per-tool `emit_bash/read/write` + the `tool_use_result` sidecar
  path + capitalized tool-name coupling; `Recorder` reshaped to pi
  `Usage`; spinner label de-claude. Depends on D1. Parallel with D2.
  Status: **`done` (package-local verified 2026-05-17)**. Edit-diff
  uses the real `args.edits[]` shape (not the record's flat
  `oldText/newText`). Per-tool `emit_bash/read/write` deleted; B-lite
  generic renderer; `message_end{role:toolResult}` dropped (Q10c).
  Single dispatch entry `Emitter.OnEvent(stream.Event)` (old
  `OnAssistant/OnUser/OnResult/OnSystem/OnRateLimit` removed);
  `DecodeStatus` removed (loop uses `stream.StatusFromAgentEnd`).
  Spinner label `claude`→`pi`. **Hard contract for D4** —
  `render.Recorder`: `TallyEvent(kind string)`,
  `TallyBlock(blockType string)`, `AddLLMTime(time.Duration)`,
  `AddToolTime(time.Duration)`, `TrackMessageUsage(u *stream.Usage,
  provider, model, stopReason string)`, `TrackToolOutcome(toolName
  string, isError bool)`.
- **D4 — `internal/loop` rewrite + `internal/pricing` deletion +
  `cmd/ralph` compiles.** iteration.go (drop `pumpStream` 3× retry,
  event-driven outcome, `RALPH-STATUS` → DONE/CONTINUE from
  `agent_end`, `errStreamEnded` fallbacks, exit-code advisory per
  Q9); stats.go (pi `Usage`, sum assistant `usage` over
  `agent_end.messages[]`, (provider, effectiveModel) attribution,
  drop context-window %); loop.go Config/options (drop claude
  flags/options); raw.go de-claude (Q12); agent.go seam; **delete
  `internal/pricing`**; make `cmd/ralph` build by removing references
  to deleted flags/symbols (full flag polish is G2). Depends on
  D1+D2+D3. Acceptance: **`make build && make test` GREEN** — closes
  the G1 RED window. Status: **`done` (verified 2026-05-17)** —
  `internal/loop` rewritten one-shot (pumpStream/retry/correction
  deleted), `internal/pricing` directory deleted, `cmd/ralph`
  compiles (dead claude flags removed; full flag redesign deferred
  to G2). `results.jsonl`/panel reshaped to pi (cost = pi float USD
  from `agent_end`; `by_model` (provider,model,api) breakdown;
  `partial` flag for no-`agent_end` fallback). loop API:
  `Run(ctx,Config,...Option)`; `Config{ReqsDir,WorkDir,Prompt,
  SystemPromptFile string; Theme}`; options
  `WithModel/Provider/Thinking/Version/Tools/Duration/Verbose/Raw/
  OutputLines/Now/ResultsHome/Spawner`; `Spawner`/`Session=
  agent.Session`; `ErrInvalidConfig/ErrInterrupted/ErrTimedOut`.

G1 done-verified ⇔ `make build` + `make test` green AND
fixture-driven tests prove: DONE sentinel → StatusDone, CONTINUE → 
StatusContinue, missing/garbled sentinel → CONTINUE, no `agent_end`
→ iteration error.

### G2 — CLI flag surface — `done-verified` (2026-05-17)

Verified independently: gofmt/vet clean, `make build`+`make test`
green (8/8), removed flags (`--engine`/`--effort`/`--config-dir`/
`--1m-context`/`--enable-claudeai-mcp-servers`/
`--dangerously-skip-permissions`) rejected as undefined. Added
`--provider`/`--thinking`; `--model` opaque pass-through; all three
forwarded only when set, no ralph default/validation (pi validates).
Default run → agent's full `read,bash,edit,write,grep,find,ls`
allowlist (literal owned by `internal/agent`, not duplicated in cmd);
operator `--tools` narrows. `kickoffPrompt` rewritten to a pure
RALPH-STATUS nudge (no "read AGENTS.md"). `usage.go` flag docs
updated. cmd-only slice; `internal/*` untouched.
Original spec below (kept for provenance):

Q7. `cmd/ralph/main.go`: delete `--engine`, `--config-dir`,
`--1m-context`, `--enable-claudeai-mcp-servers`,
`--dangerously-skip-permissions`, `--effort`; add `--provider`,
`--thinking`; keep `--model` as opaque pass-through (no parse, no
ralph-side default); default tools = explicit 7-built-in allowlist;
rewrite `kickoffPrompt` to the pure RALPH-STATUS nudge. Update
`main_test.go`. Acceptance: build+test green; flag presence/absence
asserted.

### G3 — Scaffold / templates / docs purge — `done-verified` (2026-05-17)

Verified independently: 8/8 tests green; scaffolded a fresh project
and confirmed the old uppercase `{"status":"DONE|CONTINUE"}` control
protocol is GONE, no `claude`/`stream-json`/`structured_output` in
generated files, and project `CLAUDE.md` has no stale
claude-CLI/`internal/pricing`/`stream-json` assertions. AGENTS-app.md
now uses the RALPH-STATUS bare-final-line sentinel + an explicit Q13
dual-vocabulary mapping (tool's lowercase `done/pending` →
sentinel's uppercase `DONE/CONTINUE`); AGENTS-helper.md walk-up
rationale dropped; CLAUDE.md walk-up paragraph collapsed to plain
role separation; `ralph unverified` workflow retained unchanged.
**Trap recorded (devlog):** a "purge JSON status" sweep must NOT
delete the retained lowercase `{"status":"done|pending"}` — that is
the Q13 `ralph unverified` tool contract, deliberately kept; only the
uppercase JSON *loop control* protocol was removed.
Original spec below (kept for provenance):

Q8 (+ Q13 dual-vocabulary hazard note). `skel/AGENTS-app.md`
(RALPH-STATUS bare-final-line replacing the `{"status":…}` protocol;
keep `ralph unverified`), `skel/AGENTS-helper.md` (drop walk-up
isolation rationale), `skel/OVERVIEW.md`, `cmd/ralph/usage.go`,
`init.go`/`reset.go` comments, project `CLAUDE.md` walk-up paragraph.
Update `init_test.go`/`reset_test.go`/`usage_test.go`. Acceptance:
build+test green.

### G4 — Stats panel enrichment — `done-verified` (2026-05-17)

Verified independently: `make build`/`make test` green (8/8),
`TestStats_ExactSumFromFixture` PASS, no `context-window` vestige.
Full Q6 panel: token breakdown; total cost + per-(provider,
effectiveModel) rule (single pair → concise row, multi → full
breakdown, never collapsed); turn/tool-call/tool-error counts;
stopReason headline + by-value tally when mixed; ralph wall-clock
retained. `results.jsonl` schema locked + doc-commented (tokens
object, cost number, `by_model` array always present, `partial`).
Q14c: synthetic `internal/loop/testdata/exact-sum.jsonl` with
dyadic-rational costs (honest float64 equality) asserting exact
token/cost sums across 2 (provider,effectiveModel) pairs. Captured
turns + stopReason inside stats.go only (no `iteration.go` change).
Original spec below (kept for provenance):

Q6 (full). Richer panel: tokens in/out/cacheRead/cacheWrite/total, pi
cost + (provider, effectiveModel) breakdown, turn/tool-call/tool-error
counts, stopReason; ralph wall-clock timings retained; `results.jsonl`
schema updated to the pi shape. Acceptance: build+test green;
known-fixed-number fixture asserts exact cost/token sums.

### G5 — Test corpus + live smoke + regen target — `done-verified` (2026-05-17)

Verified independently (from repo root, absolute paths): `make test`
8/8, `TestLive_PiSmoke` SKIPs cleanly ungated, `tool-error`/
`multi-turn` fixture tests pass, `go vet -tags pilive
./internal/agent/` compiles (gated live path not bitrotted),
`regen.sh` carries `</dev/null` (Q9, load-bearing, commented) and
explicitly excludes `exact-sum.jsonl`, `make fixtures` target wired.
Live smoke is triple-gated (build tag `pilive` + `RALPH_PI_LIVE=1` +
`pi` on PATH). Two new real fixtures captured by the orchestrator
(`tool-error.jsonl` isError:true; `multi-turn.jsonl` 2 read+edit
pairs, DONE) complete the Q14a corpus. Orchestrator note: `exact-sum`
lives in `internal/loop/testdata/` (not stream/) — subagent flagged
the brief's table error; exclusion enforced structurally regardless.
Original spec below (kept for provenance):

Q14b/c/d. Gated live smoke (`RALPH_PI_LIVE=1` / build tag,
auto-skip unauthed); documented `make` target to regenerate the
fixture corpus from live pi; remaining fixture cases (tool error,
multi-turn w/ tools, `edit` call). Acceptance: build+test green;
gated test skips cleanly without auth.

### G6 — Final sweep & migration close — `pending`

Whole-tree grep for residual `claude`/`anthropic`/`ikigai`/
`stream-json`/`structured_output`/`pricing`/`CLAUDE_`/`--effort`/
`--engine`; clean `examples/`/`tmp/` stragglers if claude-coupled;
confirm the three `PI-MIGRATION-*.md` are removable; final
`make build`+`make test`; closing devlog. Migration complete when
this is done-verified.

## Verification log

(append-only; newest last)

- `2026-05-17` — Ledger created (first loop entry). Baseline
  `94913d4` green. pi 0.75.0 reality-check passed. Roadmap G1–G6
  fixed. No goals closed yet.
- `2026-05-17` — Q9 #1 confirmed in practice: tool-using `pi -p
  --mode json` runs **hang past 300s without `< /dev/null`** and
  complete in seconds with it. The mandated stdin=`/dev/null` is
  load-bearing, not theoretical.
- `2026-05-17` — **Reality refines Q11 (decision intact, field path
  corrected).** Q11 prose says reconstruct the edit diff from pi
  `args.oldText`/`newText`. Real pi v0.75.0 `edit` tool args are
  `{"path":…,"edits":[{"oldText":…,"newText":…}]}` — an **array** of
  edit blocks; additionally `tool_execution_end.result.details.diff`
  carries a ready-made unified diff. The Q11 *decision* ("keep the
  edit diff; reconstruct from pi's edit args via the existing
  engine-agnostic diff+highlight code") is unchanged; only the
  record's assumed field path was wrong. Decision record left
  byte-identical (immutable). Resolution: `internal/stream` keeps
  tool `args`/`result` as `json.RawMessage` (decode-deferred), so the
  shape detail is purely a `render` (D3) concern; D3 iterates
  `args.edits[]` (each `oldText`/`newText`) through the existing diff
  path. Captured here + devlog so future sessions don't re-litigate.
- `2026-05-17` — Real pi fixtures captured under
  `internal/stream/testdata/`: `done.jsonl` (RALPH-STATUS: DONE),
  `continue.jsonl` (CONTINUE), `no-sentinel.jsonl` (no sentinel →
  CONTINUE default), `truncated.jsonl` (real capture minus terminal
  `agent_end` → iteration-error case), `tool-read.jsonl`
  (`tool_execution_*`, `read`), `tool-edit.jsonl` (`edit` with
  `args.edits[]`). Old claude fixture `session.jsonl` still tracked;
  D1 replaces it.
- `2026-05-17` — **G1 done-verified.** Spine executed as 4 dispatches
  (D1 stream+fixtures; D2 agent ∥ D3 render; D4 loop+pricing-del+
  cmd-compile). Independent orchestrator verification: `gofmt -l .`
  clean; `go vet ./...` clean; `internal/pricing/` absent and no
  `internal/pricing` references in any `.go`; `make build` exit 0;
  `make test` → all 8 packages `ok`. Q3 control flow proven green:
  stream `StatusFromAgentEnd` tests (done→DONE, continue→CONTINUE,
  no-sentinel→CONTINUE) + loop fixture tests (continue→re-iterate,
  truncated→`errStreamEnded`, truncated+cancelled-ctx→`ErrInterrupted`
  precedence). Next: G2 (CLI flag surface).
- `2026-05-17` — **G2 done-verified** (commit follows). Q7 flag
  surface + Q8 kickoffPrompt rewrite landed in `cmd/ralph` only.
  Independent verify: gofmt/vet clean, `make build`/`make test` 8/8
  green, `--engine`/`--effort` rejected as undefined flags. Next: G3
  (scaffold/templates/docs purge).
- `2026-05-17` — **G3 done-verified** (commit follows). Templates +
  CLAUDE.md purged of claude/stream-json/walk-up; RALPH-STATUS
  sentinel + Q13 dual-vocabulary mapping in AGENTS-app.md. My
  acceptance regex was over-broad (`"status":` matched the
  legitimately-retained Q13 tool vocabulary); subagent correctly
  prioritized locked Q13 over the check and flagged it; independent
  sharper grep confirmed only the old *uppercase* control protocol
  was removed. Next: G4 (stats panel enrichment).
- `2026-05-17` — **G4 done-verified** (commit follows). Full Q6
  panel + locked `results.jsonl` schema + Q14c deterministic
  exact-sum test (dyadic-rational costs, exact float64 equality, 2
  provider pairs). Independent verify green. Next: G5 (live smoke +
  fixture-regen make target + remaining fixture cases).
- `2026-05-17` — **G5 done-verified** (commit follows). Gated live
  smoke (Q14b, triple-gated), `make fixtures` regen target +
  documented script (Q14d, `</dev/null` load-bearing), Q14a corpus
  completed with `tool-error`/`multi-turn` real fixtures + tests.
  Orchestrator process note: an earlier verification run failed
  spuriously because a prior `cd …/testdata` persisted in the shell
  (violates `rules`: never cd); re-verified from repo root with
  absolute paths — all green. Henceforth absolute paths only. Next:
  G6 (final sweep & migration close).
