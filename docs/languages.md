# Language Knowledge in Extensions — Design Detail

Read this before touching extension manifests, `backend/extensions/manifest_validate.go`,
`frontend/src/lib/syntax/`, `frontend/src/lib/utils/editor-language.ts` or `highlighter.ts`.
The hard rules live in `AGENTS.md → Language Knowledge Lives in Extensions`; this file is the why.

This is the record of a **completed** refactor: language knowledge was moved out of six separate
core places (two independent ext→grammar maps, a 278-line hand-written Go-template grammar,
display-label maps, `jsTsLanguageIds`/`wireLanguageId`) into the LSP extension manifests, leaving a
generic engine plus a documented fallback.

## Why `language` and `language_id` are two fields

The browser's LSP channel takes its routing key from `resolveFile(path).language` — the same call that
chooses the grammar — so a file can never be opened against one server while being announced to it as
another language.

`file_types[]` (`{extension, language_id, label, syntax}`) replaced the flat `extensions: [...]`
list because one extension serves `ts`/`tsx`/`js`/`jsx` under `language: "typescript"` while
tsserver needs four different ScriptKinds.

`@codemirror/lsp-client` takes the languageId from `Language.name` when the caller passes none, so
a `.ts` file whose grammar was `javascript()` was announced as JavaScript and every `interface`
came back as *"'interface' declarations can only be used in TypeScript files."*

`language` stays the **routing** key (which server owns the file); `language_id` is **per file
type**. Collapsing them breaks routing (`lookupExtension("javascript")` finds nothing) or the parse
(`.tsx` as `typescript` answers a wall of `',' expected.`).

## Why `syntax` is data and never code

An extension shipping a JavaScript module would run it in the app's realm. So a syntax profile is
pure data: `line_comment`, `line_comments[]` (PHP's `#` beside `//`), `block_comment`, `strings[]`
(quote = one character, or that character repeated — Python's `"""` is a string form), `pairs[]`,
`keywords`/`types`/`constants`/`builtins`, `numbers`, `region`.

`frontend/src/lib/syntax/spec-language.ts` turns one profile into a `StreamLanguage` **plus** the
generic bracket `foldService` (a fold is an opener still open at its line's end, and brackets inside
comment/string tokens are skipped, so `// if (a) {` and `const s = "{"` open nothing), a region
folder (live in the TypeScript profiles as `//#region` / `//#endregion`), an `indentService`, and
the `commentTokens` / `closeBrackets` language data — colours, folding, indent, Ctrl-/ and
auto-close all come from the same declaration.

A spec-built grammar is named `mike-declared`, deliberately *not* a valid language id, so a call
site that forgets to pass an explicit id gets a loud "no provider" from the server instead of
silently wrong diagnostics.

## Accepted quality trade-off

A spec-built stream grammar has no parse tree, so folding is bracket-based and the outline relies on
LSP document symbols or the regex extractors that remain for the fallback set. Consequence:
`@codemirror/lang-*` for anything an extension covers (js/ts, go, python, rust, cpp, php, svelte) is
out of the dependency set — only the fallback grammars remain (lang-javascript survives
transitively, via lang-html).

## Tests

`backend/extensions/manifest_validate_test.go`, `backend/lsp/docsync_test.go` (per-file wire id),
`backend/lsp/tsserver_integration_test.go` (real tsserver refuses `.jsx` when the id collapses to
`typescript`), `frontend/src/lib/syntax/__tests__/spec-language.unit.ts` (tokens, folds, indent out
of a profile), `frontend/src/lib/utils/__tests__/editor-language.unit.svelte.ts` (resolution order:
extension → fallback → plain text), and the live probe
`frontend/tests/declared-languages-check.mjs` (wire ids sniffed off the WebSocket, plus highlight,
block/region folding, Ctrl-/ in a real editor:
`MIKE_CHECK_PROJECT=<scratch project> node tests/declared-languages-check.mjs` — it writes fixtures
into that project, so the name is required).
