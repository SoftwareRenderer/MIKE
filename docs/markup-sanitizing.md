# Untrusted Markup in the Browser

The core rule is in `AGENTS.md → Frontend`. This document describes how the app sanitizes untrusted markup, why certain defaults are overridden, and how the audit system ensures no unsanitized sinks are introduced.

## The Sanitizer (`$lib/utils/sanitize`)

All content not authored by the app (e.g., chat messages, tool output, LSP hover documentation) is processed by `sanitizeHTML` or `markdownToSafeHTML`.

**Differences from DOMPurify Defaults**:
To prevent specific attack vectors, the sanitizer applies these additional restrictions:
- **Drop `style` attributes**: Prevents attackers from using `position: fixed; inset: 0` to create full-screen fake dialogs.
- **Drop `style` elements**: Prevents the installation of page-wide stylesheets via `<style>` tags.
- **Refuse `<form>`**: Prevents live submit controls (inputs, buttons) from appearing inside chat bubbles.
- **Refuse `id` and `name`, and DOMPurify's default-kept `data-*`**: A fragment that names an element is what `getElementById` and `aria-labelledby` find first.
- **Keep only two `class` shapes**: an unfiltered `class` reaches the same layout power as `style`; `Class Adoption and the Stylesheet` holds the two shapes that survive and why.

**Why each refusal is this shape**:

- DOMPurify's defaults already remove what executes: handlers, `javascript:` and `data:` URLs,
  `<script>`/`<iframe>`/`<object>`/`<base>`/`<link>`, and the mXSS containers. What they keep, this refuses.
- DOMPurify drops a **bare** `<style>` and keeps one nested in a container, and `marked` wraps most block
  HTML in a container. So the element is refused beside the attribute; `FORBID_CONTENTS` covers `style`,
  which takes the rules with it instead of leaving them as visible text.
- `<input>` stays: `marked` emits a disabled checkbox for every `- [ ]` task list item, and a control
  with no form to submit is inert.
- CSP is a second layer, never the reason. `form-action 'self'` and `img-src` stop a submit or a fetch;
  a live submit control in the app's own chrome is what gets refused.
- A call site may add a refusal through `forbidTags` and never remove one. Hover cards drop `<hr>`:
  a language server emits section rules the card's own layout already provides.
- Each of these was measured on the pinned DOMPurify. `__tests__/sanitize.unit.ts` is written as the
  payload list, not as a description of the config.

**Markdown Fences**:
- Fences tagged as `markdown` are rendered as HTML.
- All other tags (e.g., `js`, `py`, or no tag) are rendered as plain code blocks.
- One level of nested parsing is supported; a `markdown` fence inside another is treated as source.

### Render Cache and Segments

To optimize performance, `markdownToSafeHTML` caches the most recent results. This prevents redundant parsing and sanitizing of streaming answers.

`Chat/MarkdownBody.svelte` uses `markdown-segments.ts` to split messages. Top-level code fences are lifted into `Chat/CodeFence.svelte` components, which provide:
- Dedicated copy buttons.
- Scoped styles.
- Tooltips.

Fences nested within list items or blockquotes remain part of the prose and do not receive these controls.

## Class Adoption and the Stylesheet

A `class` selects a rule the application already ships, so it reaches the same layout power the
`style` refusal exists to deny — by an attribute the sanitizer keeps. A fragment carrying
`class="explorer-view fullscreen"` reaches that rule: the bubble covers the viewport at
`position: fixed` and modal depth, in the application's own colors and fonts. It is inert markup
plus CSS the app shipped, so no CSP rule fires and no sink looks unusual — a fake sign-in panel
with a working hyperlink, and the model's own words on it.

Two things hold it closed, and both are needed:

- **`KEPT_CLASS` in `sanitize.ts` is the control.** A class token survives only as `language-<tag>`
  or a CodeMirror-generated tag class (today `ͼ`-prefixed), because those are the only classes a
  renderer this app feeds emits. Everything else is refused per token.
- **`css-surface.py` is the tripwire.** It fails when a rule whose *subject* a fragment could name
  declares `position: fixed`. Scoped out of the check on purpose: `absolute` and `sticky` keep an
  ancestor as their containing block, so they cover a region the fragment already shares rather
  than the application.

What the gate reads, and what a fix must respect:

- A rule is adoptable when a fragment can name what its declarations land on. That is the **subject** —
  the last compound selector — when it carries no Svelte scope hash, no `#id` and no `[attribute]`,
  and is unscoped. Unscoped means written through `:global()`, or sitting in a stylesheet no
  component `<style>` block imports, which Svelte therefore never scopes.
- **Scoping an ancestor proves nothing.** `.chat-messages :global(.x)` matches a fragment carrying
  `class="x"`, because the fragment sits inside the app's own `.chat-messages`. Only a scoped
  *subject* is out of a fragment's reach.
- Renaming a class is not a fix: a fragment prints whatever name you choose. The scope hash is
  defence in depth (it changes per build and per component), not the boundary — the boundary is
  `KEPT_CLASS`.
- Hiding power is out of scope too: `display: none` on a global class reaches the app's own
  elements whether or not a fragment names them, so a fragment adds nothing to the risk.

## Mermaid Diagrams

Fences tagged ` ```mermaid ` are rendered as SVG images.

**Security Model**:
The rendered SVG is converted to a base64 data URL and placed in an `<img>` tag:
`<img src="data:image/svg+xml;base64,...">`

This approach provides strong isolation:
- **No Scripts**: Scripts and event handlers in the SVG do not run.
- **No External Requests**: External references are not fetched.
- **No Navigation**: Links inside the SVG do not navigate the main page.

**Implementation**:
The `svgImageMarkup` helper ensures the SVG is a single root, removes the root `style` attribute (which could affect page layout), and blocks any `href` or `src` that is not an internal `#fragment`. The library is configured with `securityLevel: 'strict'` to ignore `%%{init}%%` directives.

## Sink Auditing (`html-sinks.py`)

`html-sinks.py` is a static analysis tool that audits the codebase for "sinks"—places where markup is rendered (e.g., `innerHTML`).

**The Audit Rules**:
1. **Safety**: Every sink must be justified (e.g., it uses the sanitizer).
2. **No Translations**: No sink may render a translation string.
3. **Persistence**: Every listed file must still contain the sink; otherwise, the entry is removed.
4. **Declaration**: Every sink must declare its safety mechanism (e.g., `sanitizer`, `guard:<Ident>`, `escaped-by:<module>`).

**URL and Style Sinks**:
The auditor also checks every `href`, `src`, and `style` binding.
- **URLs**: Must be an asset import, pass a scheme/data-URL guard, or be listed in `URL_SINKS`. This prevents `javascript:` URLs.
- **Styles**: Must be a numeric value with a CSS unit or listed in `STYLE_SINKS`. This prevents layout-breaking CSS (e.g., `position: fixed`).

## `Icon.svelte`

`Icon.svelte` accepts a single sanitized `<svg>` root. It uses `singleSvgMarkup` to verify the shape and `safeCSSValue` to ensure that the `color` prop does not introduce invalid CSS or external references.

Its `src` is a `?raw` import of a file in `frontend/src/lib/assets/icons/`. A glyph two components draw is
one file there, never markup repeated in each — an icon that is not a single `<svg>` root renders nothing
at all.

`singleSvgMarkup` sanitizes before it judges the shape, so the parse that decides is DOMPurify's and what
survives is re-read from the DOM it built rather than matched against a pattern.

## Verification

- `npm run check:sinks`: Audits all sinks, URLs, and style bindings.
- `npm run check:sinks-test`: Verifies that the audit tool itself can detect violations.
- `npm run check:css-surface`: Fails when a rule a foreign fragment can name pins an element to the viewport.
- `npm run check:css-surface-test`: Verifies that gate in both directions — a gate that stopped matching prints the same `ok`.
- `npx vitest run src/lib/components/__tests__/icon-assets-render.unit.ts`: every shipped icon asset passes
  `singleSvgMarkup`, so a glyph edit cannot silently erase the buttons that name it.
- `npx vitest run src/lib/utils/__tests__/lsp-doc-highlight.unit.svelte.ts`: reads the editor theme's class
  for a tag off a live view and asserts the sanitizer keeps it, so a rename in the pinned library fails
  there rather than losing the coloring quietly.
- `npm test`: Runs the sanitizer suite (`__tests__/sanitize.unit.ts`) and diagram tests.
- `npm run build`: Ensures the Mermaid library chunks are correctly excluded from the precache to minimize bundle size.
