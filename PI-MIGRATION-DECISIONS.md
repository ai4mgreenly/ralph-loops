# Pi Migration — Decision Record

Migrating ralph-loops from driving the `claude` CLI to driving `pi`
**exclusively**. pi is provider/model agnostic; that flexibility is the
reason for the move.

- **Rollback point:** annotated git tag `claude-restore` → commit
  `376c5f4` ("Last known-good Claude-CLI-based ralph-loops, before the
  pi migration"). Local only; not pushed.
- **Target:** `pi` = `@earendil-works/pi-coding-agent@0.75.0` (canonical
  source: `github.com/earendil-works/pi` ≈ `github.com/badlogic/pi-mono`,
  `packages/coding-agent`). All facts below verified against that source
  at v0.75.0 (exact match to the installed binary) plus live probes.
- **Status legend:** **LOCKED** = decided with the user. **PROPOSED** =
  recommended, awaiting confirmation. **OPEN** = not yet grilled.

---

## The core problem

`internal/stream` decodes the **Anthropic wire format**. That only ever
worked because the prior `ikigai-cli` engine faithfully re-emitted
Anthropic-shaped events. **pi emits its own native format.** So the
wire-decode layer is the real migration — not flag substitution.

Three hard breaks, in priority order:

1. **No structured-output / `--json-schema` / forced-final-answer
   anywhere in pi** (verified in `--help`, README, `rpc.md`, and
   source — only the streaming stop event `{type:"done",reason:…}`
   exists). claude's `--json-schema` → `result.structured_output`
   drove ralph's entire DONE/CONTINUE control. Gone — must be
   redesigned.
2. **`internal/stream` is a rewrite, not a patch:** different event
   vocabulary, `toolCall`/`arguments`/lowercase tool names/`toolResult`
   role, different `usage` keys, and **no `result` event** — `agent_end`
   replaces it.
3. **`internal/pricing` is now wrong:** Anthropic-only table; pi is
   multi-provider and reports real USD cost itself.

What survives conceptually: the `Spawner`/`Session` test seam,
process-group signal handling, the per-iteration stateless loop, the
`ralph unverified` reqs workflow, the scaffold shape.

---

## Decisions

### Q1 — Scope of "exclusively pi" — **LOCKED**

Keep the `Spawner`/`Session` interface (it is the **test seam** — the
whole loop is exercised with injected fakes, no subprocess). Delete
everything else claude/ikigai/Anthropic-specific: claude
`buildArgs`/`buildEnv`, the `--engine` flag, ikigai-cli paths,
`internal/stream`'s Anthropic types, `internal/pricing`. One pi-only
concrete `Session` implementation. `claude-restore` is the rollback
path, so in-tree multi-engine machinery earns nothing.

### Q2 — Transport & process lifecycle — **LOCKED**

**One-shot `pi -p --mode json` per iteration**, not long-lived RPC.
Mirrors today's per-iteration fresh `Spawn`/`Close`; `agent_end`
replaces the `result` event as the terminal marker; stdin closed so pi
doesn't block. **Drop the in-process correction-retry** (`pumpStream`'s
3× loop) — one-shot mode can't send a correction into an exited
process, and the loop is designed to be safely repeatable. RPC's
bidirectional power (steer/follow-up/abort-mid-stream, queue modes,
`extension_ui_request` servicing) is unused by ralph's stateless loop.

### Q3 — DONE/CONTINUE signaling — **LOCKED**

**Text sentinel parsed from the terminal `agent_end` event.** The
build-agent persona instructs the agent to end its final reply with a
**bare last line**: `RALPH-STATUS: DONE` or `RALPH-STATUS: CONTINUE`.
ralph reads `agent_end.messages[]` → last `role:"assistant"` message →
concatenate `text` blocks → last match of
`^RALPH-STATUS:\s*(DONE|CONTINUE)\s*$` wins. Fallbacks: no `agent_end`
(stream died) ⇒ iteration error (today's `errStreamEnded`); `agent_end`
present but no parseable sentinel ⇒ **CONTINUE** (safe default).

Rationale: pi exposes **no forced-tool / `tool_choice`** mechanism
(`openai-codex-responses.ts:382` hardcodes `tool_choice:"auto"`), so an
extension tool could not *enforce* the signal either — same failure
mode, far more machinery and a pi-API coupling. Backstops make a missed
signal cost at most one wasted fresh iteration: wall-clock budget,
operator interrupt, and the reqs/`unverified` model as the real arbiter
of "done."

### Q4 — Persona delivery & isolation — **LOCKED**

**Strategy B:** `pi --no-context-files --append-system-prompt
<abs path to app-root/AGENTS.md>`; kickoff as the positional arg.
**Do not rely on AGENTS.md walk-up discovery.**

- `--no-context-files` suppresses **all** AGENTS.md/CLAUDE.md discovery
  — project walk-up *and* global `~/.pi/agent/AGENTS.md`
  (`resource-loader.ts:456`).
- pi has **no `CLAUDE_CONFIG_DIR` analog**; `~/.pi/agent` is derived
  from `homedir()` and cannot be relocated without breaking oauth in
  `~/.pi/agent/auth.json`. Explicit injection is the only way to
  guarantee the build agent sees **exactly `app-root/AGENTS.md` and
  nothing else**, deterministically.
- The file on disk is unchanged (`app-root/AGENTS.md`, scaffolded from
  the embedded `cmd/ralph/skel/AGENTS-app.md` template). Only the
  *delivery mechanism* changes; the kickoff is reworded as a
  consequence (the persona is now in the system prompt, not a file to
  "go read").
- **Known residual constraint (documented, not engineered around):**
  `--no-context-files` does **not** suppress a global
  `~/.pi/agent/SYSTEM.md` / `APPEND_SYSTEM.md`. These are *not*
  cross-project: project `.pi/SYSTEM.md` is read only from `cwd/.pi/`
  (= ralph's `app-root/`, which ralph owns and won't populate) with
  **no walk-up** (`resource-loader.ts:844`). The only non-suppressed
  vector is a single optional machine-global file the user authors
  themselves; absent by default. Operators running ralph should not
  keep a global pi `SYSTEM.md`.

### Q5 — Event-consumption granularity — **LOCKED**

Consume **settled events only** — `message_end`,
`tool_execution_start`/`tool_execution_end`, `turn_end`, `agent_end`
(and `session`). **Ignore `message_update` deltas** (at most, use them
to keep the spinner alive). ralph's renderer is whole-message oriented;
no live token streaming (same behavior as today with claude). Report
format is free to change — no backward compatibility required.

### Q6 — Metrics / cost accounting — **LOCKED**

- **Delete `internal/pricing`.** pi computes real USD cost itself,
  per-provider, in every assistant message
  (`usage.cost.{input,output,cacheRead,cacheWrite,total}`). pi's number
  is authoritative.
- **`agent_end` is the single source of truth for tallies.** On
  `agent_end`: sum `usage` over `messages[]` where
  `role=="assistant"` (usage is **per-turn, not cumulative**) →
  iteration tokens + cost; loop accumulates into a run total. Live
  scrollback still renders incrementally from `message_end` /
  `tool_execution_*`. Fallback if the process dies before `agent_end`:
  sum the assistant `message_end` usages seen so far (partial but
  honest).
- **Capture provider + effective model** (`responseModel ?? model`,
  plus `api` secondary) per iteration. Run-level summary **attributes
  tokens & cost grouped by (provider, effectiveModel)** — not collapsed
  — because pi is multi-provider and cost differs by provider. One pair
  ⇒ one row; varying ⇒ a breakdown.
- Rework `stats.go` + `render.Recorder` to pi's `Usage` shape; richer
  panel (tokens in/out/cacheRead/cacheWrite/total, pi cost + breakdown,
  provider/model, turn count, tool-call count, tool-error count,
  stopReason). Keep ralph's **own wall-clock** for LLM/tool/iteration
  timing (engine-agnostic).
- **Drop context-window %** — RPC-only (`get_state`/`get_session_stats`),
  not in json mode; reproducing it needs a model→context-size table,
  the exact hardcoded-table anti-pattern being removed.

### Q7 — CLI flag surface — **LOCKED**

- **Delete:** `--engine`, `--config-dir`, `--one-m-context`,
  `--claude-ai-mcp`, `--raw`, `--skip-permissions`, and all
  `CLAUDE_*` / `ENABLE_CLAUDEAI_*` env. (pi has **no permission
  system** — verified in source + probe; nothing to skip; a tool
  prompt cannot hang the loop. Capability is constrained pi-natively
  via `--tools` / `--no-tools` / `--no-builtin-tools`.)
- **`--effort` deleted.** Expose pi's native `--thinking`
  (`off|minimal|low|medium|high|xhigh`) verbatim. No lossy mapping
  table (ralph's `max` has no pi equivalent; pi validates the level
  itself).
- **Add `--provider`, keep `--model`, add `--thinking`** — all
  **optional pass-throughs with no ralph-side defaults**. ralph omits
  the flag unless the operator sets it; pi uses its own
  `~/.pi/agent/settings.json` defaults (currently
  `openai-codex` / `gpt-5.3-codex` / `medium`). ralph does not parse
  `--model` (pi's `provider/id` and `model:thinking` forms forwarded
  as-is).
- **Default tools = all 7 pi built-ins.** pi's full built-in set is
  `{read, bash, edit, write, grep, find, ls}` (`tools/index.ts:84`);
  pi's own default enables only `read/write/edit/bash` (grep/find/ls
  are read-only, off by default). ralph spawns with the **explicit
  full allowlist** `--tools read,bash,edit,write,grep,find,ls` so the
  build agent gets every built-in (search tools make codebase
  navigation effective; read-only, no determinism cost — built-ins are
  part of the controlled environment, unlike extensions/skills). The
  operator's `--tools` pass-through can still narrow it.
- **Auth:** ralph has zero auth code; pi owns auth via
  `~/.pi/agent/auth.json`. Build & test the whole migration on the
  current openai-codex oauth. Add anthropic/gemini logins only when
  exercising those providers (flagged at that point).

### Q8 — Scaffold / template rewrite — **LOCKED**

- **`skel/AGENTS-app.md`:** replace the entire `{"status":...}`
  JSON-output protocol with the Q3 `RALPH-STATUS:` **bare-final-line**
  sentinel. Keep the `ralph unverified` tool-call workflow (engine
  agnostic) — only the *signal format* changes.
- **Keep `helper/`** as a sibling directory, now a **non-isolation-
  critical** human spec-author workspace (`cd helper && pi`). Rewrite
  `skel/AGENTS-helper.md` to drop the "stay off the build agent's
  walk-up path" rationale (obsolete under Q4) and document plain role
  separation.
- **Rewrite `kickoffPrompt`** (`cmd/ralph/main.go:40`): drop "Read
  AGENTS.md if you have not already" (persona is now injected as the
  system prompt). Becomes a pure nudge, e.g. *"Perform one iteration of
  work as described in your instructions, then end with the
  RALPH-STATUS line."*
- Purge claude/stream-json wording from `usage.go`, `init.go` comments,
  `OVERVIEW.md`, and CLAUDE.md (the long walk-up-isolation paragraph
  collapses).

### Q9 — Process lifecycle: stdin, signal, outcome — **LOCKED**

1. **Child stdin = `/dev/null`** (immediate EOF) on every spawn —
   mandatory; `main.ts:55` `readStdin()` reads piped stdin to EOF and a
   never-closed pipe blocks pi forever (probe-confirmed).
2. **Interrupt/cancel → SIGTERM to the child's process group** (keep
   `setpgid`), then SIGKILL after a short grace period. pi print-mode
   handles **SIGTERM** (clean exit 143) and SIGHUP (129); it installs
   **no SIGINT handler** (SIGINT = abrupt Node default). Override Go's
   `exec.CommandContext` default (SIGKILL) with a `Cmd.Cancel` sending
   SIGTERM.
3. **Outcome is event-driven, not exit-code-driven** (replaces the
   claude "tolerate exit 0/1 if result seen" logic, `iteration.go:67`).
   Authoritative: observed `agent_end` + parsed `RALPH-STATUS`. Exit
   code is advisory/diagnostic only (0 clean / 1 startup error *or*
   turn `stopReason error|aborted` / 143 SIGTERM / 129 SIGHUP / ~130
   abrupt). Process ends before a parseable `agent_end`: ctx cancelled
   ⇒ run aborted (ctx.Err precedence, as today); not cancelled ⇒
   iteration error.

### Q10 — Decoder/dispatch contract + extension determinism — **LOCKED**

- **(a) Resilient decode, pi vocabulary.** Keep the two-pass pattern
  (route on `type`, then decode concrete type) and unknown-tolerance
  (log + continue). Decode to concrete types only the acted-on events:
  `session`, `message_end`, `tool_execution_start`,
  `tool_execution_end`, `agent_end` (+ `turn_end` optional boundary).
  Known-but-unused events (`agent_start`, `turn_start`,
  `message_start`, `message_update`, `compaction_*`, `auto_retry_*`,
  `queue_update`, `extension_*`) → tallied, not rendered. Unrecognized
  `type` → `UnknownEvent` (logged, decoding resumes). Preserves
  forward-compat against pi's fast-moving 0.x event set.
- **(b) `--no-extensions --no-skills --no-prompt-templates
  --no-themes` on the build-agent spawn.** Discovered extensions/skills/
  themes are ambient injection of exactly the kind Q4 closed (can add
  tools/flags/system-prompt text, emit `extension_ui_request` that
  blocks a headless loop, or spew `extension_error`). Escape hatch
  deferred (YAGNI).
- **(c) De-dupe the tool-result signal.** pi emits **both**
  `tool_execution_end` and a `message_end{role:"toolResult"}` for the
  same `toolCallId` (probe-confirmed). Use `tool_execution_start`/
  `tool_execution_end` as the sole tool channel (carries
  `toolName`/`args`/`result`/`isError`; feeds tool timing + Q6 counts);
  ignore the `toolResult` `message_end` for render/count.

### Q11 — Tool-result rendering — **LOCKED**

**B-lite.** A single generic tool renderer for every tool
(`tool_execution_start` → header: `toolName` + primary arg
[`path`/`command`/`pattern`]; `tool_execution_end` →
`result.content[]` text, error-styled on `isError`) — deletes the
per-tool renderers (emit_bash/read/write) and the entire claude
`tool_use_result` sidecar path. **Plus** keep the `edit` diff:
reconstruct from pi's `args.oldText`/`newText` via the existing
engine-agnostic `internal/render` diff+highlight code. Rationale: the
diff is the build loop's primary observability *output* (operator sees
each iteration's actual `-/+` changes), not an ambient input-extra —
no new ambient surface, little retained code. Delete
`internal/stream`'s capitalized tool-name constants + sidecar struct;
`format.go`'s primary-param map shrinks to pi keys
(`path`/`command`/`pattern`).

### Q15 — Session persistence — **LOCKED**

**`--no-session` on every spawn** (ephemeral). ralph never resumes
(`--continue`/`--session` unused; continuity is the filesystem/reqs,
fresh context per iteration). Persisted pi sessions would accumulate
unused files in `~/.pi/agent/sessions/`, add pointless IO, and
duplicate the JSONL stream ralph already consumes. Durable raw-stream
archival, if ever wanted, is a separate ralph-side feature (tee stdout
to `.ralph/`), not pi session persistence.

### Q12 — `--raw` debug passthrough — **LOCKED**

**Keep `--raw`, de-claude it.** `--raw` is engine-neutral operator
debug tooling (verbatim JSONL dump, no ralph decoration, one
iteration) — *not* a compatibility shim, so the "delete shims"
tendency doesn't apply. Its only claude coupling is the `cfg.Raw →
"--raw"` engine-arg passthrough (`engine.go:229-230`); delete that and
`Config.Raw`'s claude framing. `--raw` becomes "dump pi's raw
`-p --mode json` JSONL verbatim." Retained because it is the best
window into the riskiest layer (the new stream decoder) precisely
while it's being built; the engine-neutral tap/drain/`_ralph_kickoff`
stay. (raw.go's Close 0/1 tolerance is superseded by Q9.)

### Q13 — Agent-facing subcommands (`unverified`, `newid`) — **LOCKED**

**Out of scope, unchanged.** `ralph unverified` (`main.go:112,191`,
reqs-ledger based) and `ralph newid` (`internal/idgen`, R-XXXX-XXXX
minter) are **tools the ralph build-agent invokes**, co-located in the
single `ralph` binary purely so only one binary ships. Both are
engine-agnostic; neither touches the loop/engine/stream layers. No
code changes. The only migration interaction is the **consumer
instruction** in `AGENTS-app.md` (already covered by Q8): the agent
maps `ralph unverified`'s `{"status":"done"|"pending"}` →
`RALPH-STATUS: DONE|CONTINUE`. **Implementer hazard:** keep the two
"status" vocabularies distinct — `unverified`'s `done/pending` (tool
output) vs. the loop sentinel `RALPH-STATUS: DONE|CONTINUE` (Q3).
`newid`'s usage is unaffected by the engine swap. Design intent:
agent-facing tools live in the one binary by deliberate choice.

### Q14 — Testing strategy & fixture corpus — **LOCKED**

Test seam unchanged structurally (`fakeSpawner`/`fakeSession`/
`blackboxSession` implement `Spawn`/`Events() *stream.Reader`/`Send`,
fed canned JSONL through the real `stream.Reader`); only fixture
content and decoded types change.

- **(a) Real-captured pi fixtures**, not hand-authored, under
  `internal/stream/testdata/`, from actual `pi -p --mode json` runs.
  Cases: DONE sentinel; CONTINUE sentinel; missing/garbled
  sentinel→CONTINUE; no `agent_end` (truncated)→iteration error; tool
  success (`tool_execution_end`); tool error (`isError:true`);
  multi-turn w/ tools; an `edit` call (for the Q11 B-lite diff).
- **(b) Gated live smoke test** (`RALPH_PI_LIVE=1` env / build tag):
  real `pi -p --mode json` against live oauth, assert
  kickoff→`agent_end`→sentinel parsed. Auto-skipped in CI/unauthed;
  early warning for pi 0.x format drift.
- **(c) Structural/normalized assertions** — assert decoded structure,
  parsed status, aggregated tallies; never byte-identical rendered
  output (real captures carry volatile timestamps/UUIDs/token counts).
  For exact Q6 cost/token sums, one fixture with known fixed numbers.
- A documented `make` target regenerates the corpus from live pi when
  pi's format shifts.

---

## Status

All decisions **Q1–Q15 LOCKED**. No open questions. Ready to plan the
implementation against this record.

---

## Pi technical reference (verified at v0.75.0)

**`pi -p --mode json` framing:** newline-delimited JSON, one event per
line, terminating in `agent_end`, then process exits. Stdin must be
closed or pi blocks.

**`AgentEvent` union** (`agent/src/types.ts:403`):
`session` · `agent_start` · `agent_end{messages}` · `turn_start` ·
`turn_end{message,toolResults}` · `message_start{message}` ·
`message_update{message,assistantMessageEvent}` ·
`message_end{message}` · `tool_execution_start{toolCallId,toolName,args}`
· `tool_execution_update{…,partialResult}` ·
`tool_execution_end{toolCallId,toolName,result,isError}`.
**No `result` event.**

**Message/content shapes** (`ai/src/types.ts:224-302`):
- `UserMessage{role:"user",content,timestamp}`
- `AssistantMessage{role:"assistant",content:(text|thinking|toolCall)[],
  api,provider,model,responseModel?,usage,stopReason,timestamp}`
- `ToolResultMessage{role:"toolResult",toolCallId,toolName,content,
  isError,timestamp}`
- Blocks: `{type:"text",text,textSignature?}` ·
  `{type:"thinking",thinking,…}` ·
  `{type:"toolCall",id,name,arguments}`
- `Usage{input,output,cacheRead,cacheWrite,totalTokens,
  cost:{input,output,cacheRead,cacheWrite,total}}`
- `StopReason = stop|length|toolUse|error|aborted`

**RPC mode** (not used, for reference): bidirectional LF-JSONL;
commands keyed by `"type"` (`prompt`/`steer`/`follow_up`/`abort`/
`get_state`/`get_session_stats`/`get_last_assistant_text`/…); responses
`{type:"response",command,success,data?|error}`; idle via
`get_state.{isStreaming,pendingMessageCount}`.

**Context layering** (`resource-loader.ts`, `system-prompt.ts`): base
system prompt (binary) → `--system-prompt`/SYSTEM.md replaces it →
`--append-system-prompt`/APPEND_SYSTEM.md appends → context files
(`<project_context>`, gated by `--no-context-files`). SYSTEM.md/
APPEND_SYSTEM.md discovery: `cwd/.pi/` then `~/.pi/agent/` — fixed
paths, no walk-up.

**Flag parser:** `cmd/ralph` analog of pi's `src/cli/args.ts`. `-p`
consumes the next non-flag token as the prompt
(`session.prompt(initialMessage)`, `print-mode.ts:120`).

---

## Operational note (dev environment)

`/mnt/projects/ralph-loops/.git` is owned by user `ai4mgreenly` with
the **sticky bit** set; the interactive shell runs as `mgreenly`. git
*writes* (commit/tag) must run as the repo owner —
`sudo -n -u ai4mgreenly git …` (non-interactive sudo is available).
Working-tree file writes work directly (group `projects`,
group-writable). Git identity resolves as
`ai4mgreenly <ai4mgreenly@logic-refinery.com>`, matching repo history.

---

*Sources:* [earendil-works/pi](https://github.com/earendil-works/pi) ·
[coding-agent README](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/README.md) ·
[docs/rpc.md](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/rpc.md) ·
verified against local clone at `@earendil-works/pi-coding-agent@0.75.0`.
