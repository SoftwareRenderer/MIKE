# Untrusted markup in the browser

The rule is one line in `AGENTS.md → Frontend`. This is what the sanitizer changes, why, and how the gate
checks its own claim.

## `$lib/utils/sanitize` (`sanitizeHTML` / `markdownToSafeHTML`)

Everything the app did not write goes through it: chat and tool-call markdown (an LLM's words plus the
output of tools that read an untrusted repository), an agent manifest's description, language-server hover
documentation, **and the language server's completion/signature documentation** — which reaches
`innerHTML` inside `@codemirror/lsp-client` (`renderDocInfo`, the signature-help docs element), a
dependency `html-sinks.py` does not scan. The only control there is the `sanitizeHTML` field of
`lspClientConfig`, and a config object that omits it is an unsanitized sink that no gate notices
(`__tests__/lsp-doc-sanitize.unit.svelte.ts` holds the omission open by asserting the payload survives
without the hook).

Three deliberate differences from DOMPurify's defaults:

- It drops the `style` **attribute**. The defaults keep
  `<p style="position:fixed;inset:0">…` byte for byte, and nothing above a chat bubble establishes a
  containing block, so model-authored markup would otherwise paint a full-screen fake dialog in the app's
  own theme.
- It drops the `style` **element**. The defaults refuse a *bare* `<style>` and keep one nested inside
  anything, and `marked` wraps block HTML in a container on the way through — so the attribute refusal
  alone left the same layout power available in a different syntax, and an LLM's
  `<div><style>…</style></div>` installed a page-wide stylesheet from a chat bubble.
- It refuses `<form>`. The defaults keep `<form action=…><input><button>` whole and `marked` passes an
  inline one through, which leaves a live submit control inside a chat bubble. `input` stays because
  `marked` emits a disabled checkbox for every `- [ ]`.

`class` stays — the LSP plugin colors fenced code with `tok-*`.

A fence whose info string is `markdown` is rendered, not shown, so an answer carrying a fenced document
arrives as a document. Every other tag — `md`, `text`, a language, none at all — keeps the code block:
that is how generated text displays markup the reader is meant to see. The embedded parse uses a
`marked` instance with no renderer of its own, so a `markdown` fence *inside* one is source again — one
nested parse per block. An unclosed fence renders the same way, so a streaming answer never reverts to
raw source.

The nested HTML joins the outer parse's output before either is sanitized, so one `sanitizeHTML` call
covers both. `__tests__/sanitize.unit.ts` re-runs the payload list through a fence to hold that, with
"no `<pre>` in the output" as its control: unrendered, a payload is escaped text and inert for the wrong
reason. `__tests__/chat-markdown-fence.unit.svelte.ts` proves the chat bubble and the tool card go
through the same helper.

### The chat bubble splits its fences out

`Chat/MarkdownBody.svelte` renders an assistant message as segments rather than as one HTML string.
`markdown-segments.ts` lexes the text and lifts every top-level code token into a segment of its own;
the prose between two fences returns to `markdownToSafeHTML` as one run, so a fence cannot move the
paragraphs around it. `isRenderedFence` is the tag decision the split and the renderer above share.
`src/lib/utils/__tests__/markdown-segments.unit.ts` holds both claims: the segments are the message in
order, and the prose renders as the whole message did.

A code segment is `Chat/CodeFence.svelte`, so its copy control is a real element with a handler, scoped
styles and a catalog key for its tooltip — button text never crosses the sanitizer, which is rule 2's
reason read from the other side. A fence nested in a list item or a blockquote stays in the prose run and
carries no control, and so does a fence whose body is empty. A code segment also carries `open`: whether
the fence's closing delimiter has arrived. A streaming message ends in one, and only a fence that draws
something needs the difference.

### A diagram fence is drawn as an image

A closed ` ```mermaid ` fence with a body is drawn. `isDiagramFence` owns the tag, beside `isRenderedFence`;
`Chat/DiagramFence.svelte` owns the bubble's half and `src/lib/utils/diagram.ts` owns everything below it.
An open fence stays `CodeFence`, because half a diagram is not a diagram and its source is the honest
rendering of it. A nested fence, an empty one and a message the user wrote are text as well.

**What reaches the screen is an image.** The renderer returns an SVG document, and that document becomes
`<img src="data:image/svg+xml;base64,…">`. A browser loads an SVG image as a document of its own: its
stylesheet styles that document and nothing here, its scripts and event handlers never run, its external
references are never fetched, and its links do not navigate. The markup therefore reaches no sink — no
`{@html}`, no `innerHTML` — and rule 5 accounts for the one URL binding. `src/lib/utils/diagram.ts` builds
the only `data:` URL this app builds at runtime, from a literal prefix; the other `data:` in the tree is a
literal icon in `FileEditor.svelte`'s CSS, which no message can reach.

**`svgImageMarkup` is the one helper in `sanitize.ts` that keeps `style`,** and the consumer is the reason.
It sanitizes with DOMPurify's defaults plus `form` refused, requires exactly one `<svg>` element with
nothing beside it, sets `xmlns`, and writes the bytes with `XMLSerializer` so they parse as
`image/svg+xml`. A `<!DOCTYPE>` carrying an internal subset is refused rather than filtered: it does not
survive the parse that produced the DOM being re-serialized. Two things DOMPurify keeps are refused here —
the **root's own** `style` attribute, which is the one declaration in the tree that could move this page's
layout, and any `href`, `xlink:href` or `src` that is not an internal `#fragment`. The contract is the
consumer: these bytes are for an image document and never for `innerHTML`.

**What the renderer is not told.** `diagram.ts` sets `securityLevel: 'strict'`, `htmlLabels: false`,
`suppressErrorRendering: true` and `startOnLoad: false`, and names those keys — plus `themeCSS`,
`themeVariables`, `fontFamily` and `maxTextSize` — in the library's `secure` list, which is how a diagram's
own `%%{init}%%` directive is refused. It caps the source before the parser sees it, and the markup before
it becomes a URL. It renders into a `div` it creates and removes whatever the render does, and runs one
render at a time, because the library keeps its configuration in one process-global object. A refused
diagram is the source plus one catalog line. There is no second attempt with weaker settings.

**Where CSP is the control.** Text measurement needs layout, so the library builds the diagram inside a
live element before serializing it. For the length of a draw, markup that no sanitizer of ours has seen is
in this document, off-screen and `aria-hidden`; `script-src 'self' 'nonce-…'` is what stops anything in it
running. The font the diagram draws with is the page's computed `font-family` through `safeCSSValue`,
because an image document resolves no custom property of ours.

**What it costs.** A `journey` diagram draws its labels in a `<foreignObject>`, so its boxes arrive
without their words: keeping them would widen DOMPurify's SVG allowlist to HTML in the one path that keeps
styles, and `__tests__/chat-diagram-fence.unit.svelte.ts` holds the no-HTML boundary instead. A `mermaid`
fence in a tool body stays a `<pre>`, because `Chat/ToolCall.svelte` renders one HTML string and has no
fence split to hang a component on.

The renderer is 116 chunks and 4.9 MB on disk. The first flowchart of a session fetches 45 of the built
chunks — 34 of them the renderer's — 1.2 MB gzipped; a chat with no diagram fetches none. The service worker
precache carries 28 entries and 2.7 MB, against 144 entries and 7.6 MB for the same build with the deny list
removed: every diagram type into every install, whatever that install's chats contain.
`scripts/mermaid-chunks.ts` names every chunk the app cannot reach except through the library, and the
precache refuses that name. `npm run build` prints the chunk names and the precache line, and fails when the
library is in the graph and no chunk got the name: a deny list matching nothing would otherwise re-add the
megabytes quietly.


The tests are written as the payload list plus a control case for each refusal, proving it is this config
and not DOMPurify's, each control written against the shape the defaults **keep** — a refusal tested only
on the shape they already refuse proves nothing: `src/lib/utils/__tests__/sanitize.unit.ts`, scanned by
the shared `__tests__/html-hazards.ts`, which reports a `<style>` element rather than assuming it away.

## `__tests__/html-hazards.ts`

One definition of "what must not survive sanitizing", shared by the sanitizer suite and the component sink
suite so a new hazard class lands in both instead of drifting apart. It reads the DOM the browser built,
not a pattern matched against markup — and `describe('the hazard scanner itself')` feeds it un-sanitized
payloads, because "no hazards found" is also what a scanner that went blind prints.

Two shapes are measured as *surviving* and are deliberately not hazards: `<template>` (through `marked`;
bare, it is dropped) and `<slot>`. Both keep their children with the handlers stripped, and both are inert
unless something clones the contents or mounts them in a shadow root — nothing in core does, and there is
no shadow DOM at all. The suite says so out loud, so a change that starts cloning fragment contents has to
make them hazards on purpose rather than by omission.

## `html-sinks.py`

Why it exists: `Card.svelte` once built a menu row by concatenating an icon and a `t()` label into
`innerHTML`. CSP does not settle that — `frame-src 'self'` plus the same-origin `/extensions/` tree is a way
back into the page — so the row is built with DOM APIs now and the gate keeps the surface from growing back.

Rules 1 to 4 are the markup sinks. Every file with a sink is listed with the reason it is safe (1), no sink
may render a translation (2), a listed file with no sink left in it fails so the table cannot become
history (3), and each entry declares in `METHOD` how it stays safe (4): `sanitizer` must import the module
and not DOMPurify, `guard:<Ident>` must still contain that check, `escaped-by:<module>` must import a
helper that still escapes, and `literal` is the one form with nothing mechanical to check. Rule 2 reads the
sink's whole statement rather than the line it started on, because a template may spread a `{@html …}`
expression over lines and a one-line window would report nothing. Scope is every file the browser runs —
`src/`, plus `index.html` and `public/host-client.js`, which sit outside `src/`.

Rules 5 to 7 cover the three ways the browser is handed something to interpret that never reaches the
sanitizer, so the sanitizer cannot speak for them:

- **5 — every URL.** An `href`/`src`/`srcdoc`/`action`/`formaction`/`poster`/`xlink:href` bound to `{…}`
  or set with `setAttribute` passes only as an asset import (a file the build chose), as a call to a
  scheme/data-URL guard — whose marker is re-read from the guarding module, so a `safeUrl` that stopped
  testing `protocol` is a failure, not a name — or as a `URL_SINKS` entry whose mechanism is checked
  (`contains:`, `encoded-by:`). A `javascript:` URL in a link is markup the browser runs with no
  `{@html}` anywhere in sight.
- **6 — no dynamic tag.** `<svelte:element this={X}>` must name a string literal: X is
  `createElement`'s argument, and `<script>` is one of the names.
- **7 — no text in a style attribute.** Every `{…}` inside `style="…"` or a `style:prop={…}` passes only
  when the attribute supplies the CSS unit, which leaves a number as the only thing that can fit;
  a color or a bare custom property needs a `STYLE_SINKS` entry (`hex-colors:` re-reads the module the
  colors come from). A style attribute is the layout power the sanitizer refuses for untrusted markup —
  `position:fixed;inset:0` paints over the app in its own theme — arriving through the app's own template
  instead, with `style-src 'unsafe-inline'` meaning CSP is not the control. A declaration whose site has
  gone is reported, so the tables cannot become history.

`html-sinks_test.py` holds every rule in both directions, including the status words: rendering is a
function the self-test reads, because the first version of this gate printed `ok` beside its own list of
violations (it tested the truthiness of the string `"FAIL"`).

What it still cannot see: a value carried into a sink through a variable, and a builder like
`highlighter.ts` whose output a sink later renders. Those need the per-component test, and every sink
listed here has one.

## `Icon.svelte`

Accepts a lone sanitized `<svg>` root and nothing else (`singleSvgMarkup`). Its `{@html src}` was only ever
safe because of what its call sites pass — a build-time `*.svg?raw` import — and a component cannot see its
call sites, so it checks the shape itself.

The same argument covers its `style` attribute, which interpolates the `color` prop: escaping the quotes is
not the control there, because inside a `style` attribute a `;` simply starts another declaration and
`url()` is a fetch. `safeCSSValue` refuses a value that can do either, and the sizes are coerced through
`Number` so a value that broke the `number` type cannot arrive as text.
## Checking this

`cd frontend && npm run check:sinks` audits every sink, URL and style binding, and audits its own claim.
`npm run check:sinks-test` proves that audit is capable of failing.
`npm test` holds the rest. `__tests__/sanitize.unit.ts` runs the payload list through both markup paths and
the SVG-document path. `__tests__/diagram.unit.ts` pins what a directive may not change, and that the bytes
handed to the `<img>` are the sanitized ones. `__tests__/chat-diagram-fence.unit.svelte.ts` pins which
fences get drawn at all.
`npm run build` prints the precache line and the chunk names, and fails when mermaid is in the graph and no
chunk is named for it; `scripts/mermaid-chunks.ts` decides that name from the module graph rather than from
a package list someone stops maintaining.
