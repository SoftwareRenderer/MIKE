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

The tests are written as the payload list plus a control case for each refusal, proving it is this config
and not DOMPurify's, each control written against the shape the defaults **keep** — a refusal tested only
on the shape they already refuse proves nothing: `src/lib/utils/__tests__/sanitize.unit.ts`, scanned by
the shared `__tests__/html-hazards.ts`, which reports a `<style>` element rather than assuming it away.

## `html-sinks.py`

It lists every HTML sink with the mechanism that keeps it safe, and rule 4 checks the claim: a file saying
`sanitizer` must import the module, `guard:<Ident>` must still contain that check, `escaped-by:<module>`
must import a helper that still escapes. `html-sinks_test.py` holds each rule in both directions, because
a sink check that stopped matching prints the same clean line as a sink that is safe.

## `Icon.svelte`

Accepts a lone sanitized `<svg>` root and nothing else (`singleSvgMarkup`). Its `{@html src}` was only ever
safe because of what its call sites pass — a build-time `*.svg?raw` import — and a component cannot see its
call sites, so it checks the shape itself.
