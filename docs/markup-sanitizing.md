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
