# Contributing

Thanks for taking the time to look at ralph. The project is small and
strives to be easy to read; the rules below keep it that way.

## Build and test

    make build       # bin/ralph
    make test        # plain test run
    make test-race   # race detector enabled (CI runs this)
    make check       # everything CI runs: tests, lint, fmt, tidy

`make check` is the contract: if it passes locally it should pass in CI.

## Development tools

    make tools       # installs goimports, golangci-lint, stringer

The pinned `golangci-lint` version is in the Makefile. Use `make tools`
rather than `go install ...@latest` so everyone runs the same lint
config.

## Style

- All exported identifiers carry doc comments; the comment starts with
  the identifier's name and is a complete sentence.
- Run `make fmt` (gofmt + the lint config's gofumpt + gci) before
  committing. `make fmt-check` is the CI assertion.
- Tests use `_test.go` files and table-driven cases where it shortens
  the file. New tests should pass with `-race`.

## pi fixtures and the live smoke test

ralph drives the `pi` CLI and decodes its native `-p --mode json` event
stream. The decoder is tested against a corpus of **real captured** pi
runs under `internal/stream/testdata/` (plus one derived
`truncated.jsonl`). These fixtures are frozen and checked in; ordinary
`make test` never touches pi.

- **`exact-sum.jsonl`** (under `internal/loop/testdata/`) is the only
  *hand-authored* fixture. It carries fixed token/cost numbers for the
  deterministic exact-sum tally test and is **never regenerated**.

- **`make fixtures`** regenerates the real-captured corpus from live
  pi. Run it **only** when pi's event format drifts (pi is 0.x; its
  event vocabulary moves fast) and the frozen captures no longer match
  reality. It runs `internal/stream/testdata/regen.sh`, which performs
  real `pi -p --mode json` calls: it **costs live API budget** and
  needs `pi` installed and authed (`~/.pi/agent/auth.json`). The script
  redirects pi's stdin from `/dev/null` (pi hangs forever on an
  unclosed stdin) and explicitly never overwrites `exact-sum.jsonl`.

- **`TestLive_PiSmoke`** (`internal/agent/`) is a gated live smoke
  test: the early warning for pi 0.x format drift. It is double-gated
  so CI and unauthed environments always skip cleanly — it needs the
  `pilive` build tag *and* `RALPH_PI_LIVE=1` *and* `pi` on `$PATH`. Run
  it deliberately with:

      RALPH_PI_LIVE=1 go test -tags pilive ./internal/agent/ \
          -run TestLive_PiSmoke -v

  It costs a real API call. The default build does not compile it.

## Commit messages

Look at `git log --oneline` for the convention. In short:

- One short imperative subject line, capitalised, no trailing period
  ("Add the foo bar", not "Adds the foo bar." or "added foo").
- Body is optional; if you write one, separate it from the subject
  with a blank line and wrap to ~72 columns. Explain *why* — *what*
  is in the diff.

## Pull requests

- Keep changes focused. One logical change per PR makes review easy.
- Update doc comments when you change exported behaviour.
- Don't bump the toolchain or the pinned `golangci-lint` version in
  passing — those are project-wide decisions.
