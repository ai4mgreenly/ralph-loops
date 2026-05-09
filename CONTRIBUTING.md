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
