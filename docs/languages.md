# Language Knowledge in Extensions — Design Detail

Read this before touching extension manifests, `backend/extensions/manifest_validate.go`,
`frontend/src/lib/syntax/`, `frontend/src/lib/utils/editor-language.ts` or `highlighter.ts`.
The hard rules live in `AGENTS.md → Extensions`; this file is the why.

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
folder (live in the TypeScript profiles as `//#region` / `//#endregion`), an `indentService`, the
`commentTokens` / `closeBrackets` language data, and bracket pair colorization built from `pairs` —
colors, folding, indent, Ctrl-/ and auto-close all come from the same declaration.

## Where a name's color comes from, and a bracket's

The word lists are a language's *vocabulary*; a name they do not contain gets its color from its
**shape**, in this order: declared `keywords`/`types`/`constants`/`builtins` first, then a name with `(`
after it (spaces only between) is a function, then a name starting with an upper-case letter is a type,
then nothing. So `func` is a keyword, `formatChatHistory(` is a function and `types.ChatMessage`'s
`ChatMessage` is a type, while `messages` keeps the theme's default color. Both shape rules default on
and are declared per profile as `call_as_function` / `capitalized_types` — declared, not required: an
extension installed before the fields existed highlights anyway (that is what the default is for), and an
author whose language breaks the convention (a Lisp, an Erlang, where variables start upper-case) has a
field to say it in. Go's exported constants (`EOF`, `MaxLen`) start upper-case and so read as types; that
is the accepted trade, not an open bug.

Bracket pair colorization follows VS Code's rule: a pair's color is its **nesting level**, so an opener
always matches the closer it opened (`syntax/bracket-pairs.ts`). Levels count across lines, cycle over
three slots, skip anything inside a comment or a string, and color nothing for a closer that opens
nothing. The colors belong to the theme (`.cm-bracket-l0|1|2`, VS Code's own values, different between
light and dark), which keeps the engine out of palettes. Two scan bounds and they are not the same
number: a fold needs its own line and body (`SCAN_LIMIT`), while a level needs everything above the
viewport, so the bracket scan has its own larger `BRACKET_SCAN_LIMIT` and colors nothing past it.

`buildLanguageSupport(spec, name)` owns everything a declared language does — both arguments, the name
being `mike-declared` at the one production call site, never a language id. Its consumers are the editor
(`editor-language.ts`), the outline, the LSP tooltip renderer and the tests; the diff view reads the same
specs but feeds `bracketLevelsForTree` its parsed `Tree` rather than an editor state, because it has no
view.

## A code fence in a language server's documentation

A hover card opens with a signature: gopls sends
`​```go\nfunc Println(a ...any) (n int, err error)\n```​`, and tsserver, rust-analyzer and phpactor send the
same shape in their own tag. `@codemirror/lsp-client` highlights a fence only when the tag resolves to a
`Language` — either the tag equals the *editor's* grammar name, or `LSPClientConfig.highlightLanguage(tag)`
answers it. Every grammar we build is named `mike-declared`, so the first rule matches nothing:
`lsp-client-config.ts` sets the hook to `getGrammarForFence(tag, filePath)`, which tries in order the
declaration whose `language_id`, `language` or `extension` equals the tag (**no alias table** — `python`
and `py` resolve, `golang` does not), the core fallback table (a fence tag and a file suffix name the
same grammar), and **the grammar of the file the documentation is about** — which is the answer for an
unlabelled fence, a doc comment's indented block, and for a language nothing installed declares.
Markdown's `text`/`txt`/`plain`/`plaintext` ask for prose and get no grammar, checked before all of it.

The hook lives on the **client**, so hover, completion documentation and signature help read the same
way, and both `LSPClient` constructions go through one factory — an option the reconnect drops is a
feature lost the first time the socket does. `getFile` is a getter because a client outlives the file it
connected for. Colors come from the editor's mounted `HighlightStyle`, so a tooltip follows the theme
and no `hl-*` palette is duplicated for it.

A spec-built grammar is named `mike-declared`, deliberately *not* a valid language id, so a call
site that forgets to pass an explicit id gets a loud "no provider" from the server instead of
silently wrong diagnostics.

## Accepted quality trade-off

A spec-built stream grammar has no parse tree, so folding is bracket-based and the outline relies on
LSP document symbols or the three regex extractors that remain for the file types whose outline is not
in a parse tree at all (`utils/editor-outline.ts` — Go templates, Markdown headings, a Svelte
component's `<script>`; a file none of them covers gets an empty outline, which is the honest answer). Consequence:
`@codemirror/lang-*` for anything an extension covers (js/ts, go, python, rust, cpp, php, svelte) is
out of the dependency set — only the fallback grammars remain (lang-javascript survives
transitively, via lang-html).

## Which helper reads the manifest

`file_types[]` (`{extension, language_id, label, syntax}`) is the routing table and the source of the `didOpen`
languageId and the status-bar label. Go reads it through `extensions.LSPConfig.fileTypeFor`
(`MatchesFile`/`LanguageIDFor`, `ServerConfig.LanguageIDFor`); the browser reads it through
`editor-language.ts::resolveFile`. There is no other reader and no local map, and the browser's LSP channel takes
its routing key from the same `resolveFile` call that chooses the grammar.

## What one shared scan needs

Fold, indent and bracket matching all ask the same question — "is this range code?" — and share one reader for the
answer (`syntax/stream-tokens.ts`). Splitting it is how a `"{"` becomes a bracket in one place and not another.

## An old manifest is normalized, not migrated

`backend/extensions/manifest_validate.go` refuses a bad manifest on install, on update and on
`PUT /api/extensions/{id}`; it is never stored. `LSPConfig.NormalizeFileTypes()` in `scanExtension` fills in what
an older manifest lacks, so runtime code reads one shape and there is no migration step.

## A grammar is never gated by the LSP switch

`editor_enabled` (Settings → Extensions → "Editor Features: diagnostics, completions, go-to-definition, and
hover") decides whether a *channel* opens, so `lsp-connection-manager.isLSPAvailable()` is the only reader of
`ResolvedFile.editorLSP`. `resolveFile()` answers the file type, wire id and `syntax{}` profile from
`getEnabledLSPExtensions()` (installed + enabled), because that answer is a data read from a manifest already on
disk, not a server call. Answering `spec: null` when the switch is off is how one checkbox in Settings turned
`.py`, `.ts` and `.js` back into plain text — in the diff view too, which has no LSP client at all. A disabled
extension is a different thing: it claims nothing.

## The one language table left in core

`frontend/src/lib/utils/fallback-languages.ts` (json, markdown, html, css/scss/less, yaml, sql, `tmpl`/`gohtml`)
is consulted only when no extension declares the file type. It answers a markdown fence tag too
(`fallbackLanguageForTag`), because a fence tag and a file suffix name the same grammar. `highlighter.ts` (diffs)
resolves through the same `getLanguageSupport`, so a diff and an editor cannot disagree about a file. It is a
`Map`, not an object literal: the key comes off a filename, and a literal answers inherited keys as well —
`foo.constructor` resolves to `Object`, which is truthy, and CodeMirror then refuses to open the file.

## One token table, one token stylesheet

`frontend/src/lib/syntax/token-style.ts` is the only place a token gets a color. The editor's two
`HighlightStyle`s are built from it (`buildHighlightStyle`), and the diff highlighter (`diffHighlighter`) emits
the class names that `frontend/src/lib/styles/token-colors.css` paints from the `--cm-*` variables — one list
behind both diff renderers (`DiffViewer`, and `CommitDiffOverlay`, whose side-by-side columns have no
`.diff-text` wrapper). Five hand-written lists described these tokens before, and the drift was silent: a profile
tags `true`/`None`/`nil` as `tags.atom`, `atom` has no parent tag, and the `constant(keyword)` entry most of the
lists carried matched nothing, so constants came out in the default text color everywhere. Two tags that mean
"constant" reach the table (`tags.atom` for a stream token, `tags.bool`/`tags.null` for the core JSON grammar) and
share one class: a tag with no parent needs its own row, or it inherits a color nobody chose.

## Tests

- `backend/extensions/manifest_validate_test.go` — the validator's refusals.
- `backend/lsp/docsync_test.go` — the wire id, per file.
- `backend/lsp/tsserver_integration_test.go` — real tsserver refuses `.jsx` when the id collapses to `typescript`.
- `frontend/src/lib/syntax/__tests__/spec-language.unit.ts` — tokens, folds and indent out of a profile,
  including what each shape rule does switched off.
- `frontend/src/lib/syntax/__tests__/bracket-pairs.unit.ts` — nesting levels, comments and strings, and the
  classes a real `EditorView` puts in the DOM.
- `frontend/src/lib/syntax/__tests__/bundled-profiles.unit.ts` — the shipped manifests, and a profile with neither
  new key, which is the state of every install that predates them. It reads the sibling checkout from
  `frontend/../..`: from `frontend/..` the path never exists and the whole file skips green without measuring
  anything, which is how it shipped once. A manifest missing from a checkout that is there now fails the file.
- `frontend/src/lib/__tests__/highlighter.unit.ts` — the same answers in a diff.
- `frontend/src/lib/utils/__tests__/editor-outline.unit.ts` — the outline fallback: the headings a Markdown file
  has, the `define`/`block` names a template has, and nothing for a file no extractor covers.
- `frontend/src/lib/syntax/__tests__/token-style.unit.ts` — every token `TOK` can emit, mounted in a real
  `EditorView` under both palettes and read back with `getComputedStyle`; the diff highlighter's classes; JSON's
  literals off the real core grammar; and the table against `styles/token-colors.css` in both directions, so a row
  with no rule, a rule nothing emits, or a second stylesheet naming a token class all fail it.
- `frontend/src/lib/utils/__tests__/editor-language.unit.svelte.ts` — resolution order (extension → fallback →
  plain text); the colors survive `editor_enabled: false` while the channel does not; a switched-off extension
  answers nothing; a fence tag answered by `language_id`, by `language`, by `extension`, or by the hovered file.
- `frontend/src/lib/utils/__tests__/lsp-doc-highlight.unit.svelte.ts` — a real `LSPClient` + `EditorView`
  rendering a gopls hover: the keyword class survives `DOMPurify` and resolves to a color, and the same input
  through a client without the hook yields nothing, which is the bug this test was written for.
- `frontend/tests/declared-languages-check.mjs` — the live probe: wire ids sniffed off the WebSocket, plus
  highlight, block and region folding, and Ctrl-/ in a real editor.
  `MIKE_CHECK_PROJECT=<scratch project> node tests/declared-languages-check.mjs` — it writes fixtures into that
  project, so the name is required.
- `frontend/tests/lsp-hover-highlight-check.mjs` — hover a real `fmt.Println` and read the computed colors off the
  tooltip, then check a TypeScript file in the same workspace: the grammar follows the open file, not the
  connection.
