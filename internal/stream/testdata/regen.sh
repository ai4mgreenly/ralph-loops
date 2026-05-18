#!/usr/bin/env bash
# regen.sh — regenerate the real-captured pi fixture corpus (Q14(d)).
#
# WHEN TO RUN THIS: only when pi's `-p --mode json` event format drifts
# (pi is 0.x and its event vocabulary moves fast) and the frozen
# fixtures in this directory no longer reflect reality — typically
# signalled by TestLive_PiSmoke failing or the stream tests breaking
# after a pi upgrade. It is NOT part of `make test`.
#
# COST/PREREQS: every capture below is a REAL `pi -p --mode json` run
# against live oauth. Running this spends real API budget and requires
# `pi` to be installed and authed (~/.pi/agent/auth.json). Most CI and
# unauthed dev environments cannot and must not run it.
#
# exact-sum.jsonl is INTENTIONALLY NOT REGENERATED: it is the Q14(c)
# hand-authored deterministic fixture with fixed token/cost numbers used
# by the exact-sum tally test. (It actually lives in
# internal/loop/testdata/, not here, so this script could not touch it
# even by accident — but we still exclude it by name, and never write
# anything but the files listed below, so the intent is explicit and
# this directory's exact-sum, if one is ever added, stays safe.)
#
# The script is idempotent in effect: each capture overwrites its target
# file in place. Outputs are written by ABSOLUTE path derived from this
# script's own location, so it does not depend on the caller's cwd.

set -euo pipefail

# Absolute path to this directory (internal/stream/testdata), resolved
# from the script's own location so the caller's cwd is irrelevant.
TESTDATA_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# The Q14(c) hand-authored fixture this script must never overwrite.
# Listed explicitly so the exclusion is auditable; the capture loop
# below only ever writes the names in CAPTURES, so exact-sum.jsonl is
# structurally out of reach, but we assert it here for clarity.
readonly EXCLUDE_FIXTURE="exact-sum.jsonl"

PI_BIN="${PI:-pi}"
if ! command -v "$PI_BIN" >/dev/null 2>&1; then
	echo "regen.sh: \`$PI_BIN\` not found on \$PATH; this needs pi installed and authed" >&2
	exit 1
fi

# Fresh, isolated workspace for the tool-using captures. A new mktemp -d
# per run keeps captures reproducible and never pollutes the repo.
WS="$(mktemp -d)"
trap 'rm -rf "$WS"' EXIT
echo "regen.sh: workspace = $WS" >&2

# Seed files for the tool captures (paths interpolated into prompts).
printf 'alpha\nbeta\ngamma\n' >"$WS/sample.txt"
printf 'The quick brown fox.\n' >"$WS/edit-target.txt"
printf 'one\ntwo\nthree\n' >"$WS/multi.txt"
# tool-error.jsonl deliberately has NO seed file: the read of
# "$WS/nope-does-not-exist.txt" must fail so pi emits a
# tool_execution_end with isError:true.

# capture <output-file> <tools-spec> <prompt>
#
# Reproduces EXACTLY the locked invocation table: every run also passes
# --no-session --no-context-files, and <tools-spec> is either
# "--no-tools" or "--tools <list>".
#
# STDIN IS REDIRECTED FROM /dev/null. THIS IS LOAD-BEARING: pi's print
# mode reads piped stdin to EOF before starting, so an unclosed stdin
# makes pi hang forever (a prior capture confirmed this empirically).
# /dev/null yields immediate EOF, which is exactly what one-shot mode
# needs. Do not remove the `</dev/null`.
capture() {
	local out="$1" tools="$2" prompt="$3"
	if [ "$out" = "$EXCLUDE_FIXTURE" ]; then
		echo "regen.sh: refusing to write $EXCLUDE_FIXTURE (hand-authored Q14c fixture)" >&2
		exit 1
	fi
	echo "regen.sh: capturing $out ($tools)" >&2
	# shellcheck disable=SC2086 -- $tools is an intentional multi-token flag spec.
	"$PI_BIN" -p --mode json --no-session --no-context-files $tools "$prompt" \
		</dev/null \
		>"$TESTDATA_DIR/$out"
}

# --- The locked capture table (Q14(a)) -----------------------------------

capture done.jsonl "--no-tools" \
	"Reply with the single word: acknowledged. Then output a final line containing exactly: RALPH-STATUS: DONE"

capture continue.jsonl "--no-tools" \
	"Reply with the single word: working. Then output a final line containing exactly: RALPH-STATUS: CONTINUE"

capture no-sentinel.jsonl "--no-tools" \
	"Reply with exactly: hello world"

capture tool-read.jsonl "--tools read" \
	"Use the read tool to read $WS/sample.txt and tell me its second line, then output a final line containing exactly: RALPH-STATUS: CONTINUE"

capture tool-edit.jsonl "--tools read,edit" \
	"Use the edit tool to change the word quick to slow in $WS/edit-target.txt, then output a final line containing exactly: RALPH-STATUS: DONE"

capture tool-error.jsonl "--tools read" \
	"Use the read tool to read the file $WS/nope-does-not-exist.txt. It will fail. Briefly note the failure, then output a final line containing exactly: RALPH-STATUS: CONTINUE"

capture multi-turn.jsonl "--tools read,edit" \
	"First use the read tool to read $WS/multi.txt. Then use the edit tool to change the word 'two' to 'TWO' in that same file. Then output a final line containing exactly: RALPH-STATUS: DONE"

# --- Derived (NOT a live capture) ---------------------------------------
#
# truncated.jsonl is the fresh done.jsonl with its terminal agent_end
# line removed, so the decoder's "stream ended without agent_end"
# contract has a real capture to test against.
grep -v '"type":"agent_end"' "$TESTDATA_DIR/done.jsonl" \
	>"$TESTDATA_DIR/truncated.jsonl"

echo "regen.sh: done. exact-sum.jsonl was NOT touched (hand-authored Q14c fixture)." >&2
