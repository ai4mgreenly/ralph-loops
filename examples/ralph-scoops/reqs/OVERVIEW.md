# ralph-scoops

An automated news-gathering web application that runs locally on a
developer's machine. It supersedes `magpie`, an earlier project, by
adding a browseable web UI on top of the same news-collection
behavior.

ralph-scoops has two responsibilities, both running in a single
process:

1. **Collector** — periodically researches recent AI and technology
   news via the `claude` CLI and persists new stories. See
   `collector.md`.
2. **Web UI** — serves a paginated, reverse-chronological list of
   stories. See `web.md`.

Story data, the operations the storage must support, and hand-
curation rules are in `storage.md`.

The application runs locally, end-to-end, with one command.

## Stack constraints

The implementer chooses language, framework, web server, and
dependency tooling, subject to:

- **Single process.** The collector and web server cohabit; no
  separate daemons.
- **Local-first.** Runs on a developer's laptop with one command.
  No managed services, no Docker, no system-level dependencies
  beyond standard developer tools and the `claude` CLI.
- **Filesystem only.** Story content lives as files at the repo
  root. No database for canonical story data. (An in-memory or
  on-disk index for query speed is fine, as long as the filesystem
  remains source of truth.)
- **`claude` CLI as subprocess.** The collector invokes `claude` as
  an external process; it does not call any HTTP API directly.
