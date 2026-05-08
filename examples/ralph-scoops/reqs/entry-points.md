# Entry-point scripts

The repo root has two executable shell scripts that are the
documented way to operate the app: one to run it, one to test it.

## `./launch.sh`

Runs the application. The single-process binary that combines the
collector and the web UI (see `OVERVIEW.md`) starts, and the web UI
listens on **port 3000**.

`./launch.sh` is the command referenced by `project-readme.md` as
the way to run the app locally.

## `./test.sh`

Runs the full test suite and exits 0 on success, non-zero on
failure. It is the entry point used by any developer running tests.

## Tests

- [R-IS0P-8GKF] `./launch.sh` exists at the repo root and is executable.
- [R-IT8L-M8B4] Running `./launch.sh` causes the application to begin listening
  on port 3000.
- [R-IVOE-DRSI] After `./launch.sh` starts, an HTTP `GET` to
  `http://127.0.0.1:3000/` returns status 200 with an HTML body that
  satisfies the index page contract from `web.md` — i.e. the
  response includes the `<meta name="viewport">` tag required by
  R-JW9D-UW1C and the stylesheet reference required by
  R-JXHA-8NS1. A response body that lacks these (e.g. a
  placeholder string like `hello`) does not satisfy this
  requirement; the launched binary must mount the web UI handlers
  at `/`, not a stand-in.
- [R-IUGI-001T] `./test.sh` exists at the repo root and is executable.
- [R-IWWA-RJJ7] After `./test.sh` returns, no process is listening on
  `127.0.0.1:3000`. Any server the test suite started — directly,
  via `./launch.sh`, or via a child of either — must be stopped
  before `./test.sh` exits, regardless of whether the suite passed
  or failed.
