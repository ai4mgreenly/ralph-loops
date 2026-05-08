# Example: ralph-scoops

A working example specification you can point `ralph` at to drive an
end-to-end iteration loop.

The spec describes **ralph-scoops** — a small news-gathering web
application that periodically researches recent stories via the
`claude` CLI, writes them to a local store, and serves a paginated
browseable list over HTTP. It's a non-trivial but tractable project:
about a hundred and seventy testable requirements split across
collector, storage, web UI, and entry-point concerns.

## Layout

The spec is deployment-agnostic — it describes the application, not
how it's built or shipped. Requirement IDs are in `R-XXXX-XXXX` form
(produced by `ralph newid`).

| File              | What it covers                                        |
|-------------------|-------------------------------------------------------|
| `OVERVIEW.md`     | Purpose, top-level structure, reading order.          |
| `collector.md`    | News-gathering subsystem — the largest file.          |
| `storage.md`      | Story file format, directory layout, append rules.    |
| `web.md`          | HTTP routes, pagination, rendering.                   |
| `entry-points.md` | `./launch.sh` and `./test.sh` contracts.              |

## Running it

The spec lives in `reqs/`; pick any directory you want the
application built in (this example does **not** ship a workdir):

```sh
mkdir -p /tmp/ralph-scoops-build
ralph --reqs=examples/ralph-scoops/reqs /tmp/ralph-scoops-build
```

For a budgeted run:

```sh
ralph --reqs=examples/ralph-scoops/reqs --duration=2h /tmp/ralph-scoops-build
```

`ralph .` invoked from inside `examples/ralph-scoops/` would also
work and would build the application in-place; the trade-off is that
generated source then sits next to the spec rather than in a
separate tree.

## Expectations

ralph-scoops is a real-shaped project, not a toy. A clean run from
empty workdir to `DONE` will take many iterations and meaningful
token spend. Use a duration cap on the first run to bound cost while
you observe the loop, then extend it once you trust the output.
