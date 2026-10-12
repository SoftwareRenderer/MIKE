# Untrusted Markup in the Browser

The core rule is in `AGENTS.md → Frontend`. This document describes how the app sanitizes untrusted markup, why certain defaults are overridden, and how the audit system ensures no unsanitized sinks are introduced.

## The Sanitizer (`$lib/utils/sanitize`)

All content not authored by the app (e.g., chat messages, tool output, LSP hover documentation) is processed by `sanitizeHTML` or `markdownToSafeHTML`.

**Differences from DOMPurify Defaults**:
To prevent specific attack vectors, the sanitizer applies these additional restrictions:
- **Drop `style` attributes**: Prevents attackers from using `position: fixed; inset: 0` to create full-screen fake dialogs.
- **Drop `style` elements**: Prevents the installation of page-wide stylesheets via `<style>` tags.
- **Refuse `<form>`**: Prevents live submit controls (inputs, buttons) from appearing inside chat bubbles.

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

## Verification

- `npm run check:sinks`: Audits all sinks, URLs, and style bindings.
- `npm run check:sinks-test`: Verifies that the audit tool itself can detect violations.
- `npm test`: Runs the sanitizer suite (`__tests__/sanitize.unit.ts`) and diagram tests.
- `npm run build`: Ensures the Mermaid library chunks are correctly excluded from the precache to minimize bundle size.
