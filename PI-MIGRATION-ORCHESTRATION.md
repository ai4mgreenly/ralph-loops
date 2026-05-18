# Pi Migration — Orchestrator Operating Contract

This is the standing instruction set for the agent orchestrating the pi
migration. It is written to be read **cold** by a fresh session with no
memory of prior work. It governs *how* the migration is executed;
`PI-MIGRATION-DECISIONS.md` governs *what* is built and *why*.

---

## Role

You are the **orchestrator**, not the implementer. The design is
finished: `PI-MIGRATION-DECISIONS.md` records Q1–Q15 all **LOCKED**,
"no open questions." You do not re-open, re-debate, or "improve" a
locked decision — the record is the spec. You turn that spec into
working, verified code by running the loop below; **subagents perform
the actual code edits**, you plan, dispatch, and verify.

If reality (pi's real behavior, or the existing code) contradicts the
locked record, **stop and report it** — do not silently deviate or
unilaterally redesign.

## Mode: autonomous

Run the loop end to end without per-goal checkpoints. Do not pause for
confirmation between goals. The operator interrupts if they want to
intervene. Stop on: migration complete and verified; an unrecoverable
contradiction with the locked record; or repeated subagent failure on
the same goal that you cannot resolve.

## The decision record is invariant — it is never progress

`PI-MIGRATION-DECISIONS.md` reads byte-identical at the start of every
session and to every subagent. It can **never** indicate what is done.
"X is described in the record" is never evidence "X is implemented."
You have no cross-session memory; subagents start blank and see only
the repo plus what you hand them. Therefore "where are we" is
**reconstructed from durable, mutable sources every start — never
assumed, never read off the decision record.**

## State reconstruction precedes goal selection (mandatory first act of every iteration)

Fixed order, every iteration, no exceptions:

1. **Read `PI-MIGRATION-PLAN.md`** — the in-repo ledger you maintain
   (separate from the decision record): the ~10–20-goal roadmap, each
   goal's status (`pending` / `in-progress` / `done-verified`), the
   last verification result, and a one-line "why" per closed goal. It
   lives in the working tree so subagents and future sessions read it
   without your context.
2. **Verify its claims against the repo** — `git log`, the actual
   code, and `make build` + `make test`. The ledger is a *claim*; the
   repo is *fact*. On any disagreement the **repo wins**; reconcile the
   ledger to match reality before proceeding.
3. **Only then select the next goal.** A goal is `done-verified` only
   when the repo proves it (build + test green + the goal's own
   acceptance check), not when the ledger says so.

## The execution loop

1. **Reconstruct state** (the procedure above).
2. **Select the next functional goal** — one *vertically coherent,
   independently verifiable* slice (e.g. "rewrite `internal/stream` to
   pi's event vocabulary with passing real-captured fixture tests"),
   not a file-count chunk. Respect dependency order: the
   `internal/stream` rewrite + fixtures is the spine; pricing deletion,
   flag surface, process lifecycle, scaffold/templates, and stats hang
   off it.
3. **Plan the goal** — the locked decision IDs it implements, files in
   scope, files explicitly out of scope, and its definition of done.
4. **Dispatch subagent(s)** with fully self-contained written
   instructions (see contract below).
5. **Verify** — `make build` and `make test` green, plus the goal's
   own acceptance check. Mechanical verification, not assertion.
6. **Close the goal** — update `PI-MIGRATION-PLAN.md` to
   `done-verified` with the verification result; write a `devlog`
   entry for any non-obvious decision, rejected alternative, or
   deliberate deferral; commit (see commit policy).
7. **Repeat** until the migration is complete and verified.

## Subagent dispatch contract

Every dispatch is self-contained — the subagent has none of your
context. Each instruction must give:

- The exact locked decision IDs (Q#) the work implements, quoted or
  tightly paraphrased so the subagent need not interpret the record.
- The files in scope and the files it must **not** touch.
- The explicit warning: `PI-MIGRATION-DECISIONS.md` describes the
  **target**, not the current state of the files being edited; current
  state comes from the code itself and `PI-MIGRATION-PLAN.md`.
- The acceptance check the subagent must run before reporting done
  (build/test/specific assertion).
- Instruction to report what it actually changed and the verification
  output verbatim — not a claim of success.

## Sizing

Chunk the **entire migration** into roughly **10–20 total subagent
iterations** (a work breakdown of ~10–20 dispatched tasks across the
whole effort), not 10–20 per goal. The roadmap in
`PI-MIGRATION-PLAN.md` keeps goals coherently ordered even though they
are selected one at a time.

## Artifact roles (do not conflate)

| File | Role | Mutability |
|---|---|---|
| `PI-MIGRATION-DECISIONS.md` | Locked spec — what/why | Immutable; never edited |
| `PI-MIGRATION-PLAN.md` | Progress ledger — how-far, the ~10–20-goal roadmap + status | Mutable; you maintain it |
| `PI-MIGRATION-ORCHESTRATION.md` | This file — how you operate | Stable; the operating contract |
| `devlog` (Obsidian) | Strategic per-goal "why / rejected / deferred" | Append-only, durable across machines |

All three `PI-MIGRATION-*.md` files are removable at migration end.

## Operational constraints (hard)

- **Never `cd`.** Stay at repo root; use relative paths. (project
  `rules` skill)
- **Never use the AskUserQuestion tool.** Surface choices in prose.
  (project `rules` skill)
- **Git writes run as the repo owner:**
  `sudo -n -u ai4mgreenly git …` (the `.git` sticky-bit constraint in
  the decision record's operational note). Working-tree file writes are
  direct.
- **Commit policy:** durable cross-session progress requires that each
  `done-verified` goal end in a commit (ledger committed with the
  work, so `git log` and the ledger advance together). This overrides
  the default "commit only when asked" — it is in force **only because
  the operator selected the autonomous loop and authorized
  per-goal commits for it.** Do not push. Do not tag (the
  `claude-restore` rollback tag already exists).
- **Verification is non-negotiable** (`principles` skill): a goal is
  not done until the build and tests prove it.
- **Reject scope creep:** no machinery for hypothetical futures, no
  backwards-compat shims, no reopening locked Qs.

---

*Companion documents:* `PI-MIGRATION-DECISIONS.md` (locked spec),
`PI-MIGRATION-PLAN.md` (progress ledger — created at first loop entry
if absent).
