# Web UI

A web server reads the live store (see `storage.md`) and serves a
browseable view of the stories: a paginated index, and a detail
page per story.

## Time zone for displayed times

`collected_at` is recorded in US Central Time at story creation
(see `storage.md`), so the displayed time is just the stored value
formatted for output — no time-zone conversion happens at render.

The `today` and `yesterday` labels on the index are computed
against the current Central date — not the host machine's local
date — so the boundary follows the same zone the timestamps were
recorded in.

## Index page

The index is a paginated, reverse-chronological list of stories.
Each row is laid out in two columns:

- a left column containing the date and time the story was
  collected
- a right column containing the title — the title is a link to
  that story's detail page

Long titles wrap within the right column; they never wrap back
underneath the date/time column.

The date/time renders as:

- `today HH:MM` when the story was collected on the current local
  date
- `yesterday HH:MM` when the story was collected on the previous
  local date
- `YYYY-MM-DD HH:MM` for any older story

Adjacent rows are separated by a subdued horizontal divider — a
thin, light-gray line that delineates entries without competing
visually with the title text.

Pagination URL scheme (`?page=N`, `/page/N`, etc.) is the
implementer's choice. The visible pagination control consists of,
in order:

- a small pill/button-styled link labeled `prev`
- the text `page N of M` (e.g. `page 3 of 19`)
- a small pill/button-styled link labeled `next`

The labels read exactly `prev` and `next` — no square brackets
appear in the rendered output; the brackets in earlier drafts of
this spec were shorthand for "this is a button," not literal text.
On the first page the `prev` button is absent or inactive; on the
last page the `next` button is absent or inactive.

The pagination control's three visible parts — the `prev` pill,
the `page N of M` text, and the `next` pill — are centered as a
group within the main content container. The group's left and
right edges (the leftmost edge of `prev` and the rightmost edge
of `next`) sit equidistant from the container's content edges;
wrapping the group in a centered container while leaving the
visible children left-aligned within that container does not
count as centering. A clear vertical gap separates the group from
the last story row above it so it does not sit flush against the
final row's divider.

The default page size is **10 stories per page** when the request
does not specify one.

## Detail page

Each story has a permanent URL. The detail page shows:

- title
- date and time the story was collected (`collected_at`),
  rendered in the same font and the same subdued gray color used
  for the date/time column on the index
- the article body, rendered from its markdown source. Standard
  CommonMark constructs in the source render to their semantic
  HTML equivalents — bold (`**text**` / `__text__`) as
  `<strong>`, italic (`*text*` / `_text_`) as `<em>`, inline
  code (`` `code` ``) as `<code>`, unordered lists (`- item`)
  as `<ul><li>`, ordered lists (`1. item`) as `<ol><li>`,
  blockquotes (`> text`) as `<blockquote>`, and paragraphs
  separated by blank lines as separate `<p>` tags. Every URL in
  the source — markdown links and bare URLs — renders as an
  active HTML link
- the citations list — each `{title, url}` rendered as an active
  link

A small section header reading exactly `Citations` introduces the
citations list. The header sits after the divider that follows the
article body and before the first citation entry. It is rendered
at the meta/small text size (`1rem` / 16px) in the muted/meta
gray (`#595959`) — smaller and more subdued than the article body
so the section reads as a footnote-style label rather than a
peer of the article's prose. The header does not add a divider;
the post-article divider remains exactly one line, and the header
simply labels the section that follows.

Each citation entry — its `{title, url}` rendered as an active
link — uses smaller text than the `Citations` header (`0.875rem` /
14px) and the same muted/meta gray (`#595959`). The grayscale link
rules from elsewhere on the site still apply to citation links.

A subdued horizontal divider — the same style used between rows
on the index — is rendered after the article body and after each
entry in the citations list. Multiple horizontal dividers never
appear consecutively — between fields or after them. Exactly one
divider follows the article body, exactly one divider follows
each citation, and no other dividers are rendered on the page.

In this spec, a "horizontal divider" means **any** visible
horizontal line in the rendered output — an `<hr>` element, a
`border-top` or `border-bottom` on a content element, a
`box-shadow`-based rule, or any other markup or CSS that produces
a visible line. The single-divider count covers all such lines
together. A citation `<li>` with both an `<hr>` after it and a
`border-bottom` on the `<li>` itself counts as two consecutive
dividers and is forbidden.

URL scheme for the detail page is the implementer's choice
(`/story/<id>`, `/s/<slug>`, etc.).

A back link is rendered at the top of the detail page, before the
story title. The back link spans the full width of the content
column with its label horizontally centered within the button. It
keeps the rounded button affordance (background, border-radius)
of the index pagination's `prev` and `next` controls, but is wider
rather than the small pill those controls use. Its computed
background color matches the computed background color of the
`prev` and `next` pills on the index pagination, so the three
button-style controls read as a coherent set across pages. Its
label is exactly `back`. No square brackets appear in the
rendered output — the brackets in earlier drafts of this spec
were shorthand for "this is a button," not literal text.

Following the back link returns the visitor to the same page of
the index from which they navigated — clicking a title on index
page 3 yields a detail page whose back link returns to index page
3, not to page 1 and not generically to the previous browser
location. If a detail page is reached directly (e.g. opening the
URL without first visiting the index), the back link returns to
index page 1. The implementer chooses the mechanism (query
parameter on the detail URL, `Referer` header, in-page history
script, etc.); only the behavior is specified.

All major content blocks on the detail page — the `back` link,
the story title, the date/time line, the article body, the
`Citations` header, and the citations list — share the same
horizontal extent, aligned to the prose reading column. The
article body is not rendered narrower than any other content
block on the page.

## Site banner

Every page renders a banner at the top — above all other content,
including the detail page's `back` link — that displays the site
name `Ralph Scoops`. The banner has a black background and the
site-name text is rendered in a very light gray, so it reads as a
subdued mark against the dark bar rather than as primary content.
The banner spans the full viewport width even when the main
content container is constrained and centered.

The site-name text is **horizontally centered** within the banner
and rendered at a banner-prominent size — at least **1.5rem
(24px)** with **weight 600** — so it reads as the page header
rather than as ordinary running text.

A clear vertical gap separates the banner from whatever content
appears first on the page below it — the first story row on the
index, the `back` link on the detail page — so the banner does
not sit flush against that content.

The site-name text in the banner is itself an active link whose
target is the index's first page (the no-parameter index URL).
Following the link from any page — index or detail — returns the
visitor to index page 1. The link does not change the banner's
visual treatment: the text remains horizontally centered, in the
banner's light-gray color, and carries no underline in its
default state.

## Typography

Typography is tuned for long-form reading. Concrete values:

- **Body face**: Source Serif 4, loaded from Google Fonts at the
  weights `400` and `600`, with a fallback stack of
  `Charter, Georgia, Cambria, "Times New Roman", serif`. The
  Google Fonts stylesheet is referenced from the page head via a
  `<link rel="stylesheet" href="https://fonts.googleapis.com/...">`
  tag.
- **Root size**: `html { font-size: 16px }`. Body copy is
  `1.1875rem` (19px). Below a 480px viewport, body copy drops to
  `1.0625rem` (17px).
- **Heading scale** (modular ratio 1.2): h1 `2rem` (32px), h2
  `1.625rem` (26px), h3 `1.375rem` (22px), h4 `1.1875rem` (19px),
  meta/small text `1rem` (16px).
- **Line-height**: `1.6` (unitless) on body; `1.2` on headings.
- **Reading column**: the inner content column on both index and
  detail pages has `max-width: 42rem` (~672px) and is
  horizontally centered within the 1024px outer container. The
  outer container holds the column plus margins; the prose itself
  never stretches to 1024px.
- **Spacing**: paragraphs get `margin: 0 0 1em`; h2 takes
  `margin-top: 2em`; h3 takes `margin-top: 1.6em`; the article
  body gets `padding-block: 48px` on viewports ≥ 768px and
  `32px` below; horizontal page padding follows
  `padding-inline: clamp(20px, 4vw, 48px)` on the main container
  and on the banner so the wordmark column-aligns with the prose.
- **Color palette** (all grayscale, equal RGB components):
  - background `#fdfdfd`
  - body text `#1a1a1a`
  - headings `#0d0d0d`
  - muted/meta text `#595959`
  - banner background `#0d0d0d`
  - banner text `#e6e6e6`

## Styling and responsiveness

Pages are styled for comfortable blog reading: a readable font, a
generous line-height, a constrained content width so lines do not
stretch uncomfortably wide on a large monitor, adequate spacing
between rows in the index and between paragraphs in the article.

The main content container is sized for a 13-inch laptop display:
its rendered width never exceeds **1024 pixels**, and on viewports
wider than that the container is horizontally centered so the
unused space splits evenly into left and right margins. Narrower
viewports (phone- and tablet-width) may use the full available
width as before.

The palette is monochrome: all text is rendered in shades of black
or gray — no chromatic hues anywhere, including in links and
link-hover states. The date/time column on the index uses a more
subdued (lighter) shade of gray than the title text so the title
reads as the primary content.

On the index page, link text carries no underline in its default
state; an underline appears only on `:hover`. Link text retains
its grayscale color in both states.

Pages render correctly at both phone-width and laptop-width
viewports — text reflows, no horizontal scrolling, the pagination
control and article remain legible at narrow widths.

## Tests

- [R-J5FL-FXQ2] The index page renders rows in reverse-chronological order by
  `collected_at`.
- [R-J6NH-TPGR] Each row on the index displays the story's date, time, and
  title.
- [R-J7VE-7H7G] Each row's title is an active HTML link that resolves to that
  story's detail page.
- [R-J93A-L8Y5] Each row uses a two-column layout: the date/time occupies a
  left column, the title occupies a right column. When a title is
  long enough to wrap, the wrapped lines remain within the title
  column and do not flow underneath the date/time.
- [R-JAB6-Z0OU] Adjacent rows on the index are separated by a horizontal divider
  (an `<hr>` element between them, a `border-bottom` on the row,
  or equivalent) rendered in a subdued (light) gray.
- [R-JBJ3-CSFJ] A story whose `collected_at` falls on the current Central date
  renders its date column as `today HH:MM`.
- [R-JCQZ-QK68] A story whose `collected_at` falls on the previous local date
  renders its date column as `yesterday HH:MM`.
- [R-JDYW-4BWX] A story whose `collected_at` is older than yesterday renders its
  date column as `YYYY-MM-DD HH:MM`.
- [R-JF6S-I3NM] The date/time column on the index renders in a different (more
  subdued) gray than the title text — the resolved CSS color of
  the date/time element is not the same as the title's.
- [R-JGEO-VVEB] On the index page, link elements have `text-decoration: none`
  (or equivalent) in their default state and show an underline on
  `:hover`.
- [R-JHML-9N50] All text on the page renders in grayscale — every text element's
  computed `color` has equal red, green, and blue components — on
  both the index page and the detail page.
- [R-JIUH-NEVP] Pagination divides the full list across multiple pages and is
  reachable via documented URLs.
- [R-JK2E-16ME] The pagination control renders, in order: a `prev` link styled
  as a small pill or button, the text `page N of M`, and a `next`
  link styled as a small pill or button. The labels read exactly
  `prev` and `next` — no `[` or `]` characters appear in the
  rendered pagination control. The `prev` button is absent or
  inactive on page 1; the `next` button is absent or inactive on
  the last page.
- [R-JLAA-EYD3] A story added to the live store after the previous request
  appears in the next request's index listing.
- [R-JMI6-SQ3S] The detail page for a story shows the story's title, date, time,
  full article body, and citations.
- [R-JNQ3-6HUH] The detail page includes a back link rendered before the story
  title in the page's DOM.
- [R-KGZO-CZN5] The back link on the detail page is rendered as a button
  (rounded-corner background, visible button affordance) that
  spans the full width of the content column. Its label, exactly
  `back`, is horizontally centered within the button — the
  rendered horizontal midpoint of the label text is within 1px
  of the button's horizontal midpoint. No `[` or `]` characters
  appear in the rendered back link.
- [R-JOXZ-K9L6] Following the back link from a detail page that was entered by
  clicking a story title on index page N returns the visitor to
  index page N.
- [R-JQ5V-Y1BV] Following the back link from a detail page entered directly
  (without a referring index page) returns the visitor to index
  page 1.
- [R-KQQV-F5KP] On the detail page, every link expressed in the article
  body's markdown source renders as an active HTML `<a>`
  element. Specifically: (a) for every markdown link of the
  form `[label](url)` in the source — where `label` contains
  no `[` or `]` and `url` contains no unescaped `(` or `)` —
  the rendered article body contains an `<a>` element whose
  `href` attribute equals `url` and whose visible text content
  equals `label`, and the literal substrings `[label]` and
  `(url)` do not appear in the rendered text content of the
  article body; (b) for every bare URL in the source (a URL
  not enclosed in markdown link syntax), the rendered article
  body contains an `<a>` element whose `href` and visible
  text both equal the URL. Verifying against a fixture article
  containing both forms is the intended shape of the test.
- [R-JRDS-BT2K] Each citation in the citations list renders as an active link to
  its `url`.
- [R-JSLO-PKT9] On the detail page, the date/time element renders in the same
  font (font-family, size, weight) and the same subdued gray
  color as the index page's date/time column.
- [R-JTTL-3CJY] On a detail page with N citations, exactly N+1 visible
  horizontal lines appear in the rendered output — counting
  `<hr>` elements, CSS borders/box-shadows that paint a
  horizontal rule, and any other source of a visible line
  together. The N+1 lines fall one after the article body and
  one after each citation entry; the divider style matches the
  index row divider.
- [R-JV1H-H4AN] No two visible horizontal lines (from any source — `<hr>`,
  `border-bottom`, etc.) ever appear consecutively in a rendered
  detail page, whether between fields or after the last field.
- [R-JW9D-UW1C] Every page response includes a `<meta name="viewport"
  content="width=device-width...">` tag.
- [R-JXHA-8NS1] Every page response includes or references CSS, and the served
  CSS contains `font-family`, `line-height`, and a width constraint
  (`max-width` or equivalent) on the main content container.
- [R-JYP6-MFIQ] At a 375px viewport (a typical phone width), the rendered index
  page has no element extending past the viewport horizontally and
  the page has no horizontal scroll.
- [R-JZX3-079F] At a 375px viewport, the rendered detail page has no element
  extending past the viewport horizontally and the page has no
  horizontal scroll.
- [R-K14Z-DZ04] At a viewport wider than 1024px (e.g. 1440px), the main
  content container's rendered width is at most 1024px on both
  the index page and the detail page, and the container is
  horizontally centered — the rendered left and right margins
  outside the container are equal (within 1px). At viewports of
  1024px or narrower, the container may occupy the full available
  width.
- [R-KAW6-G4XO] Both the index page and the detail page render a banner at
  the top of the page — appearing in the DOM before any other
  visible content (including the detail page's `back` link) —
  whose visible text contains exactly `Ralph Scoops` (with that
  capitalization and a single ASCII space between the words).
  The banner's computed `background-color` is black (RGB `0,0,0`
  or visually indistinguishable from it), and the site-name
  text's computed `color` is a very light gray (equal red,
  green, and blue components, each at least `0xC0`/192). The
  banner element spans the full viewport width regardless of the
  main content container's width constraint.
- [R-K2CV-RQQT] Every page response includes, in `<head>`, a
  `<link rel="stylesheet">` whose `href` resolves to
  `https://fonts.googleapis.com/css2` and whose query string
  requests `Source Serif 4` at weights `400` and `600`. The
  served CSS sets the `body` element's `font-family` to a stack
  whose first declared family is `"Source Serif 4"`, followed
  (in order) by at least `Charter`, `Georgia`, `Cambria`,
  `"Times New Roman"`, and a generic `serif`. The body computed
  `font-weight` is `400`; headings (`h1`–`h4`) and `strong`
  compute to `600`.
- [R-K3KS-5IHI] At a viewport ≥ 768px, the computed `font-size` on `body`
  is exactly `19px` (`1.1875rem` against a `16px` root); on
  `h1` `32px`; on `h2` `26px`; on `h3` `22px`; on `h4` `19px`.
  The computed `line-height` on `body` is `1.6` × `font-size`
  (within 0.5px); on `h1`–`h4` it is `1.2` × `font-size` (within
  0.5px). At a viewport of 375px, the computed `font-size` on
  `body` is `17px` (`1.0625rem`).
- [R-K4SO-JA87] On both the index page and the detail page, at a 1440px
  viewport, the rendered content column that holds the prose
  (story rows on the index, article body on the detail page) has
  a computed `max-width` of `42rem` (672px ± 1px) and is
  horizontally centered within the 1024px outer container — the
  rendered left and right margins between the column and the
  outer container's content edges are equal within 1px. The
  prose column's rendered width never exceeds 672px on either
  page at any viewport ≥ 768px.
- [R-K60K-X1YW] The page renders the following exact computed colors (all
  channels equal):
  - `body` `background-color`: `rgb(253,253,253)` (`#fdfdfd`).
  - `body` text `color`: `rgb(26,26,26)` (`#1a1a1a`).
  - `h1`, `h2`, `h3`, `h4` `color`: `rgb(13,13,13)` (`#0d0d0d`).
  - Muted/meta elements (the index date/time column and the
    detail page's date/time line) `color`: `rgb(89,89,89)`
    (`#595959`).
  - Banner `background-color`: `rgb(13,13,13)` (`#0d0d0d`).
  - Banner site-name text `color`: `rgb(230,230,230)` (`#e6e6e6`).
- [R-K78H-ATPL] Computed spacing on rendered pages matches:
  - Body `<p>`: `margin-block-start: 0`,
    `margin-block-end` ≈ 1em (within 1px of the body
    `font-size`).
  - `<h2>`: `margin-block-start` ≈ 2em (within 1px of `2 ×
    body font-size`).
  - `<h3>`: `margin-block-start` ≈ 1.6em (within 1px).
  - The main content container's `padding-inline` follows
    `clamp(20px, 4vw, 48px)`: at a 375px viewport it is `20px`
    (within 1px); at a 1440px viewport it is `48px` (within
    1px). The banner element's `padding-inline` matches the
    main container's at the same viewport so the wordmark's
    inner edge column-aligns with the prose column edges.
  - The article body's `padding-block` is `48px` (within 1px)
    at a viewport ≥ 768px and `32px` (within 1px) at a 375px
    viewport.
- [R-K8GD-OLGA] The banner's site-name text is horizontally centered within
  the banner — its computed `text-align` is `center` (or its
  rendered horizontal midpoint is within 1px of the banner's
  horizontal midpoint) — and is rendered at a banner-prominent
  size: computed `font-size` ≥ `24px` (`1.5rem`) and computed
  `font-weight` ≥ `600`.
- [R-K9OA-2D6Z] When the index is requested without an explicit page-size
  parameter, the rendered page contains at most 10 story rows.
  With a live store of 11 or more stories, the default
  (no-parameter) request to the index renders exactly 10 rows
  and a pagination control reading `page 1 of M` with `M ≥ 2`.
- [R-KPIZ-1DU0] On the index page at a viewport ≥ 768px, the visible
  pagination group — the `prev` pill, the `page N of M` text,
  and the `next` pill — is horizontally centered as a group
  within the main content container. Measured by the union
  bounding box from the leftmost rendered pixel of the `prev`
  pill to the rightmost rendered pixel of the `next` pill, the
  group's horizontal midpoint is within 1px of the main content
  container's content-area horizontal midpoint. Equivalently:
  the rendered horizontal distance from the container's
  content-left edge to the leftmost edge of `prev` equals
  (within 1px) the distance from the rightmost edge of `next`
  to the container's content-right edge. A wrapping element
  that is itself centered but whose visible children are
  left-aligned within it does not satisfy this requirement.
- [R-KC42-TWOD] On the index page, a vertical gap of at least 24px separates
  the bottom edge of the last rendered story row (including any
  divider that belongs to it) from the top edge of the
  pagination control.
- [R-KDBZ-7OF2] On both the index page and the detail page, a vertical gap
  of at least 24px separates the bottom edge of the banner
  element from the top edge of the first content element below
  it (the first story row on the index, the `back` link on the
  detail page).
- [R-KEJV-LG5R] On the detail page, a heading element whose tag is one of
  `h2`, `h3`, or `h4` and whose visible text is exactly
  `Citations` (with that capitalization) is rendered between
  the divider that follows the article body and the first
  citation entry. No additional horizontal line of any kind
  appears between the post-article divider and the `Citations`
  heading, or between the `Citations` heading and the first
  citation entry.
- [R-KFRR-Z7WG] On the detail page at a viewport ≥ 768px, the rendered
  horizontal extent of the `back` link, the story title, the
  date/time line, the article body, the `Citations` heading,
  and each citation entry all share the same left content edge
  and the same right content edge (within 1px), and none
  renders with a narrower content width than the article body.
- [R-KI7K-QRDU] On the detail page at a viewport ≥ 768px, the `Citations`
  heading element's computed `font-size` is `16px` (`1rem`
  against a `16px` root) — smaller than the article body's
  `19px` and smaller than `h2`–`h4` on the same page.
- [R-KJFH-4J4J] On the detail page, the `Citations` heading element's
  computed `color` is `rgb(89,89,89)` (`#595959`) — the same
  muted/meta gray used for the date/time elements.
- [R-KKND-IAV8] On the detail page at a viewport ≥ 768px, every citation
  entry's link text has a computed `font-size` of `14px`
  (`0.875rem` against a `16px` root), strictly less than the
  `Citations` heading element's computed `font-size`.
- [R-KLV9-W2LX] On the detail page, every citation entry's link text has a
  computed `color` whose red, green, and blue components are
  equal and whose value is `rgb(89,89,89)` (`#595959`) in its
  default state.
- [R-KN36-9UCM] The computed `background-color` of the index page's
  pagination `prev` pill, the index page's pagination `next`
  pill, and the detail page's `back` link button are all equal
  to each other (within rounding noise on a single computed
  RGB value).
- [R-KRYR-SXBE] On every page (index and detail), the banner's site-name
  text is wrapped in (or otherwise behaves as) an HTML `<a>`
  element whose `href` resolves to the index's first page (the
  no-parameter index URL such as `/`). Following the link from
  any page lands on the index with the first page of stories
  rendered (the pagination control reads `page 1 of M`). The
  link wrapping does not change the rendered banner text's
  appearance: the deepest rendered text element containing the
  visible string `Ralph Scoops` has a computed `color` of
  `rgb(230,230,230)` (`#e6e6e6`) in both its default and
  `:hover` states, and carries no `text-decoration` underline
  in its default state (an underline may appear only on
  `:hover`). Querying the banner's outer element's computed
  color is not sufficient — the assertion is on the text-bearing
  inner element.
- [R-KOB2-NM3B] When the source markdown for a story's article body contains
  bold (`**…**` or `__…__`), italic (`*…*` or `_…_`), inline
  code (`` `…` ``), an unordered list (`- item`), an ordered
  list (`1. item`), a blockquote (`> text`), and paragraphs
  separated by blank lines, the rendered detail page contains,
  inside the article-body element, at least one `<strong>`,
  one `<em>`, one `<code>`, one `<ul>` with at least one
  `<li>` child, one `<ol>` with at least one `<li>` child,
  one `<blockquote>`, and at least two distinct `<p>`
  elements — one per source paragraph.
