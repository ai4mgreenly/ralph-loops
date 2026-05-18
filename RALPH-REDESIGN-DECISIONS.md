# Ralph Redesign — Decision Record

Forward redesign of ralph-loops from a **scaffold-bound, single-agent,
sentinel-terminated** loop into an **adopt-any-project, cross-provider,
oracle-verified** spec-driven build system.

This record **supersedes `PI-MIGRATION-DECISIONS.md`** in full. The pi
migration is complete; that record is historical. Where this document
and the migration record disagree, this document wins — most notably
the migration's Q3 (`RALPH-STATUS` text sentinel as the termination
control), which is **retired** here (Q5).

- **Status legend:** **LOCKED** = decided with the user. Every entry
  below is LOCKED unless marked otherwise.
- **Provenance:** decisions Q1–Q20 from a grilled design session
  (2026-05-17), each resolved one at a time with the user.

---

## The thesis

Three convergent findings drive the whole redesign:

1. **Fresh-context-per-iteration is ralph's structural advantage, not a
   limitation.** It is exactly what makes a *principled* verifier cheap
   for ralph and expensive for single-session tools (`/goal`-style). The
   verifier can be a clean-room spawn that never saw the worker's
   reasoning — and, further, a *different provider entirely*, de-correlating
   model priors (anti model-tribalism).
2. **Autonomous loops succeed when "done" is an external, hard-to-game
   oracle the agent cannot edit; they fail when "done" is the agent's
   own prose.** The `RALPH-STATUS` sentinel was load-bearing on its own —
   the soft spot. The fix already half-existed in `internal/reqs` /
   `idgen`.
3. **Least human steps + most aligned outcome are not in tension** once
   planning (alignment) is separated from implementation (mechanics):
   the human's only touch-points are ratifying intent (planning) and
   alignment review (post-run) — the two places human judgment has the
   highest leverage and removing it destroys alignment.

---

## Decisions

### Q1 — Adoption model — LOCKED
**Adopt-first.** ralph runs against an arbitrary existing project. The
3-dir scaffold becomes one *preset*, not a precondition. `ralph init`
*investigates* an existing project to fill in the manifest.
**Why:** "run in a project not built for it" is the core goal; making
the scaffold the universal invariant forces every adopted repo to be
contorted, violating least-human-steps.

### Q2 — State ownership — LOCKED
Run telemetry is **global** at `~/.ralph/results.jsonl` (cross-project
cost rollups — a user-level concern). The verified ledger is
**ralph-written**, never agent-appended.
**Why:** the artifact that proves "done" must not be writable by the
agent claiming done (reward-hacking surface).

### Q3 — State location & source of truth — LOCKED
Per-project state lives **outside the repo** at
`~/.ralph/projects/<key>/` where `<key>` derives from the project's
absolute path. Specs stay **in-repo**. Model **(B)**: the in-repo green
ID-tagged test is the source of truth; `~/.ralph/.../ledger.jsonl` is a
**disposable cache** ralph rebuilds from the repo.
**Why:** disjoint subtrees (agent's writable repo vs. ralph's state)
dissolve the enforcement problem with zero privilege/namespace/worktree
mechanics; adoption adds *nothing* to the repo. Accepted consequences:
repo move orphans state; second checkout = independent state; the
enforcement question is *designed out*, not walled.

### Q4 — Unit of done & who authors the check — LOCKED
The **human declares the acceptance criterion in the spec**, next to the
requirement. The build agent satisfies it with an ID-tagged test. A
**cross-provider, fresh-context verifier** is handed only *requirement
text + diff + test* and answers one question: *does this test genuinely
verify this stated criterion?* Green test **and** verifier-affirmed →
ralph records. **Hybrid:** the non-testable minority is marked
*judge-only* (Q16) and decided by the verifier from spec+diff; default
is the hard test oracle.
**Why:** independent + human-anchored is precisely the generator/verifier
configuration the evidence says works; the soft path is small and
conspicuous.

### Q5 — Planning is outside the loop — LOCKED
Requirement creation is a **bounded interactive phase before the loop**.
The loop runs a **frozen spec** and **terminates**. **Consequence
(locked):** termination is ralph-computed (unverified set empty); the
`RALPH-STATUS` sentinel is **retired**; pi-migration Q3 is deliberately
superseded.
**Why:** a loop that invents its own work never terminates — the
single most-cited failure mode (spec drift / overbaking).

### Q6 — ralph is non-interactive; planning is human-hosted — LOCKED
ralph exposes **only non-interactive commands**. `ralph plan` *emits*
the embedded, **binary-versioned** spec-process prompt (Layer 1) composed
with the project's `AGENTS.md` (Layer 2); the **human's own interactive
agent** runs it and writes specs into the in-repo spec dir. ralph never
hosts a TTY (out of scope) and never parses the planning session. The
scaffolded `helper/AGENTS.md` is **retired** (now embedded in the binary).
**Why:** keeps the spec process version-locked to ralph (the coupling
goal) while respecting that ralph has no input mechanism.

### Q7 — Commit & report granularity — LOCKED
**One commit per verified requirement** (message carries ID + acceptance
criterion + verifier verdict), made by **ralph** after the verifier
affirms — the build agent never commits. `ralph report` renders a
completion report; the human verdict feeds the **next** `ralph plan` as
new-ID requirements, never re-entering the loop.
**Why:** requirement-granular review is the only way the irreducible
human alignment step stays cheap.

### Q8 — ralph models requirements + runs, not features — LOCKED
"Feature" is the planning agent's organizing vocabulary, invisible to
ralph. The loop drains the **full** unverified set. `ralph report`
defaults to the **last run**.
**Why:** the run boundary already is the plan↔review rhythm; a feature
concept is moving parts with no payoff. Accepted: a run/report may be a
superset of the just-planned feature when prior work was left unverified
(correct — stranded work *should* resurface).

### Q9 — Durable, portable done-record — LOCKED
The ralph-made per-requirement commit carries a structured trailer:
`ralph-requirement`, `ralph-test`, `ralph-verified-by`,
`ralph-criterion-sha`. "Done" is rederivable anywhere from *spec has ID*
∧ *commit trailer exists* ∧ *test currently green* ∧ *criterion-sha
matches*. No committed ledger, no in-repo done-file. **Invariant:** the
spec is timeless, feature-organized, freely reorganizable; the join key
is order/position-independent; the temporal record is git-only and
insulated from the spec and future agents — the embedded `ralph plan`
prompt must enforce this. **Constraints:** ralph owns commits on its
branch; no squash on the branch ralph rederives from.
**Why:** puts integrity in tamper-evident git history written by ralph,
not the agent; `criterion-sha` gives Q4's "text change ⇒ new ID"
enforcement for free (mismatch ⇒ requirement falls back to unverified).

### Q10 — Loop failure posture — LOCKED
Verifier reject **or** can't-get-green both count as a fail. Retry the
*same* requirement with the verifier's reason injected (default **3**,
manifest-tunable). After the cutoff: **quarantine and continue** to the
next eligible requirement (do not terminate the run). Build provider ≠
verify provider **enforced by default**; explicit `--allow-same-provider`
override. Run ends when: unverified empty **∨** all remaining quarantined
**∨** wall-clock budget hit. Budget and quarantine are independent limits.

### Q11 — Verifier is loop-only — LOCKED
The verifier is **only** orchestrated by the loop; never a standalone
command, never invocable by the build agent.
**Why:** if the agent can call the gating verifier it tunes to pass it.

### Q12 — Requirement selection — LOCKED
The **build agent** picks. ralph hands it the **eligible set
(unverified − quarantined)** + the spec; the agent's first move is to
declare one chosen ID honoring prose ordering hints; ralph sanity-checks
the ID is in the set it gave.
**Why:** ordering hints are prose (Q9 invariant) — only an agent reading
the spec can honor them; ralph cannot.

### Q13 — Quarantine is non-durable — LOCKED
Quarantine lives only in the disposable `~/.ralph/` cache. A fresh clone
loses it and will re-attempt the requirement. Accepted.

### Q14–Q15 — Red-baseline handling — LOCKED
A red suite at iteration start ⇒ that iteration's only job is
**green-first, one failing test per iteration**. If the failing-test
count fails to decrease for **3 consecutive iterations**, **hard-halt**
the whole run (report: "halted, baseline red"). Hard-halt, not
quarantine — nothing can proceed on a broken baseline.

### Q16 — Judge-only marker — LOCKED
A judge-only requirement is marked by an **inline marker in its spec
text**, taught by the embedded `ralph plan` prompt. No separate file —
the marker travels with the requirement.

### Q17 — No agent-managed handoff — LOCKED
`handoff.md` is **removed**. There is no agent-managed cross-iteration
file. The sole cross-iteration state is a retry's verifier reason, which
ralph captures from the verifier spawn and **injects directly into the
next build spawn's prompt**. ralph is the only carrier.

### Q18 — Missing persona on adoption — LOCKED
Adopting a project with no root `AGENTS.md`: `ralph init` **generates a
draft** from its codebase investigation for the human to refine. It does
not hard-require a hand-written persona.

### Q19 — Command surface — LOCKED
`ralph` (loop) · `ralph init` (investigate, write manifest, draft
`AGENTS.md` if absent) · `ralph plan` (emit embedded prompt) ·
`ralph report` (default last run) · `ralph newid` · `ralph reset` ·
`ralph time-of`. **Retired:** `ralph unverified` (ralph selects
internally now) and `ralph verify` (loop-only per Q11).

### Q20 — Cross-provider mechanism & defaults — LOCKED
Single harness: ralph spawns **`pi` with per-role `--provider`/`--model`**.
Defaults: **build = OpenAI `gpt-5.5`**, **verify = Anthropic
`opus-4.7`** (satisfies Q10's B≠V by default). Precedence:
**CLI flags at `ralph` start > manifest > built-in defaults**.
**Why:** de-correlated model priors are the independence you want; the
independence is the model's, not the harness's — different CLIs would
multiply integration surface for no added independence.

---

## The macro cycle

```
ralph plan    interactive, human + provider-P agent author/ratify spec
   │          (bounded, outside the loop; ralph only emits the prompt)
   ▼
ralph (loop)  autonomous, frozen spec. per requirement: agent picks →
   │          build (B) → suite gate → cross-provider verify (V) →
   │          ralph commits w/ trailer. quarantine on repeated fail.
   │          terminates: unverified empty ∨ all quarantined ∨ budget.
   ▼
human verify  alignment review the machine structurally cannot do;
   │          ralph report scopes it, flags judge-only items.
   ▼
decision      accept (done)  │  gaps → next `ralph plan` (new IDs)
```

## Deferred / explicitly out of scope

- **OS-enforced filesystem sandboxing** (Landlock / mount-ns). Designed
  *out* via Q3's disjoint-subtree model, not walled. Threat model =
  lazy-but-honest agent, not adversarial. Add later only if untrusted
  specs/models become a real scenario.
- **ralph hosting any interactive session** — permanently out of scope
  (Q6); ralph is non-interactive commands only.
- **Mid-run human checkpoints** — review is end-of-run per the agreed
  cycle; per-feature checkpointing is a possible future option, not now.
