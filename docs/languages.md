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
folder (live in the TypeScript profiles as `//#region` / `//#endregion`), an `indentService`, the
`commentTokens` / `closeBrackets` language data, and bracket pair colorization built from `pairs` —
colours, folding, indent, Ctrl-/ and auto-close all come from the same declaration.

## Where a name's colour comes from, and a bracket's

The word lists are a language's *vocabulary*; a name they do not contain gets its colour from its
**shape**, in this order: declared `keywords`/`types`/`constants`/`builtins` first, then a name with `(`
after it (spaces only between) is a function, then a name starting with an upper-case letter is a type,
then nothing. So `func` is a keyword, `formatChatHistory(` is a function and `types.ChatMessage`'s
`ChatMessage` is a type, while `messages` keeps the theme's default colour. Both shape rules default on
and are declared per profile as `call_as_function` / `capitalized_types` — declared, not required: an
extension installed before the fields existed highlights anyway (that is what the default is for), and an
author whose language breaks the convention (a Lisp, an Erlang, where variables start upper-case) has a
field to say it in. Go's exported constants (`EOF`, `MaxLen`) start upper-case and so read as types; that
is the accepted trade, not an open bug.

Bracket pair colorization follows VS Code's rule: a pair's colour is its **nesting level**, so an opener
always matches the closer it opened (`syntax/bracket-pairs.ts`). Levels count across lines, cycle over
three slots, skip anything inside a comment or a string, and colour nothing for a closer that opens
nothing. The colours belong to the theme (`.cm-bracket-l0|1|2`, VS Code's own values, different between
light and dark), which keeps the engine out of palettes. Two scan bounds and they are not the same
number: a fold needs its own line and body (`SCAN_LIMIT`), while a level needs everything above the
viewport, so the bracket scan has its own larger `BRACKET_SCAN_LIMIT` and colours nothing past it.

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
connected for. Colours come from the editor's mounted `HighlightStyle`, so a tooltip follows the theme
and no `hl-*` palette is duplicated for it.

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
`typescript`), `frontend/src/lib/syntax/__tests__/spec-language.unit.ts` (tokens, folds, indent out of a
profile, including what each shape rule does switched off),
`frontend/src/lib/syntax/__tests__/bracket-pairs.unit.ts` (levels, comments and strings, and the classes a
real `EditorView` puts in the DOM),
`frontend/src/lib/syntax/__tests__/bundled-profiles.unit.ts` (the shipped manifests, and a profile with
neither new key — the state of every install that predates them),
`frontend/src/lib/__tests__/highlighter.unit.ts` (the same answers in a diff),
`frontend/src/lib/utils/__tests__/editor-language.unit.svelte.ts` (resolution order: extension →
fallback → plain text, and a fence tag answered by `language_id`, by `language`, by `extension` or by the
hovered file), `frontend/src/lib/utils/__tests__/lsp-doc-highlight.unit.svelte.ts` (a real `LSPClient` +
`EditorView` rendering a gopls hover: the keyword class survives `DOMPurify` and resolves to a colour;
the same input through a client without the hook yields nothing, which is the bug), and the live probes
`frontend/tests/declared-languages-check.mjs` (wire ids sniffed off the WebSocket, plus highlight,
block/region folding, Ctrl-/ in a real editor:
`MIKE_CHECK_PROJECT=<scratch project> node tests/declared-languages-check.mjs` — it writes fixtures
into that project, so the name is required) and
`frontend/tests/lsp-hover-highlight-check.mjs` (hover a real `fmt.Println` and read the computed colours
off the tooltip, then check a TypeScript file in the same workspace: the grammar follows the open file,
not the connection).

## Rules the core keeps (moved from AGENTS.md)
### Language Knowledge Lives in Extensions
The core holds **no** language names, keyword lists, comment markers or bracket tables — neither in Go nor in the frontend. Why the design is shaped this way, and its tests: `docs/languages.md`.

- **`file_types[]`** (`{extension, language_id, label, syntax}`) is the routing table *and* the source of the `didOpen` languageId and the status-bar label. `language` is the **routing** key (which server owns the file); `language_id` is **per file type** — collapsing them breaks routing or the parse. Both sides read it through exactly one helper: `extensions.LSPConfig.fileTypeFor` (`MatchesFile`/`LanguageIDFor`, `ServerConfig.LanguageIDFor`) in Go, `editor-language.ts::resolveFile` in the browser — **never** a local map, and the browser's LSP channel takes its routing key from that same call. A code fence in a language server's own documentation (the ​```go signature a hover card opens with) is answered by `getGrammarForFence` in the same module — the same three fields, **no alias table**, then the core fallback table, then the grammar of the file the documentation is about — wired as `LSPClient`'s `highlightLanguage` by `lsp-client-config.ts`, the one factory **both** `LSPClient` constructions use.
- **`syntax{}`** is a grammar as **data, never code** - a shipped module would run in the app's realm. `frontend/src/lib/syntax/spec-language.ts` compiles a profile into a `StreamLanguage` + `foldService` + `indentService` + `commentTokens`/`closeBrackets` + bracket pair colorization (from `pairs`). A name the word lists do not cover is coloured by **shape**: `name(` is a function, an upper-case-first name is a type, everything else the default (`call_as_function`, `capitalized_types`; `*bool` in Go so an explicit `false` survives the round trip). Bracket colour is the pair's nesting level (`syntax/bracket-pairs.ts`), themed .cm-bracket-l0|1|2`. **Every scan that reads characters shares one reader** for "this range is not code" (`syntax/stream-tokens.ts`) — fold, indent, brackets — or a `"{"` becomes a bracket in one place and not another. A spec-built grammar is named `mike-declared`, deliberately not a valid language id, so a call site that forgets an explicit id fails loudly instead of silently. Tests: `frontend/src/lib/syntax/__tests__/`, `backend/extensions/manifest_validate_test.go`, `bundled_manifests_test.go`, and the tooltip path in `frontend/src/lib/utils/__tests__/lsp-doc-highlight.unit.svelte.ts`.
- **A bad manifest is refused, not stored** (`backend/extensions/manifest_validate.go`, on install, update and `PUT /api/extensions/{id}); **legacy manifests are normalized at load**, not migrated (`LSPConfig.NormalizeFileTypes()` in `scanExtension`), so runtime code sees one shape.
- **The one language table left in core** is `frontend/src/lib/utils/fallback-languages.ts` (json, markdown, html, css/scss/less, yaml, sql, `tmpl`/`gohtml`) — consulted only when no extension declares the file type, and it answers a markdown fence tag too (`fallbackLanguageForTag`), because a fence tag and a suffix name the same grammar. `highlighter.ts` (diffs) resolves through the same `getLanguageSupport`, so a diff and an editor can never disagree.
