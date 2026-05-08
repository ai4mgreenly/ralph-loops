# Storage

Story data — the canonical record of every story collected or
imported — lives on the filesystem under the repo root. No database
holds the canonical story content.

Stories live in `./stories/` at the repo root. Nothing else lives in
`./stories/` — it is the story store and only the story store. The
internal layout inside `./stories/` (per-story file vs. directory,
naming, format) is the implementer's choice; the directory boundary
is the contract.

## Story fields

Every story has, at minimum:

- `title` — short, factual headline (one line).
- `article` — markdown body, a few sentences with inline links to
  primary sources.
- `citations` — one or more sources, each a `{title, url}` pair.
  Every URL referenced in `article` must appear in `citations`.
- `collected_at` — ISO-8601 timestamp in US Central Time (the
  `America/Chicago` zone, which observes daylight saving — `CST`
  during standard time, `CDT` during DST). The stored string
  carries the Central offset (`-06:00` or `-05:00` as appropriate);
  no runtime time-zone conversion is performed at render or read.
  Indicates when the story was collected (or, for imported seed
  stories, when the original collection happened).

## Required operations

The storage layout (filenames, directory structure, file format) is
the implementer's choice. Whatever shape it takes must support:

- **Append.** Adding a new collected story is cheap and atomic.
- **List by recency.** Producing a reverse-chronological list of
  all stories, paginated, is fast enough to serve a web request
  without noticeable delay even with thousands of stories
  accumulated.
- **Dedup by source URL.** Before each collection run, the
  collector must be able to obtain the set of source URLs already
  seen across all stored stories.
- **Hand curation.** A human operator can edit a story's text or
  remove a story entirely without using the application — by
  editing files on disk directly. The filesystem is the source of
  truth.
- **Inspectability.** A human can browse the stored stories with
  ordinary file-system tools (`ls`, `cat`, an editor) and
  understand what's there.

An in-memory or on-disk index for query speed is fine, as long as
the filesystem remains source of truth and remains accurate after
hand-edits.

## Tests

- [R-IY47-5B9W] Listing stories by recency returns them in reverse-chronological
  order by `collected_at`.
- [R-IZC3-J30L] Removing a story from the filesystem causes it to disappear from
  the application's listings on the next request.
- [R-J0JZ-WURA] Editing a story's `article` text on the filesystem causes the
  new text to be served on the next request.
- [R-J1RW-AMHZ] The collector can obtain the full set of source URLs across all
  stored stories.
- [R-J2ZS-OE8O] A newly recorded `collected_at` carries the correct Central
  offset for the moment it was recorded: `-05:00` during US
  daylight saving and `-06:00` during standard time.
- [R-J47P-25ZD] All canonical story files live under `./stories/` at the repo
  root. After any collection or import, every persisted story is
  reachable by walking `./stories/`, and no canonical story content
  resides elsewhere in the tree.
