# Collector

The collector periodically researches recent AI and technology news
via the `claude` CLI and persists new stories to the live store (see
`storage.md`).

## Cadence

The collector triggers runs based on **wall-clock time**, at four
fixed local-time slots: **05:00, 11:00, 17:00, 23:00** local —
every six hours, starting at 05:00.

The schedule is wall-clock anchored, not "every six hours from
start." If the process starts at 09:30 the first run is at 11:00,
not 15:30.

The cadence must be configurable so a developer can shorten it for
local iteration (e.g. an interval-based override that fires every N
seconds). The wall-clock schedule is the default.

On startup, the collector inspects the live store. If the most-
recent story's `collected_at` is older than six hours — or the
store contains no stories at all — the collector fires one run
immediately, then resumes the wall-clock schedule. If the most-
recent story is within the last six hours, the collector does
**not** fire a run on startup; it waits for the next scheduled
slot. A separate one-shot mode remains available for forcing an
immediate run during testing.

## Behavior of a run

Each run:

- Builds a **topics-to-avoid context** from the live store: the
  titles — only titles, no source URLs, no article bodies — of
  the **most-recent 100** stories (or all available stories if
  the live store holds fewer than 100). This mirrors magpie's
  pattern of bounded recent-history awareness.
- Invokes the `claude` CLI as a subprocess in structured-output
  mode (see `Invocation`) with a prompt that asks for fresh
  recent AI/tech news matching the editorial intent below, and
  includes the topics-to-avoid context as skip-instructions:
  claude is asked to gather fresh news that does **not**
  semantically duplicate any topic in that list. Titles vary
  across publishers covering the same story (e.g. "Anthropic
  launches Mythos Preview" vs "Anthropic's Mythos Preview goes
  public"), so semantic matching by claude is required — exact
  string match is insufficient.
- Reads the schema-validated story list from claude's response
  (see `Invocation`) and persists each story to the live store.
  The collector does not re-dedup claude's output; semantic
  deduplication is fully delegated to claude (see R-HF8P-XMWN).
- Returns 3–8 substantive stories per run when the news cycle
  warrants. An empty result on a quiet cycle is acceptable.
  Never fabricate stories or pad the list to hit a count.

## Editorial intent

Subject matter and source preferences mirror magpie's: research
papers and benchmarks; product and model launches; technical
writeups (postmortems, teardowns, engineering deep-dives); policy
and industry shifts. Prefer primary sources (official
announcements, the paper itself, the actual filing) over
aggregators and commentary. Style is terse and factual; no hype
adjectives.

The exact prompt sent to `claude` is the implementer's to author —
the intent above is sufficient guidance.

## Invocation

The collector invokes the `claude` CLI as a long-lived subprocess
in **streaming structured-output mode**, using a bidirectional
popen pattern. This gives the operator real-time visibility into
claude's research progress instead of black-box waiting for a
single envelope at the end of a multi-minute run.

The subprocess is spawned with bidirectional pipes — the collector
writes the kickoff user message to claude's stdin, then reads
claude's stdout line-by-line as events arrive. The argv includes:

- `--input-format stream-json` — the collector sends the user
  message as a single JSON line on claude's stdin, of the shape
  `{"type":"user","message":{"role":"user","content":[{"type":
  "text","text":"<prompt>"}]}}`.
- `--output-format stream-json` — claude emits one JSON event
  per line on stdout. Event types are `system`, `assistant`,
  `user`, `result`, `rate_limit_event`. The terminal `result`
  event carries `structured_output` (validated against
  `--json-schema`), `is_error`, `result` text, `usage`,
  `total_cost_usd`, `num_turns`, and `duration_ms`.
- `--verbose` — required when `--output-format stream-json` is
  set.
- `--replay-user-messages` — echoes the kickoff user message
  back as a `user` event so the captured stream is
  self-contained.
- `--json-schema <schema>` — JSON Schema describing the
  story-list shape. The schema is the implementer's to author,
  but it must describe at minimum: an array of stories, each
  with a `title` (string), an `article` (string, markdown body),
  and `citations` (array of `{title, url}` objects).
- `--model <model-id>`, `-p` (one-shot non-interactive), and
  any auth/permission flags the implementer needs.
- `--max-budget-usd <amount>` — per-invocation dollar cap.
  Without this, an agent doing exhaustive research can run for
  many minutes and many dollars per call (one observed failed
  run consumed $5.18 across 101 turns and 98 web searches before
  hitting `blocking_limit`). The exact cap is the implementer's
  to tune for cost vs. coverage trade-offs.

The prompt itself is bounded in size — see R-HBL0-SBOK — so
that growth in the live store does not push the prompt past
claude's context window.

The collector reads each event line as it arrives and processes
it immediately (logging at the appropriate level — see Logging).
Stories are extracted from the `result` event's
`structured_output` field. The subprocess is considered complete
once the `result` event has been observed (or stdout closes /
the process exits). The persisted raw stdout file
(R-H5HI-VGZ3) holds the **entire NDJSON event stream**
verbatim, not just the result event — an operator can replay any
past run's full event trail from disk.

When the `result` event has `is_error: true`, the run is treated
as a failure regardless of subprocess exit code, and the event's
`result` text — claude's own description of what went wrong — is
surfaced into the ERROR line so the operator sees it in the log
without having to read the persisted raw response.

Plain envelope mode (`--output-format json` alone, no
`--input-format stream-json`) is **not** acceptable. The
operator gets no progress visibility into multi-minute research
runs, and diagnosis of a stalled or empty run is artificially
limited to "last byte before the timeout" with no intermediate
event trail. Plain-text invocation (no `--output-format`, no
`--json-schema`, parsing the model's natural-language response)
is also not acceptable — the model's free-form output is not
reliably parseable by string-shape heuristics.

## Micro-test ladder

The bullets under `Tests` (those whose verification uses stubs and
synthetic stores) prove the collector's wiring is correct *in
isolation*. They do **not** prove the pipeline works end-to-end
against the real `claude` CLI.

The cycle of "implementation lands → operator runs `./launch.sh` →
discovers the prompt is malformed / the schema is wrong / claude
returned a shape we don't parse / the writer silently drops every
story" is long — each real `claude` invocation runs for minutes
and costs dollars per failed attempt — and has consistently
produced code that passes unit tests but fails in production. The
visible failure pattern is a `run end` line reporting
`count=0 raw=N rejected=0 dedup=0` (with `N > 0`): N items were
parsed but vanished without ever being attributed to rejection or
dedup, and there is no log evidence localizing where in the
pipeline they died.

The repository therefore ships a **micro-test ladder** — a
sequence of tightly scoped, individually-verifiable assertions,
ordered from cheapest/most-targeted to most expensive/most
end-to-end. Each test asserts exactly one fact. When a test
fails, the failing layer is unambiguous and every layer below
it is still proven. The ladder runs **without dedup
restrictions** (each end-to-end test uses an empty temporary
live store, so the topics-to-avoid context per R-HE0T-JV5Y is
empty and cannot mask a bug as "claude returned only duplicates").

The ladder organizes into six bands:

1. **Environment** — `claude` is on `PATH`, the binary runs,
   credentials resolve.
2. **Plain-text smoke** — claude can be invoked one-shot and
   returns a response.
3. **JSON envelope (`--output-format json`)** — claude honors
   `--json-schema` and returns the documented envelope shape.
4. **Stream-json (`--input-format stream-json --output-format
   stream-json --verbose`)** — claude accepts a user message on
   stdin and emits an NDJSON event stream containing the
   expected event types.
5. **Collector wiring** — the collector itself spawns claude
   with the full stream-json flag set, reads each event as it
   arrives, and emits the required INFO milestone lines (and,
   when opt-in is enabled, DEBUG per-event lines).
6. **End-to-end (no dedup)** — with an empty temporary live
   store and the production prompt+schema, the collector
   completes a real run that returns at least one story whose
   every field validates against the production schema, the
   arithmetic identity holds, and the persisted file
   re-validates against the same schema.

A failure at band N localizes the bug to that band. A failure
in band 6 with all of bands 1–5 passing means the production
prompt or schema (not the wiring or the CLI) is the problem.

**Authentication.** Tests that invoke `claude` rely on whatever
credentials the developer has already configured for the `claude`
CLI (an OAuth login or an `ANTHROPIC_API_KEY` in the environment).
A test reports *skipped* (not failed) only when `claude` itself
cannot authenticate — i.e. no usable credentials resolve.

**Cost.** OAuth and API-key invocations both hit the same
Anthropic API at the same rates; auth method does not change
runtime or cost. The end-to-end band with the production prompt
typically runs for minutes and costs dollars per invocation. The
default `./test.sh` does **not** run any ladder test (see
R-HLC7-UHM4).

The micro-test ladder is the canonical gate for "is the
collector actually working?" Passing all bands means a real
`./launch.sh` against prod will also work; iterations that pass
unit tests but fail any band are not done.

## Logging

The collector writes operational logs to stdout (errors to stderr).

**Line format.** Every log line follows the shape
`<timestamp> <level> <message>`:

- `<timestamp>` is an RFC 3339 / ISO-8601 instant in UTC with at least
  second precision (e.g. `2026-05-03T18:30:45Z`; fractional seconds
  are permitted).
- `<level>` is one of the literal tokens `INFO` (routine events),
  `ERROR` (failures), or `DEBUG` (per-event trace; opt-in only).
- `<message>` is the rest of the line — its content is governed by
  whichever event-specific bullet (startup decision, run start,
  claude start, etc.) produced the line.

The three components are separated by single ASCII spaces. No
logging-framework wrapper adds any other prefix or suffix (no `[`,
no `]`, no logger-name field, no source-file location). Within
`<message>`, the field/key shape (plain text, `key=value`, JSON)
is the implementer's choice; the bullets below pin only the
substrings that prove the event fired.

**Levels and what's at each.** The default operator view is `INFO`
+ `ERROR`. Those two levels alone must be sufficient to follow a
multi-minute claude run end-to-end — what triggered the run, what
claude is doing as it researches, why it ended, and where output
landed. `DEBUG` is the per-event trace of the claude subprocess
stream (one line per event read from claude's stdout); it is
**off by default** and must be enabled via a documented opt-in
mechanism (e.g. an environment variable or command-line flag).
When `DEBUG` is disabled, no `DEBUG` lines are emitted.

The collector logs at the following points:

- **Startup decision.** Before any sleep or scheduling, an `INFO`
  line states whether a run will fire immediately or be deferred to
  the next slot, and the reason — either the most-recent
  `collected_at` value, or the literal string `empty` when the store
  has no stories.
- **Run start.** When a run begins — the startup catch-up run or a
  regular wall-clock slot — an `INFO` line names the trigger
  (`startup` or the slot's zero-padded `HH:MM`).
- **Run completion.** When a run finishes, an `INFO` line states the
  count of new stories written and the relative path within the
  live store of each file added by that run. A zero-result run
  logs a count of `0` and no path entries.
- **`claude` lifecycle.** Each invocation of the `claude` CLI is
  bracketed by two `INFO` lines: one immediately before the call
  stating the model name and the size of the prompt sent to
  `claude`, and one immediately after the call returns stating the
  wall-clock elapsed time, the integer exit code, the count of
  stories present in the parsed response (`0` on parse failure or
  non-zero exit), and an explicit parse-outcome indicator (e.g.
  `parse=ok` / `parse=fail` / `parse=skipped`).
- **Per-turn progress (INFO).** Each `assistant` event observed in
  the claude stream produces one `INFO` line naming the 1-indexed
  turn number, the block types present in the event, and — for
  each `tool_use` block — the tool name (e.g. `WebSearch`,
  `WebFetch`). This is the operator's window into what claude is
  doing during the long middle of a research run.
- **Rate-limit notices (INFO).** Each `rate_limit_event` observed
  in the claude stream produces one `INFO` line naming the
  rate-limit type, status, and utilization percentage.
- **Result summary (INFO).** When the `result` event is observed,
  one `INFO` line summarizes the call: turn count, wall-clock
  duration, total cost in USD.
- **Per-event trace (DEBUG, opt-in).** When the documented
  opt-in mechanism is set, every event line read from claude's
  stdout produces one `DEBUG` log line. Lines that fail to parse
  as JSON are still emitted (tagged so they remain searchable).
- **Errors.** When the `claude` subprocess invocation fails, when
  its output cannot be parsed, or when a store write fails, an
  `ERROR` line names the failing step.

**Parse diagnostics.** Because zero-result runs are legal
(R-GKR8-DDDA), the existing log lines cannot tell apart "claude
returned nothing" from "claude returned items the parser couldn't
recognize" or "every item was dedup'd against the existing store."
The collector closes that gap by:

- Persisting `claude`'s raw stdout from every invocation to a
  known on-disk location, so the operator can read what claude
  actually said.
- Reporting a per-stage breakdown in the `run end` line
  (`raw` / `rejected` / `dedup` / `count`), so the operator can
  see exactly where items disappeared.
- Emitting one `INFO` log line per item the parser rejects, with
  the rejection reason and a short identifying snippet.

These functions must be wired into the collector's actual runtime —
the startup path, the scheduled-run path, and the error-handling
paths — not merely exposed as a callable module. A test that only
calls the logger functions directly is not sufficient evidence;
the test must drive the collector's real entrypoint (the function
the production binary's `main` calls to run the collector loop)
and assert the lines appear in *that* captured stdout/stderr.

## Tests

- [R-GLZ4-R53Z] The collector triggers a run at each of 05:00, 11:00, 17:00,
  23:00 local time when running on the default schedule.
- [R-GN71-4WUO] On startup, if the live store's most-recent story has a
  `collected_at` within the last six hours, the collector does
  not fire a run; it waits for the next scheduled slot.
- [R-GOEX-IOLD] On startup, if the live store's most-recent story has a
  `collected_at` older than six hours, the collector fires
  exactly one run immediately, then resumes the wall-clock
  schedule.
- [R-GPMT-WGC2] On startup with an empty live store (no stories), the
  collector fires exactly one run immediately, then resumes the
  wall-clock schedule.
- [R-GJJB-ZLML] The cadence is configurable via a documented mechanism (e.g.
  command-line flag, configuration file, environment variable).
- [R-HE0T-JV5Y] The prompt sent to `claude` includes the **titles** of the
  most-recent **100** stories from the live store as a
  topics-to-avoid context (or all available stories if the live
  store holds fewer than 100). The prompt does **not** include
  source URLs from those stories, does **not** include their
  article bodies, and does **not** include any other story
  content beyond titles. The prompt instructs `claude` to gather
  fresh news that does not semantically duplicate any topic in
  this list — claude does the dedup, since titles vary across
  publishers covering the same story and exact string matching
  on the collector side would miss those near-paraphrase
  duplicates. Verified by: the prompt material captured by the
  recording stub from R-H1TT-Q5R0 contains 100 titles drawn
  from the live store's most-recent stories (or all available,
  if fewer), contains no source URLs from those stories, and
  contains no article bodies.
- [R-HF8P-XMWN] The collector does not perform its own dedup of `claude`'s
  output. Semantic deduplication is fully delegated to `claude`
  via the topics-to-avoid context (R-HE0T-JV5Y); every story
  in the schema-validated `structured_output` list is persisted
  to the live store as-is, even if its title exactly matches a
  story already present. Verified by: with a live store seeded
  with 5 stories titled `A`, `B`, `C`, `D`, `E`, the recording
  stub returns three stories with titles `F`, `A`, `G`; after
  the run completes, all three of `F`, `A`, `G` appear in the
  live store and the run-end line reports
  `raw=3, rejected=0, dedup=0, count=3`.
- [R-GKR8-DDDA] A run that returns zero stories writes nothing and produces no
  errors.
- [R-HGGM-BENC] Each `claude` subprocess invocation includes the full
  stream-json flag set in its command-line arguments:
  `--input-format stream-json`, `--output-format stream-json`,
  `--verbose`, `--replay-user-messages`, and
  `--json-schema <schema-string>`. Verified by capturing the
  argv passed to the recording stub from R-H1TT-Q5R0: every
  one of these flags appears in the argv, and the value
  following `--json-schema` is a non-empty string that itself
  parses as a valid JSON object (the schema). Plain
  `--output-format json` (envelope mode without
  `--input-format stream-json`) in argv fails this bullet.
- [R-H958-0S76] The JSON Schema passed via `--json-schema` describes a top-level
  object whose payload includes an array of story objects, each
  with at minimum a `title` (string), an `article` (string), and
  a `citations` array whose items are objects with a `title`
  (string) and a `url` (string). Verified by parsing the schema
  from the captured argv and asserting that a sample object
  populated with representative story data validates against the
  schema.
- [R-HHOI-P6E1] The parser reads stories from the `structured_output` field of
  the `result` event in the NDJSON stream emitted by
  `claude --output-format stream-json`, not by string-searching
  the response for markdown code fences or attempting to
  JSON-decode the raw response from byte 0. Verified by: the
  recording stub emits a known NDJSON event stream — at minimum
  one `system` event, one `assistant` event, and one terminal
  `result` event whose `structured_output` payload contains
  three sample stories; after the run completes, the live store
  contains exactly those three stories (subject to dedup) and
  the run-end line reports `raw=3`.
- [R-HAD4-EJXV] The parse-outcome token in the post-call `INFO` line
  (R-H0LX-CE0B) reflects whether the parser successfully
  located and decoded the `structured_output` field from
  claude's JSON envelope. If the field is present and conforms
  to the schema, `parse=ok` is emitted (with `parsed=N`
  reflecting the count in the stories array — `0` is legitimate
  if claude returned an empty array). If the field is absent,
  malformed, or the envelope itself is not valid JSON, `parse=fail`
  is emitted **and** the collector emits an `ERROR`-level log line
  per R-GVQB-TB1J naming `parse` as the failing step. A run that
  produces a non-empty `response_bytes` but cannot extract any
  structured stories from it must not report `parse=ok parsed=0`.
- [R-HIWF-2Y4Q] When the `result` event in claude's NDJSON stream has
  `is_error: true`, the collector treats the invocation as a
  failure regardless of subprocess exit code: the post-call line
  reports `parse=fail`, and the ERROR log line required by
  R-GVQB-TB1J includes the event's `result` field verbatim
  (truncated to at most 500 characters if longer) in addition
  to naming the failing step. Verified by: the recording stub
  emits a `result` event with
  `{"type":"result","is_error":true,"result":"Prompt is too long"}`
  on stdout and exits 0; the collector emits an ERROR line
  containing the literal substring `Prompt is too long`.
- [R-HBL0-SBOK] The prompt sent to `claude` does not exceed **50,000 bytes**
  regardless of live-store size. The implementer's chosen
  technique for staying under the cap is unconstrained — bounding
  the dedup URL list to the most-recent N stories, hash-summarizing
  older entries, segmenting into multiple sub-runs, etc. — but the
  cap holds. Verified by: with a synthetic live store of 1,000
  stories, a triggered run's post-call `INFO` line reports
  `prompt_bytes` ≤ 50,000.
- [R-HCSX-63F9] The `claude` subprocess invocation includes the
  `--max-budget-usd <amount>` flag. The exact dollar value is the
  implementer's choice but must be a positive finite number;
  unbounded invocations are not acceptable. Verified by capturing
  the argv passed to the recording stub from R-H1TT-Q5R0:
  `--max-budget-usd` appears in the argv with a numeric value
  greater than zero.
- [R-GY64-KUIX] Every triggered run — startup catch-up or scheduled wall-clock
  slot — invokes the `claude` CLI as a subprocess exactly once.
  An end-to-end test that drives the collector's top-level
  entrypoint with a stand-in `claude` available to the run path
  (a stub binary on `PATH`, an injected subprocess factory, or
  any equivalent recording mechanism) observes exactly one
  invocation per triggered run, with the editorial-intent prompt
  material delivered to the stub on stdin or as an argument.
  An implementation that satisfies the zero-result rule
  (R-GKR8-DDDA) by returning early without ever invoking
  `claude` does **not** satisfy this bullet.
- [R-GQUQ-A82R] All collector log output is written to the process's stdout or
  stderr — not to a log file, not via syslog. With the binary's
  stdout and stderr captured to a buffer, every required log line
  below appears in that buffer.
- [R-HK4B-GPVF] Every log line emitted by the collector matches the regex
  `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z (INFO|ERROR|DEBUG) .+$`
  — an RFC 3339 / ISO-8601 UTC timestamp (with optional fractional
  seconds, terminated by `Z`), a single ASCII space, the literal
  token `INFO`, `ERROR`, or `DEBUG`, a single ASCII space, and a
  non-empty message body. No additional prefix or suffix from a
  logging framework appears on any line. `DEBUG` lines are emitted
  only when the documented opt-in mechanism (R-HQ7T-DKKW) is
  enabled; with the opt-in unset, no `DEBUG` lines appear in the
  output.
- [R-GS2M-NZTG] On startup, the collector emits exactly one log line whose level
  prefix is `INFO` and whose content includes either (a) the
  most-recent story's `collected_at` value as an ISO-8601 substring,
  or (b) the literal string `empty` when the store has no stories.
  The same line states whether the collector will fire a run
  immediately (substring `fire` or `immediate`) or wait for the next
  slot (substring `wait` or `defer`).
- [R-GTAJ-1RK5] At the start of every run — the startup catch-up run and every
  wall-clock-scheduled run — the collector emits exactly one log
  line whose level prefix is `INFO` and whose content includes the
  trigger: the literal `startup` for the catch-up run, or the slot
  in zero-padded `HH:MM` form (`05:00`, `11:00`, `17:00`, `23:00`)
  for a scheduled run.
- [R-GUIF-FJAU] At the end of every run, the collector emits exactly one log line
  whose level prefix is `INFO` and which contains both (a) the
  integer count of new stories written and (b) the relative path
  within the live store of each new file written by that run, one
  path per entry. A run that writes zero stories emits the same
  kind of line with count `0` and no path entries.
- [R-GVQB-TB1J] When a `claude` subprocess invocation fails (non-zero exit,
  timeout, or I/O error), when its output cannot be parsed, or when
  a store write fails, the collector emits a log line whose level
  prefix is `ERROR` and whose content names the failing step (e.g.
  `claude`, `parse`, `store`).
- [R-GZE0-YM9M] Each `claude` subprocess invocation is preceded by exactly one
  log line whose level prefix is `INFO` and whose content includes
  the model name passed to `claude` and the size of the prompt
  sent (e.g. character or byte count). The line is emitted before
  the subprocess is spawned, not after it returns.
- [R-H0LX-CE0B] Each `claude` subprocess invocation that returns an exit code
  (success or non-zero) is followed by exactly one log line whose
  level prefix is `INFO` and whose content includes the wall-clock
  elapsed time of the call, the integer exit code, the count of
  stories parsed from the response (`0` on parse failure or
  non-zero exit), and an explicit parse-outcome token (e.g.
  `parse=ok`, `parse=fail`, or `parse=skipped`). Invocations that
  never return an exit code (timeout, I/O error before spawn) are
  out of scope for this bullet — those are covered by the ERROR
  bullet (R-GVQB-TB1J).
- [R-HMK4-89CT] For every `assistant` event observed in claude's NDJSON
  stream during a run, the collector emits exactly one `INFO`
  log line. The line contains (a) a `turn=N` field with the
  1-indexed turn number (the count of `assistant` events seen
  so far in this invocation), (b) an enumeration of the block
  types present in the event's `message.content` (any of
  `text`, `thinking`, `redacted_thinking`, `tool_use`), and
  (c) for each `tool_use` block, the tool name (e.g.
  `WebSearch`, `WebFetch`). A run in which claude takes 12
  turns therefore produces exactly 12 such lines, in order.
- [R-HNS0-M13I] When the `result` event is observed in claude's NDJSON
  stream, the collector emits exactly one `INFO` log line that
  summarizes the call. The line includes (a) `turns=N` from
  the event's `num_turns`, (b) `duration=...` from the event's
  `duration_ms`, and (c) `cost=$X.XXXX` formatted from the
  event's `total_cost_usd` (or the literal token `cost=n/a` if
  the field is absent). This line is distinct from — and
  emitted before — the post-call `INFO` line required by
  R-H0LX-CE0B.
- [R-HOZW-ZSU7] For every `rate_limit_event` observed in claude's NDJSON
  stream during a run, the collector emits exactly one `INFO`
  log line. The line contains the rate-limit type, status, and
  utilization percentage drawn from the event's
  `rate_limit_info` payload.
- [R-HQ7T-DKKW] The collector exposes an opt-in mechanism (an environment
  variable, a CLI flag, or both — implementer's choice) that
  enables `DEBUG`-level per-event logging. The mechanism is
  documented by name in the project's README. With the mechanism
  unset, no `DEBUG` lines appear in the collector's stdout/stderr;
  with it set to a truthy value, `DEBUG` lines are emitted per
  R-HRFP-RCBL.
- [R-HRFP-RCBL] When the opt-in from R-HQ7T-DKKW is enabled, the
  collector emits exactly one `DEBUG` log line for every line
  read from the claude subprocess's stdout (including event
  lines that fail to parse as JSON — those are emitted with a
  `parse=raw` tag so they remain searchable). The count of
  `DEBUG` lines emitted during a single claude invocation
  equals the count of newline-terminated lines read from
  claude's stdout for that invocation.
- [R-GWY8-72S8] An end-to-end test drives the collector's top-level entrypoint —
  the same function the production binary's `main` invokes to run
  the collector loop — and, in the captured stdout/stderr of that
  entrypoint (not from a direct logger call), observes all four
  required log-line kinds: the startup-decision line
  (R-GS2M-NZTG), at least one run-start line for a triggered run
  (R-GTAJ-1RK5), at least one run-end line for the same run
  (R-GUIF-FJAU), and at least one ERROR line (R-GVQB-TB1J)
  produced when the underlying `claude` invocation, output parser,
  or store writer is induced to fail. Tests that invoke the logger
  functions directly do not satisfy this bullet; the assertion is
  on lines emitted as a side effect of the collector's run path.
- [R-H1TT-Q5R0] When the production `ralph-scoops` binary is launched as a child
  process of a test (via `exec.Command` against the compiled
  binary, with a writable temp directory as its working directory
  and a recording `claude` shell script first on `PATH`), a
  triggered run causes the binary to actually fork+exec that script
  exactly once. The recording script — typically one that writes
  its argv and stdin to a known file, then exits — must be invoked
  as a real OS-level child process during the binary's run.
  Stubbing `claude` via Go function pointers, interfaces, or any
  other in-process mechanism inside `main.go` does **not** satisfy
  this bullet; only a real fork+exec of the `claude` executable
  resolved through `PATH` does. This closes the loophole left by
  the "or any equivalent recording mechanism" wording in
  R-GY64-KUIX — for production wiring, the equivalent mechanism
  must itself be a real subprocess.
- [R-H31Q-3XHP] Under the same end-to-end harness as R-H1TT-Q5R0, the
  pre-call `INFO` line's prompt-size field (per R-GZE0-YM9M)
  reports a value of at least **500 bytes**, and the recording
  `claude` stub captures stdin or argv whose byte length is also at
  least 500. A `prompt_bytes` value below this floor — or stub-
  captured input below this floor — indicates that the editorial-
  intent prompt material from the `Editorial intent` section is not
  actually being constructed and sent to `claude`, and fails this
  bullet.
- [R-H5HI-VGZ3] Each `claude` subprocess invocation, regardless of exit code or
  parse outcome, persists `claude`'s captured stdout to a file on
  disk. In addition to the fields required by R-H0LX-CE0B, the
  post-call `INFO` line includes (a) `response_bytes=N` reporting
  the byte length of the captured stdout and (b) `raw=<path>`
  naming the relative path of the persisted file. The persisted
  file exists after the line is emitted and its byte length equals
  the reported `response_bytes`. The on-disk path scheme (e.g.
  `./debug/claude/<UTC-timestamp>.txt`) is the implementer's choice;
  only the existence and the in-line path field are pinned.
- [R-H6PF-98PS] In addition to the fields required by R-GUIF-FJAU, the
  `run end` `INFO` line includes three additional integer count
  fields: `raw=M` (items extracted by the parser from claude's
  response, before any validation or post-fetch dedup),
  `rejected=K` (items the parser identified but rejected for
  failed schema validation — missing required field, malformed
  structure, etc.), and `dedup=L` (items dropped by any
  collector-side dedup step the implementer chooses to add as a
  safety net; under the delegated-to-claude dedup design of
  R-HE0T-JV5Y/D7RRVO8 this count is typically `0`). The
  arithmetic identity must hold: `raw == count + rejected + dedup`.
- [R-H7XB-N0GH] For every item the parser rejects from claude's response —
  whether for failed validation or for matching an
  already-covered source URL — the collector emits exactly one
  `INFO`-level log line. The line includes (a) a short
  rejection-reason token (e.g. `missing-title`, `malformed-url`,
  `dedup-hit`, or similar implementer-chosen vocabulary) and
  (b) a short identifying snippet of the rejected item — the
  source URL when available, or the first 80 characters of the
  item's raw text otherwise.
- [R-H49M-HP8E] The binary's `main` runs the wall-clock scheduling loop
  described in the `Cadence` section, not a single `RunOnce` call
  followed by exit. Verified by: launching the binary under the
  cadence-override mechanism (R-GJJB-ZLML) configured to fire
  every N seconds (small enough for a test, e.g. 1–2 seconds),
  with a recording `claude` stub on `PATH`; within a bounded
  observation window (e.g. 5 seconds) the binary's stdout/stderr
  contains at least two `INFO` `run start` lines (per
  R-GTAJ-1RK5) for two different triggers — typically the
  `startup` trigger followed by an interval-driven trigger — and
  the binary remains alive between them rather than exiting after
  the first run.
### Micro-test ladder

The micro-test ladder runs via a dedicated runner — not via the
default `./test.sh` (see R-HLC7-UHM4). Every test below relies on
whatever credentials the developer's `claude` CLI is already
configured with (an OAuth login or `ANTHROPIC_API_KEY`); if no
usable credentials resolve, the test reports *skipped*, never
*failed*. Tests run in ID order (Band 1 first, Band 6 last); when
a test fails, the runner reports the failure and may continue or
stop at its discretion, but every test must report PASS / SKIP /
FAIL on a single output line so an operator scanning the runner's
output can see exactly which checks held.

**Band 1 — Environment.**

- [R-HSNM-542A] **M01 — `claude` on PATH.** `command -v claude` exits 0 and
  prints a non-empty path. With this missing, every subsequent
  test is short-circuited to *skipped*.
- [R-HTVI-IVSZ] **M02 — `claude --version`.** `claude --version` exits 0
  within 10 seconds and prints non-empty stdout that contains a
  semver-shaped substring (regex `\d+\.\d+\.\d+`).
- [R-HV3E-WNJO] **M03 — `claude --help` documents the stream-json flags.**
  `claude --help` exits 0 within 10 seconds and its combined
  stdout/stderr contains all four literal substrings:
  `--input-format`, `--output-format`, `--json-schema`,
  `--verbose`. If any is missing, the installed `claude` binary
  predates the API the collector relies on and the test fails.
- [R-HWBB-AFAD] **M04 — Credentials resolve.** A trivial authenticated
  invocation (e.g. `claude -p hi --output-format text
  --model haiku`) exits 0 within 30 seconds and produces
  non-empty stdout. A non-zero exit whose stderr indicates an
  auth/login problem reports *skipped*; any other non-zero exit
  reports *failed*.

**Band 2 — Plain-text smoke.**

- [R-HXJ7-O712] **M05 — Ping/pong.** `claude -p "Reply with exactly the word
  pong and nothing else." --output-format text --model haiku`
  exits 0 within 30 seconds and its trimmed lowercased stdout
  equals the literal string `pong` (or contains it as a single
  whitespace-isolated token). Same skip semantics as M04.
- [R-HYR4-1YRR] **M06 — Model selection honored.** Two M05-shaped calls run
  back-to-back, one with `--model haiku` and one with
  `--model sonnet`. Both exit 0; both produce non-empty stdout.
  The point is to prove `--model` is accepted and resolved; the
  test does not attempt to verify *which* model answered.

**Band 3 — JSON envelope.**

- [R-HZZ0-FQIG] **M07 — Trivial JSON envelope returns and parses.** A call
  with `--output-format json --json-schema '{"type":"object",
  "properties":{"answer":{"type":"string"}},"required":
  ["answer"]}'` and a prompt asking for a one-word answer exits
  0 within 60 seconds, and its stdout parses as a single JSON
  object. Skip semantics as M04.
- [R-I16W-TI95] **M08 — Envelope shape.** The parsed envelope from M07 has,
  at top level: `is_error` (boolean), `result` (string), and
  `structured_output` (object). Any missing field fails this
  bullet.
- [R-I2ET-79ZU] **M09 — Structured output validates.** The
  `structured_output` payload from M07 conforms to the supplied
  schema — i.e. it is a JSON object with an `answer` field
  whose value is a string. Verified by re-validating the
  payload against the same schema string passed on the command
  line.
- [R-I3MP-L1QJ] **M10 — Impossible schema surfaces `is_error`.** A call with
  a deliberately impossible schema (e.g. one requiring a
  property whose value is a string of exact length 9999, with
  a prompt that asks for that property) returns an envelope
  with `is_error: true`, and the envelope's `result` field is
  a non-empty string. The test prints `result` so the operator
  can see what claude reported. Same skip semantics as M04.

**Band 4 — Stream-json subprocess.**

- [R-I4UL-YTH8] **M11 — Stream-json popen accepts stdin.** `claude
  --input-format stream-json --output-format stream-json
  --verbose -p --model haiku --json-schema '<trivial>'` is
  spawned with bidirectional pipes; the test writes one
  `{"type":"user",...}` line to stdin (matching ralph's
  `send_user` shape) and closes stdin. Within 60 seconds the
  subprocess exits 0 and its stdout contains at least one
  newline-terminated line.
- [R-I62I-CL7X] **M12 — Stdout is NDJSON.** Every newline-terminated line on
  the M11 subprocess's stdout parses as a JSON object, and
  every parsed object has a string `type` field. Lines that
  do not parse as JSON or that lack a `type` field fail this
  bullet.
- [R-I7AE-QCYM] **M13 — Expected event types observed.** Across all event
  lines from the M11 subprocess: at least one event has
  `type == "system"`, at least one has `type == "assistant"`,
  and exactly one has `type == "result"`. The `result` event is
  the last event line on stdout (no further events appear after
  it).
- [R-I8IB-44PB] **M14 — Result event carries valid `structured_output`.** The
  `result` event from M13 has a `structured_output` field
  (object) that conforms to the trivial schema passed via
  `--json-schema`. Same validation logic as M09 but against
  the streamed event.

**Band 5 — Collector wiring.**

- [R-I9Q7-HWG0] **M15 — Collector spawns claude with the full stream-json
  flag set.** Under the recording-stub harness from
  R-H1TT-Q5R0 (real fork+exec of a `claude` shim on `PATH`),
  the argv captured by the stub on a triggered run contains
  every one of `--input-format stream-json`,
  `--output-format stream-json`, `--verbose`,
  `--replay-user-messages`, and a `--json-schema` flag whose
  value parses as a JSON object. The stub's captured stdin
  contains a single line whose JSON parses to an object with
  `type == "user"` and a non-empty `message.content[0].text`.
  This bullet is the wiring proof for R-HGGM-BENC.
- [R-IAY3-VO6P] **M16 — INFO per-turn lines emitted.** Under the
  recording-stub harness, with the stub replaying a captured
  NDJSON stream that contains exactly 4 `assistant` events
  followed by a `result` event with `num_turns: 4`, the
  collector's stdout/stderr contains exactly 4 INFO lines
  matching R-HMK4-89CT (one per assistant event), in order,
  each carrying its 1-indexed `turn=` field, *and* exactly 1
  INFO line matching R-HNS0-M13I (the result summary). No
  `DEBUG` lines appear unless the opt-in is set.
- [R-IC60-9FXE] **M17 — DEBUG opt-in produces one line per event.** Under
  the same harness as M16 but with the documented DEBUG
  opt-in (R-HQ7T-DKKW) enabled, the collector's
  stdout/stderr contains exactly one `DEBUG` line per event
  line in the replayed stream. With the opt-in unset, zero
  `DEBUG` lines appear regardless of stream length.

**Band 6 — End-to-end (no dedup).**

- [R-IDDW-N7O3] **M18 — Empty store + production prompt invokes claude.** A
  test drives the collector's top-level entrypoint (the same
  function the production binary's `main` invokes) against an
  **empty** temporary live store, using the production prompt
  and schema the collector actually uses. The pre-call `INFO`
  line (R-GZE0-YM9M) reports `prompt_bytes >= 500`. The run
  performs a real fork+exec of `claude` (not a stub). Same
  skip semantics as M04. Per-test runtime budget: 15 minutes.
- [R-IELT-0ZES] **M19 — At least one well-formed story persists.** After
  the M18 run completes, the post-call `INFO` line reports
  `parse=ok` with `parsed >= 1`, the run-end `INFO` line
  reports `count >= 1`, and at least one new story file
  exists on disk in the temporary store. Each persisted story,
  re-read from disk, has a non-empty `title` (string), a
  non-empty `article` (string), and at least one entry in
  `citations` whose own `title` (string) and `url` (string)
  are non-empty.
- [R-IFTP-ER5H] **M20 — Every citation URL is a valid absolute http(s)
  URL.** Every `url` value across every citation of every
  story persisted by the M18 run parses as an absolute URL
  (scheme `http` or `https`, non-empty host, non-empty path
  permitted but optional). A relative URL or a non-http(s)
  scheme fails this bullet.
- [R-IH1L-SIW6] **M21 — Arithmetic identity holds and matches disk.** From
  the run-end `INFO` line emitted during the M18 run (read
  back from the captured stdout, not synthesized), the
  identity `raw == count + rejected + dedup` holds, and
  `count` equals the integer count of new story files
  written under `./stories/` (or its temp-store equivalent)
  during the run. A run-end line of `count=0 raw=N
  rejected=0 dedup=0` with `N > 0` — the regression that
  motivated this ladder — fails this bullet.
- [R-II9I-6AMV] **M22 — Silent drops are impossible.** Under a stubbed
  variant of the M18 harness in which the recording stub
  emits a `result` event with `structured_output` containing
  4 stories and the collector is configured to reject 2 of
  them (e.g. by injecting a synthetic schema-validation
  failure on those 2), the run-end line reports `raw=4
  rejected=2 dedup=0 count=2`, **and** exactly 2 INFO lines
  matching R-H7XB-N0GH appear in the captured stdout/stderr
  — one per rejected item, each with its rejection-reason
  token and identifying snippet. With the rejection injection
  disabled, `rejected=0` and zero R-H7XB-N0GH lines appear.
- [R-IJHE-K2DK] **M23 — Persisted file re-validates against the schema.**
  At least one persisted file from the M18 run, re-read from
  disk and re-parsed back into the in-memory story shape, is
  re-validated against the same JSON Schema string the
  collector passed to `claude` via `--json-schema`. The
  validation succeeds. This proves end-to-end fidelity: the
  shape claude was constrained to produce is the shape that
  ends up on disk.
- [R-IN53-PDLN] **M24 — Result event carries usage and cost.** The
  `result` event captured during the M18 run (recoverable
  from the persisted raw NDJSON file per R-H5HI-VGZ3) has
  a `total_cost_usd` field whose value is a finite number
  greater than zero, a `num_turns` field whose value is a
  positive integer, and a `duration_ms` field whose value is
  a positive integer. A `result` event missing any of these
  three fields fails this bullet.
- [R-IQSS-UOTQ] **M25 — INFO turn-line count equals stream `assistant`-event
  count.** From the captured stdout/stderr of the M18 run,
  the count of INFO lines matching R-HMK4-89CT
  (assistant-turn lines) equals the count of newline-
  terminated lines in the persisted raw NDJSON file
  (R-H5HI-VGZ3, validated well-formed by M27) whose JSON has
  `type == "assistant"`. The raw file is the canonical record
  of what claude streamed; a mismatch indicates the collector
  is dropping `assistant` events silently between the stream
  and the log. Note: this invariant deliberately does **not**
  compare against the `result` event's `num_turns` — that
  field counts agentic turn cycles (model→tool→model loops),
  not raw `assistant` events on the wire, and the two are
  routinely unequal.
- [R-IOD0-35CC] **M26 — DEBUG line count equals stream-line count.** Under
  the M18 harness re-run with the DEBUG opt-in enabled, the
  count of `DEBUG` lines emitted by the collector equals the
  count of newline-terminated lines in the persisted raw
  NDJSON file (R-H5HI-VGZ3) for that invocation.
- [R-IPKW-GX31] **M27 — Persisted raw file holds the full NDJSON stream.**
  The raw file path emitted by R-H5HI-VGZ3 for the M18 run
  exists on disk, is non-empty, contains more than one
  newline-terminated line, every line parses as JSON, and the
  last parseable line has `type == "result"`.

**Runner.**

- [R-IKPA-XU49] A dedicated runner script — its name and exact invocation
  documented in the project's README — exists and runs the
  micro-test ladder. The runner emits, for each test M01–M27,
  a single output line containing the test ID and exactly one
  of the literal tokens `PASS`, `SKIP`, or `FAIL`. After the
  last test, the runner emits a one-line summary
  (`passed=N skipped=N failed=N`) and exits 0 if `failed == 0`,
  non-zero otherwise.
- [R-ILX7-BLUY] The runner from R-IKPA-XU49 runs tests in ID order
  (M01 → M27). When a Band-N test reports FAIL, every Band-N+1
  and later test that depends on the failed band's invariant
  may report FAIL or SKIP at the runner's discretion, but the
  runner still emits a result line for every test in the
  ladder so the operator sees the full state. The runner does
  not silently omit any test from its output.
- [R-HLC7-UHM4] The default `./test.sh` invocation does **not** run any
  micro-test ladder bullet (R-HSNM-542A through
  R-IPKW-GX31). The ladder runs only via its dedicated
  runner (R-IKPA-XU49). The dedicated runner, its
  prerequisites (`claude` CLI on `PATH` with usable
  credentials — an OAuth login or `ANTHROPIC_API_KEY` set in
  the environment), the expected per-band runtimes (Band 1
  < 30s total; Band 2 < 60s; Band 3 < 90s; Band 4 < 90s;
  Band 5 < 30s; Band 6 single-digit minutes, occasionally up
  to 15), and the DEBUG opt-in mechanism are documented in
  the project's README so an operator can run the ladder on
  demand.
