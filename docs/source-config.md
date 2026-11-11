<!-- Copied from the news repo's docs/source-config.md; this copy is the server's spec. -->
# Source config reference (schema version 1)

Each news site is described by `sources/<id>/config.json`. The config is data, not code. It is the
portable contract of this project: the Kotlin engine in `scraper/engine` interprets it today, and a
future server engine in any language must interpret it the same way and pass the same
[golden fixtures](#golden-fixtures).

A complete, working example lives in [`sources/_example/config.json`](../sources/_example/config.json).

## Top level

| Key             | Type                | Required | Meaning                                                        |
|-----------------|---------------------|----------|----------------------------------------------------------------|
| `schemaVersion` | int                 | yes      | Must be `1`.                                                   |
| `id`            | string              | yes      | Stable id, `[a-z0-9][a-z0-9_-]*`. Stored on every article.     |
| `name`          | string              | yes      | Display name.                                                  |
| `baseUrl`       | string              | yes      | Absolute `http(s)` URL; relative `list.urls` resolve against it. |
| `language`      | string              | yes      | BCP-47 tag of the content (`he`, `en`, ...).                   |
| `http`          | [Http](#http)       | no       | Request settings.                                              |
| `list`          | [List](#list)       | yes      | How to read headline pages.                                    |
| `feed`          | [List](#list)       | no       | The site's RSS feed, read instead of `list` when switched on; see [Feed](#feed). |
| `article`       | [Article](#article) | yes      | How to read an article page.                                   |
| `categories`    | [Categories](#categories) | no | Maps the site's section names to the shared categories.  |

Unknown keys are an error (except inside `http`, see below). That is deliberate: a misspelled key would otherwise be silently ignored.

## Http

| Key         | Type              | Default                    |
|-------------|-------------------|----------------------------|
| `userAgent` | string            | the server's own (`news-server-go (+repo URL)`) |
| `headers`   | map string→string | none (e.g. a consent cookie) |

Other keys in `http` are not errors: they are for packs, which read them from `fetch.Site.Extra` (the server's README,
"Packs"). The core ignores them.

## List

| Key           | Type                 | Required | Meaning                                              |
|---------------|----------------------|----------|------------------------------------------------------|
| `urls`        | string[]             | yes      | Headline pages, absolute or relative to `baseUrl`.   |
| `sections`    | `{url, category}`[]  | no       | More headline pages, one per site section; see [Categories](#categories). |
| `item`        | CSS selector         | one of   | Selects one element per article on the page.         |
| `json`        | [JSON items](#json-lists) | one of | Reads one JSON value per article instead.        |
| `url`         | [Field](#field)      | yes      | Evaluated inside each item. Resolved to absolute.    |
| `title`       | [Field](#field)      | yes      | Evaluated inside each item.                          |
| `summary`     | [Field](#field)      | no       |                                                      |
| `image`       | [Field](#field)      | no       | Resolved to absolute.                                |
| `publishedAt` | [Field](#field)      | no       | Parsed as a [date](#date).                           |
| `category`    | [Field](#field)      | no       | The site's own section name; see [Categories](#categories). |

Rules:

- Exactly one of `item` and `json` is set.
- Pages are fetched in order: `urls`, then `sections`.
- An item with no `url` or no `title` is skipped (promo boxes, ads).
- Items are de-duplicated by resolved URL. The first occurrence keeps its position, `url` and
  `title`; its missing `summary`, `imageUrl`, `publishedAt` and `category` are filled from later
  duplicates, in order. Sites often list a story twice, once as a plain link and once with a picture. Across several
  `urls` pages the merge keeps page order and de-duplicates the same way.

### JSON lists

Some sites render their headline cards with JavaScript, so the HTML holds empty placeholders, but the
same data is embedded in the page as JSON (for example Next.js's `<script id="__NEXT_DATA__">`).
`json` reads the items from there:

```json
"json": { "selector": "script#__NEXT_DATA__", "items": "$.props.pageProps..post" },
"url":  { "path": "$.permalink" },
"image": { "path": "$.media.images[0].url", "fallback": { "path": "$.media.poster" } }
```

| Key        | Type                     | Meaning                                                        |
|------------|--------------------------|----------------------------------------------------------------|
| `selector` | CSS selector             | The first matching element's raw content is parsed as JSON.    |
| `items`    | [JSON path](#json-paths) | Selects one value per article, in document order.              |

In a JSON list every field uses `path` (relative to the item) instead of `selector` and `attr`. A
missing element or malformed JSON gives an empty list.

### JSON paths

A subset of JSONPath:

| Syntax       | Meaning                                                                  |
|--------------|--------------------------------------------------------------------------|
| `$`          | The root (the page JSON for `items`, the item for a field's `path`).      |
| `.key`       | Member `key` of an object. A key is any run of characters except `.[]`.   |
| `..key`      | Every member named `key` at any depth, in pre-order (a node before its children). |
| `.*`, `[*]`  | Every member of an object or element of an array.                         |
| `[n]`        | Element `n` (from 0) of an array.                                         |

A step that does not apply (a key on an array, an index out of range) selects nothing. When a step
is applied to a **string**, the string is parsed as JSON first, and selects nothing if it is not
JSON. Sites often embed JSON as a string inside their page data, and this lets one path go through
it. A field reads the first match: a string as is, a number or boolean as its JSON text. An object,
array or `null` has no value.

## Categories

Every headline can carry one of a fixed set of shared categories, so the app can group stories from
all sites together:

`israel`, `politics`, `security`, `world`, `economy`, `law`, `opinion`, `sport`, `culture`, `tech`,
`health`, `lifestyle`

Sites name their sections differently, so a config reads the site's own **raw** category and maps it:

1. The raw category of an item is `list.category` evaluated in the item. If that has no value and
   the item came from a page listed in `list.sections`, it is that section's `category`.
2. `categories` maps each shared id to regex patterns. The item gets the **first** id, in the order
   written, with a pattern found anywhere in the raw value. Put specific ids before general ones
   (`"tech": ["^money/tech"]` before `"economy": ["^money"]`).
3. No raw value, or no matching pattern, means no category. The app shows those as "other".

```json
"list": {
  "urls": ["/news"],
  "sections": [{ "url": "/news/category/192", "category": "חדשות בעולם" }],
  "category": { "selector": "a", "attr": "href", "transforms": [{ "type": "regex", "pattern": "^/(.+)/article/" }] }
},
"categories": {
  "world": ["בעולם", "^news/world"],
  "israel": ["בארץ", "^news/local"]
}
```

`sections` is for sites whose cards say nothing about their section: the section pages are fetched
with the same list config, and a story found on the front page gets its category from the section
page it also appears on.

`article.category` reads the section name from the article page itself (often
`meta[property=article:section]` or JSON-LD `articleSection`), mapped through the same `categories`. Like
`video`, it is read before `remove` runs, so a `<script>` that `remove` drops can still hold it. It is
for headlines the list leaves without one: the app fills a headline's missing category from its article
once the article is downloaded, and never replaces a category the list gave.

## Article

| Key           | Type                  | Required | Meaning                                               |
|---------------|-----------------------|----------|-------------------------------------------------------|
| `remove`      | CSS selector[]        | no       | Elements deleted from the page **before** any field runs. |
| `title`       | [Field](#field)       | yes      | If missing, extraction fails: the config is broken.   |
| `subtitle`    | [Field](#field)       | no       |                                                       |
| `author`      | [Field](#field)       | no       |                                                       |
| `publishedAt` | [Field](#field)       | no       | Parsed as a [date](#date).                            |
| `image`       | [Field](#field)       | no       | Resolved to absolute.                                 |
| `category`    | [Field](#field)       | no       | The page's own section name; see [Categories](#categories). Read before `remove`. |
| `body`        | [Field](#field)       | yes      | **Every** matching element becomes one paragraph.     |
| `figures`     | [Figures](#figures)   | no       | In-article photos, in reading order with `body`.      |
| `content`     | [Content](#content)   | no       | The article's HTML from embedded JSON, for `body` and `figures`. |
| `video`       | [Video](#video)       | no       | The article's main video.                             |

Selectors here are evaluated against the whole document.

### Figures

```json
"figures": {
  "selector": ".article-body figure",
  "image": { "selector": "img", "attr": "src" },
  "caption": { "selector": "figcaption" }
}
```

`selector` selects each in-article figure; `image` and `caption` are fields read inside it. The output's `blocks`
holds the article in reading order: every element matching `body.selector` or `figures.selector`, in document
order, as a `text` block (the body field read from it) or an `image` block (`url` resolved to absolute, and the
`caption`). Rules:

- A figure with no `image` value is dropped (embeds, empty placeholders).
- A `body` element inside a figure (its caption) is part of the figure, not a paragraph.
- The same image twice in a row (a picture and its zoom copy) is one block.
- `figures` needs `body.selector`. With `figures`, `body` is the text of `blocks`, so the two always agree.
- `remove` runs first, so a config with `figures` must not remove the figures (or their captions).

### Content

Some sites render the article text with JavaScript: the page's HTML holds placeholders, and the text, with its photos,
is an HTML string inside the page's JSON (Next.js's `__NEXT_DATA__`, for example). `content` points at it:

```json
"content": { "selector": "script#__NEXT_DATA__", "path": "$.props.pageProps.serverData.post..content" }
```

`selector` selects the element holding the JSON (its raw content is parsed, as for [JSON lists](#json-lists)) and
`path` is a [JSON path](#json-paths); the first string it selects is parsed as HTML. `body` and `figures` are then
read from that HTML, and every other field still from the page. `remove` applies to both. Missing JSON or no string
at the path reads as an empty article text.

### Video

```json
"video": {
  "poster": { "selector": "html", "attr": "html", "transforms": [{ "type": "regex", "pattern": "\"VideoObject\"[\\s\\S]{0,1500}?\"thumbnailUrl\"\\s*:\\s*\"([^\"]+)\"" }] },
  "stream": { "selector": "video source", "attr": "src" },
  "embed":  { "selector": "meta[property=og:video]", "attr": "content" },
  "duration": { ... }
}
```

All four are optional [fields](#field) evaluated against the whole document **before** `remove`, since video details
usually live in a `<script>` (schema.org `VideoObject` JSON-LD). `stream` is a file or HLS playlist a player opens
directly; `embed` is the site's own player page. There is a video only if `stream` or `embed` has a value. `poster`,
`stream` and `embed` are resolved to absolute; `duration` is kept as written (often ISO-8601, `PT1M45S`).

Only read a `stream` that the page itself offers publicly. Videos the site plays only in its own player (signed or
ticketed streams) take `embed`.

How the app turns these fields into native playback — the highest resolution, a disk cache, an MP4 archive, and working a stream out for sites that hide it behind one more request — is the app's business (the news app repo's docs/video.md).

## Field

A field describes how to read one value.

| Key          | Type                       | Default  | Meaning                                                           |
|--------------|----------------------------|----------|-------------------------------------------------------------------|
| `selector`   | CSS selector               | none     | Relative to the scope (the list item, or the document). Absent = the scope element itself. |
| `path`       | [JSON path](#json-paths)   | none     | Only in a [JSON list](#json-lists), instead of `selector`/`attr`. Absent = the item itself. |
| `attr`       | string                     | `"text"` | What to read, see below.                                          |
| `transforms` | [Transform](#transforms)[] | `[]`     | Applied in order.                                                 |
| `date`       | [Date](#date)              | ISO      | Only used by `publishedAt` fields.                                |
| `fallback`   | Field                      | none     | Tried when this field yields no value. Can be chained.            |

`attr` values:

- `"text"`: the element's text including descendants, with whitespace collapsed to single spaces
  (jsoup `text()` semantics).
- `"ownText"`: only the element's direct text, excluding child elements.
- `"html"`: the element's inner HTML.
- anything else: that HTML attribute, e.g. `"href"`, `"src"`, `"data-src"`, `"content"`, `"datetime"`.

Evaluation of a single-valued field:

1. Select the **first** element matching `selector` (or take the scope element).
2. Read `attr` from it. A missing attribute reads as the empty string.
3. Apply each transform in order. If a transform yields no value, the field has no value.
4. Trim. An empty string means no value.
5. If there is no value at any step, evaluate `fallback` (if any) the same way.

Multi-valued fields (`article.body`) select **all** matching elements, apply steps 2–4 to each, and
drop the empty ones. If none remain, `fallback` is evaluated as a multi-valued field.

### Transforms

Each transform is an object with a `type`:

```json
{ "type": "regex",   "pattern": "^By (.+)$", "group": 1 }
{ "type": "replace", "pattern": "\\s*\\|.*$", "with": "" }
```

- `regex`: keeps capture `group` (default `1`) of the first match. No match means no value.
- `replace`: replaces every match of `pattern` with `with` (default `""`). `$1` refers to a group.

Keep patterns to the common regex subset (character classes, groups, quantifiers, anchors). Avoid
lookbehind and named groups, so the configs stay portable across regex engines.

### Date

```json
"date": { "format": "dd.MM.yy HH:mm", "timezone": "Asia/Jerusalem" }
```

| Key        | Default | Meaning                                                                 |
|------------|---------|-------------------------------------------------------------------------|
| `format`   | `"iso"` | `"iso"`, `"epochSeconds"`, `"epochMillis"`, or a pattern.               |
| `timezone` | `"UTC"` | IANA zone, used when the value has no UTC offset.                       |

- `iso`: an ISO-8601 instant with offset (`2026-10-26T09:15:00+02:00`, `...Z`), or a local
  date-time without offset (`2026-10-26T09:15`), which is interpreted in `timezone`.
- Patterns use the tokens `yyyy`, `yy` (20xx), `M`/`MM`, `d`/`dd`, `H`/`HH`, `m`/`mm`, `s`/`ss`.
  Every other character matches literally. The pattern may match **anywhere** in the value, so
  `"Updated: 26.10.26 09:15"` matches `dd.MM.yy HH:mm`. Missing time parts are `00`.
- An unparseable date gives no value. It never fails the article. Each `fallback` uses its own
  `date` spec.

Output is always a UTC instant, serialized as `2026-10-26T07:15:00Z`.

## URL resolution

`list.url`, `list.image` and `article.image` are resolved against the URL of the page they were found
on, like a browser does: absolute URLs are kept, and `//host/x`, `/x`, `x`, `../x`, `?q` and `#f` resolve
relative to the page. Dot segments are collapsed.

## Feed

A site that keeps an RSS feed may describe it in `feed`, with the same keys as [List](#list) (an RSS feed is XML:
`item` selects the feed's `item` elements, and fields read their child elements). It is an alternative to `list`,
not an addition: an engine reads `list` unless told to read the feed for that site (the app has a switch per site,
off by default). Both are validated, and both carry fixtures (`kind: "feed"`). `categories` serves both, so it holds
the section names of each.

## Golden fixtures

Every source directory carries saved pages and the output they must produce:

```
sources/<id>/
  config.json
  fixtures.json          # manifest, see below
  fixtures/list.html
  fixtures/article-1.html
  expected/list.json     # array of ArticleSummary
  expected/article-1.json  # one Article
```

`fixtures.json` lists `{ "kind": "list" | "feed" | "article", "url": <original page URL>, "html": <path>,
"expected": <path> }`. It needs at least one list fixture and one article fixture; a source with a `feed` also has
feed fixtures (pages of the feed, read with its rules, conventionally `fixtures/feed-*.xml|html`). `url` is the
address the page was downloaded from; it is needed to resolve relative links.

Expected output is compared as JSON, ignoring key order. Optional values that are absent are
**omitted**, not written as `null`.

### Output shapes

`ArticleSummary`: `sourceId`, `url`, `title`, and optionally `summary`, `imageUrl`, `publishedAt`,
`category` (a shared category id).

`Article`: `sourceId`, `url`, `title`, optionally `subtitle`, `author`, `publishedAt`, `imageUrl`, `category`,
`body` (array of paragraph strings, possibly empty), `blocks` (with `figures`: an array of
`{"type": "text", "text"}` and `{"type": "image", "url", "caption"?}` in reading order) and `video`
(`{"poster"?, "stream"?, "embed"?, "duration"?}`).

## Changing the schema

Additive, optional keys can be added within version 1. Anything that changes how an existing config
is interpreted requires bumping `schemaVersion`, updating this document, and keeping every source's
fixtures green.
